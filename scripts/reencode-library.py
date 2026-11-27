#!/usr/bin/env python3
"""Independent, persistent video queue. Encoding requires an explicitly approved profile.

The VM reads a read-only source mount and publishes over the public filesystem API.
The VPS downloads original Drive files and publishes locally. Neither modifies sources
or legacy normalization records. All configuration and detailed state remain private.
"""
import argparse
import bisect
import concurrent.futures
import contextlib
import datetime
import fcntl
import hashlib
import http.client
import importlib.util
import json
import math
import os
import re
from pathlib import Path, PurePosixPath
import shlex
import shutil
import signal
import ssl
import subprocess
import threading
import time
import urllib.parse
import urllib.request
import uuid

VIDEO_EXTENSIONS={'.mp4','.mov','.mkv','.wmv','.avi','.webm','.m4v'}
RESERVED={'.filebeans-thumbnails','.filebeans-stage','.filebeans-vaults'}


def modified_ns(value):
    date=datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
    if date.tzinfo is None:raise ValueError('Modification date needs a timezone')
    delta=date.replace(microsecond=0)-datetime.datetime(1970,1,1,tzinfo=datetime.timezone.utc)
    fraction=re.search(r'\.(\d+)(?:Z|[+-]\d\d:\d\d)$',value)
    nanoseconds=int(fraction[1][:9].ljust(9,'0')) if fraction else 0
    return (delta.days*86400+delta.seconds)*10**9+nanoseconds


def relative(value):
    p=PurePosixPath(value)
    if p.is_absolute() or not p.parts or '..' in p.parts or any(x.casefold() in RESERVED for x in p.parts):
        raise ValueError('Unsafe library path')
    return str(p)


def atomic(path,value,mode=0o600):
    path.parent.mkdir(parents=True,exist_ok=True)
    temp=path.with_name(path.name+'.tmp')
    payload=json.dumps(value)
    with temp.open('w') as f:
        f.write(payload);f.flush();os.fsync(f.fileno())
    temp.chmod(mode);os.replace(temp,path)


def checksum(path,algorithm='sha256'):
    with path.open('rb') as f:return hashlib.file_digest(f,algorithm).hexdigest()


def profile_hash(profile):
    return hashlib.sha256(json.dumps({k:v for k,v in profile.items() if k not in ['approved','name']},sort_keys=True).encode()).hexdigest()


def approved(config):
    p=config.get('profile',{})
    return config.get('enabled') is True and p.get('approved') is True and p.get('keyframe_seconds')==1


def audio_args(stream):
    if stream is None:return []
    codec=stream.get('codec_name');channels=int(stream.get('channels',2));rate=int(stream.get('sample_rate',48000))
    if codec=='aac' and stream.get('profile')=='LC' and channels<=2 and rate<=48000:
        return ['-c:a','copy']
    channels=min(2,max(1,channels));source_rate=int(stream.get('bit_rate') or 0)
    target=96000 if channels==1 else 192000
    # Comparable older lossy codecs do not need an arbitrary bitrate increase.
    if codec in ['mp3','mp2','wmav1','wmav2','aac'] and source_rate:
        target=min(target,max(48000 if channels==1 else 64000,source_rate))
    elif codec in ['opus','vorbis'] and source_rate:
        target=min(target,max(96000 if channels==1 else 128000,int(source_rate*1.5)))
    valid=[8000,11025,12000,16000,22050,24000,32000,44100,48000]
    rate=min(valid,key=lambda x:abs(x-min(rate,48000)))
    return ['-c:a','aac','-profile:a','aac_low','-b:a',str(target),'-ac',str(channels),'-ar',str(rate)]


def validate_keyframe_timing(keys,duration,fps,source_frames=None,output_frames=None):
    if not keys:raise RuntimeError('Output contains no readable keyframes')
    gap=max([b-a for a,b in zip(keys,keys[1:])]+[max(0,duration-keys[-1])])
    if gap<=1+1/fps+0.03:return gap,False
    if source_frames is None or output_frames is None:
        raise RuntimeError('Keyframe gap %.3fs requires source timestamp verification'%gap)
    source_frames=sorted(source_frames);output_frames=sorted(output_frames)
    if not source_frames or len(source_frames)!=len(output_frames):
        raise RuntimeError('Video frame count changed: source %d, output %d'%(len(source_frames),len(output_frames)))
    tolerance=max(0.003,min(0.01,0.1/fps))
    offset=output_frames[0]-source_frames[0]
    error=max(abs(a+offset-b) for a,b in zip(source_frames,output_frames))
    if error>tolerance:raise RuntimeError('Video frame timestamps changed by %.3fs'%error)
    # A missing source frame cannot carry an IDR. Verify that each one-second
    # target has a keyframe on its first available output frame, with allowance
    # for timestamp rounding at the boundary. Never excuse a missed available frame.
    for second in range(1,math.floor(output_frames[-1])+1):
        index=bisect.bisect_left(output_frames,second)
        if index==len(output_frames):break
        target=output_frames[index];lower=target
        if index and output_frames[index-1]>=second-tolerance:lower=output_frames[index-1]
        key_index=bisect.bisect_left(keys,lower-tolerance)
        if key_index==len(keys) or keys[key_index]>target+tolerance:
            raise RuntimeError('Missing keyframe at first available frame after %ds'%second)
    return gap,True


class PublicAPI:
    def __init__(self,config):
        self.config=config;self.connection=None
        self.base=urllib.parse.urlsplit(config['api_base'])
        if self.base.scheme!='https' or not self.base.hostname:raise ValueError('Public HTTPS required')

    def close(self):
        if self.connection:self.connection.close()
        self.connection=None

    def call(self,method,route,query=None,data=None,headers=None,allow404=False):
        path=self.base.path.rstrip('/')+'/v1/'+route
        if query:path+='?'+urllib.parse.urlencode(query)
        h={'Authorization':'Bearer '+self.config['api_token']};h.update(headers or {})
        if isinstance(data,dict):data=json.dumps(data).encode();h['Content-Type']='application/json'
        for attempt in range(3):
            try:
                if not self.connection:self.connection=http.client.HTTPSConnection(self.base.hostname,self.base.port,timeout=60,context=ssl.create_default_context())
                self.connection.request(method,path,data,h);r=self.connection.getresponse();body=r.read()
                if r.status==404 and allow404:return None
                if r.status==409 and (method=='PUT' or (method=='POST' and route=='fs/folders')):return {'conflict':True}
                if not 200<=r.status<300:raise RuntimeError('API HTTP '+str(r.status))
                return json.loads(body) if body else {}
            except (OSError,http.client.HTTPException):
                self.close()
                if attempt==2:raise
                time.sleep(2**attempt)


class Worker:
    def __init__(self,config_path):
        self.config_path=Path(config_path);self.config=self.load();self.role=self.config['role']
        self.root=Path(self.config['work']);self.root.mkdir(parents=True,exist_ok=True)
        self.guard=(self.root/'worker.lock').open('w');fcntl.flock(self.guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        self.queue=json.loads(Path(self.config['queue']).read_text());self.jobs=self.queue['jobs']
        assert self.queue['role']==self.role
        assert len({j['id'] for j in self.jobs})==len(self.jobs)
        for j in self.jobs:relative(j['relative_path']);relative(j['output_relative']);assert Path(j['relative_path']).suffix.lower() in VIDEO_EXTENSIONS
        self.state_path=self.root/'state.json'
        self.state=json.loads(self.state_path.read_text()) if self.state_path.exists() else {'jobs':{},'active_profile':None,'started_at':None}
        self.lock=threading.RLock();self.stop=threading.Event();self.container='filebeans-reencode-'+self.role
        self.engine=(['sudo','-n'] if self.role=='vm' else [])+['docker']
        self.runtime=False;self.peak=0;self.working_memory=0;self.cpu_percent=0
        self.encoder_limit=int(self.config.get('encoders',1))
        self.max_active=int(self.config.get('max_active_jobs',self.encoder_limit+2))
        assert 1<=self.encoder_limit<=8 and self.encoder_limit<=self.max_active<=12
        self.prepare_gate=threading.BoundedSemaphore(1)
        self.encode_gate=threading.BoundedSemaphore(self.encoder_limit)
        self.validate_gate=threading.BoundedSemaphore(1)
        self.publish_gate=threading.BoundedSemaphore(1)
        self.pending_outputs=threading.BoundedSemaphore(self.encoder_limit+1)
        self.tasks=concurrent.futures.ThreadPoolExecutor(max_workers=self.max_active)
        self.futures={}

        spec=importlib.util.spec_from_file_location('media',Path(__file__).with_name('compare-video-encodings.py'))
        self.media=importlib.util.module_from_spec(spec);spec.loader.exec_module(self.media)
        for j in self.jobs:self.state['jobs'].setdefault(j['id'],{'phase':'pending','percent':0})
        for receipt_path in (self.root/'manual-accepted').glob('*.json'):
            receipt=json.loads(receipt_path.read_text());job_id=receipt_path.stem
            if job_id in self.state['jobs'] and receipt.get('manually_accepted') and receipt.get('phase')=='completed' and self.state['jobs'][job_id]['phase']!='completed':
                self.state['jobs'][job_id]=receipt

    def load(self):return json.loads(self.config_path.read_text())

    def row(self,job):return self.state['jobs'][job['id']]

    def save(self):
        with self.lock:atomic(self.state_path,self.state)

    def status(self):
        with self.lock:
            rows=[];totals={'completed':0,'failed':0,'review':0};done_bytes=0;done_seconds=0
            for j in self.jobs:
                r=self.row(j);phase=r['phase'];totals['completed']+=phase=='completed';totals['failed']+=phase=='failed';totals['review']+=bool(r.get('review'))
                if phase=='completed':done_bytes+=j['source_size'];done_seconds+=j.get('duration_seconds',0)
                rows.append({'number':j['number'],'phase':phase,'percent':r.get('percent',0),'encode_seconds':r.get('encode_seconds',0),'review':bool(r.get('review'))})
            phase='held' if not approved(self.config) else 'running'
            if approved(self.config) and totals['completed']+totals['failed']==len(rows):phase='finished'
            result={'role':self.role,'phase':phase,'hold_reason':('Paused' if self.config['profile'].get('approved') else 'Final encoding profile undecided') if phase=='held' else '',
                    'profile':self.config['profile']['name'],'profile_approved':self.config['profile'].get('approved',False),
                    'total':len(rows),**totals,'source_bytes':sum(j['source_size'] for j in self.jobs),'completed_source_bytes':done_bytes,
                    'completed_video_seconds':done_seconds,'total_video_seconds':sum(j.get('duration_seconds',0) for j in self.jobs),
                    'memory_limit_bytes':self.config['memory_bytes'],'peak_memory_bytes':self.peak,'started_at':self.state.get('started_at'),
                    'updated_at':time.time(),'jobs':rows,'encoders':self.encoder_limit,'max_active_jobs':self.max_active,
                    'active_encoders':sum(r['phase']=='encoding' for r in self.state['jobs'].values()),
                    'cpu_percent':self.cpu_percent,'cpu_count':os.cpu_count(),'working_memory_bytes':self.working_memory}
            if self.state.get('blocked'):result.update(phase='blocked',hold_reason=self.state['blocked'])
            atomic(Path(self.config['public_status']),result,0o644)

    def observe(self):
        last_remote=0
        while not self.stop.is_set():
            try:
                if self.runtime:
                    r=subprocess.run(self.engine+['exec',self.container,'cat','/sys/fs/cgroup/memory.peak'],capture_output=True,text=True,timeout=4)
                    if r.returncode==0:self.peak=max(self.peak,int(r.stdout))
                    memory=self.memory_usage()
                    if memory is not None:self.working_memory=memory
                    stats=subprocess.run(self.engine+['stats','--no-stream','--format','{{json .}}',self.container],capture_output=True,text=True,timeout=5)
                    if stats.returncode==0:self.cpu_percent=float(json.loads(stats.stdout)['CPUPerc'].rstrip('%'))
                self.status()
                if self.role=='vm' and time.time()-last_remote>=15:
                    last_remote=time.time()
                    remote=self.config['remote_status']
                    r=subprocess.run(['dev','ssh','-n','vps-admin','sudo cat '+shlex.quote(remote)],capture_output=True,text=True,timeout=8)
                    if r.returncode==0:
                        other=json.loads(r.stdout);assert other['role']=='vps'
                        atomic(Path(self.config['public_status']).with_name('vps.json'),other,0o644)
            except (OSError,ValueError,subprocess.TimeoutExpired):pass
            self.stop.wait(3)

    def ensure_runtime(self):
        if self.runtime:return
        if shutil.disk_usage(self.root).free<self.config['min_free_bytes']:raise RuntimeError('Insufficient scratch disk space')
        inspect=subprocess.run(self.engine+['inspect',self.container],capture_output=True)
        if inspect.returncode==0:subprocess.run(self.engine+['rm','-f',self.container],check=True,stdout=subprocess.DEVNULL)
        subprocess.run(self.engine+['run','-d','--name',self.container,'--network','none','--read-only','--memory',str(self.config['memory_bytes']),
            '--memory-swap',str(self.config['memory_bytes']),'--cpu-shares','128','--pids-limit',str(self.config.get('pids_limit',1024)),'--oom-score-adj','500','--cap-drop','ALL','--security-opt','no-new-privileges',
            '--mount','type=bind,src='+str(self.root)+',dst=/work','--entrypoint','sleep',self.config['image'],'infinity'],check=True,stdout=subprocess.DEVNULL)
        self.runtime=True

    def command(self,args,capture=False):
        result=subprocess.run(self.engine+['exec',self.container,'nice','-n','10','ionice','-c','3']+args,
            stdout=subprocess.PIPE if capture else subprocess.DEVNULL,stderr=subprocess.PIPE)
        if result.returncode:
            if self.stop.is_set():raise InterruptedError('Worker stopping')
            raise RuntimeError('Media command failed: '+result.stderr.decode(errors='replace')[-1200:])
        return result.stdout

    def probe(self,path):return json.loads(self.command(['ffprobe','-v','error','-show_streams','-show_format','-of','json','/work/'+path.name],True))

    def prepare(self,job):
        self.source_modified(job)
        row=self.row(job);target=self.root/(job['id']+'.input')
        if target.exists() and target.stat().st_size==job['source_size'] and checksum(target,'md5')==job['source_md5']:return target
        row['phase']='preparing';self.save();needed=self.config['min_free_bytes']+job['source_size']*2
        if shutil.disk_usage(self.root).free<needed:raise RuntimeError('Insufficient scratch disk space')
        partial=target.with_suffix('.part');digest=hashlib.md5()
        if self.role=='vm':
            source_root=Path(self.config['source_mount'])
            if not source_root.is_mount():raise RuntimeError('Source library mount unavailable')
            source=source_root/relative(job['relative_path']);before=source.stat()
            if before.st_size!=job['source_size']:raise RuntimeError('Source size changed')
            stream=source.open('rb')
        else:
            from urllib.parse import quote
            c=json.loads(Path(self.config['companion_config']).read_text());container=json.loads(subprocess.check_output(['docker','inspect','filebeans-companion']))[0]
            ip=next(x['IPAddress'] for x in container['NetworkSettings']['Networks'].values() if x['IPAddress'])
            url='http://'+ip+':'+c['listen'].rsplit(':',1)[-1]+'/v1/drive/files/'+quote(job['drive_id'],safe='')
            with urllib.request.urlopen(urllib.request.Request(url,headers={'Authorization':'Bearer '+c['token']}),timeout=60) as r:meta=json.load(r)
            if str(meta['version'])!=str(job['source_version']) or int(meta['size'])!=job['source_size']:raise RuntimeError('Drive source changed')
            state=next(Path(m['Source']) for m in container['Mounts'] if m['Destination']=='/state');token=json.loads((state/'google.json').read_text())['access_token']
            stream=urllib.request.urlopen(urllib.request.Request('https://www.googleapis.com/drive/v3/files/'+quote(job['drive_id'],safe='')+'?alt=media&supportsAllDrives=true',headers={'Authorization':'Bearer '+token}),timeout=60)
        try:
            with stream,partial.open('wb') as out:
                while not self.stop.is_set():
                    data=stream.read(1024*1024)
                    if not data:break
                    out.write(data);digest.update(data)
                out.flush();os.fsync(out.fileno())
            if self.stop.is_set():raise InterruptedError('Worker stopping')
            if partial.stat().st_size!=job['source_size'] or digest.hexdigest()!=job['source_md5']:raise RuntimeError('Original checksum mismatch')
            if self.role=='vm':
                after=source.stat()
                if after.st_size!=before.st_size or after.st_mtime_ns!=before.st_mtime_ns:raise RuntimeError('Source changed while reading')
            os.replace(partial,target)
            if self.role=='vps':os.chown(target,1000,1000)
            target.chmod(0o640);row['phase']='ready';self.save();return target
        finally:partial.unlink(missing_ok=True)

    def packet_rates(self,path,info):
        args=['ffprobe','-v','error','-show_packets','-show_entries','packet=stream_index,size','-of','csv=p=0','/work/'+path.name]
        proc=subprocess.Popen(self.engine+['exec',self.container]+args,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True)
        sizes={}
        for line in proc.stdout:
            fields=line.strip().split(',')
            try:index,size=int(fields[0]),int(fields[1])
            except (ValueError,IndexError):continue
            sizes[index]=sizes.get(index,0)+size
        proc.stdout.close()
        if proc.wait():
            if self.stop.is_set():raise InterruptedError('Worker stopping')
            raise RuntimeError('Packet inspection failed')
        duration=float(info['format']['duration']);video=self.media.video_stream(info)
        audio=next((s for s in info['streams'] if s['codec_type']=='audio'),None)
        return sizes.get(video['index'],0)*8/duration,(sizes.get(audio['index'],0)*8/duration if audio else 0)

    def encode(self,job,source):
        row=self.row(job);output=self.root/(job['id']+'.mp4')
        if row.get('output_sha256') and output.exists() and checksum(output)==row['output_sha256']:return output
        info=self.probe(source);video=self.media.video_stream(info);audio=next((s for s in info['streams'] if s['codec_type']=='audio'),None)
        p=self.config['profile'];fps_parts=video.get('avg_frame_rate','30/1').split('/');fps=float(fps_parts[0])/float(fps_parts[1]) if float(fps_parts[1]) else 30
        fps=max(0.01,fps);hdr=video.get('color_transfer') in ['smpte2084','arib-std-b67'];codec=p['codec'];row['audio_mode']='copy' if '-c:a' in audio_args(audio) and audio_args(audio)[-1]=='copy' else 'convert' if audio else 'none'
        args=['ffmpeg','-v','error','-nostdin','-filter_threads','0','-i','/work/'+source.name,'-map','0:'+str(video['index'])]
        if audio:args+=['-map','0:'+str(audio['index'])]+audio_args(audio)
        args+=['-vf',self.media.filters(hdr),'-c:v','libx265' if codec=='hevc' else 'libx264','-preset',p['preset'],'-crf',str(p['crf']),'-pix_fmt','yuv420p10le' if codec=='hevc' else 'yuv420p','-threads','0','-g',str(math.ceil(fps)),'-keyint_min','1','-force_key_frames','expr:gte(t,n_forced*1)','-fps_mode','passthrough']
        if codec=='hevc':args+=['-x265-params','log-level=error:open-gop=0:keyint='+str(math.ceil(fps))+':min-keyint=1','-forced-idr','1','-tag:v','hvc1']
        else:args+=['-profile:v','high','-x264-params','open-gop=0','-tag:v','avc1']
        if hdr:args+=['-color_primaries','bt709','-color_trc','bt709','-colorspace','bt709']
        args+=['-movflags','+faststart','-progress','pipe:1','-nostats','/work/'+output.name]
        duration=float(info['format']['duration'])
        output.unlink(missing_ok=True);row.update(phase='ready',percent=0);self.save()
        with self.slot(self.encode_gate):
            self.wait_for_memory()
            row.update(phase='encoding',percent=0);self.save();start=time.monotonic()
            with (self.root/(job['id']+'.log')).open('wb') as error:
                child=subprocess.Popen(self.engine+['exec',self.container,'nice','-n','10','ionice','-c','3']+args,stdout=subprocess.PIPE,stderr=error,text=True)
                for line in child.stdout:
                    key,_,value=line.strip().partition('=')
                    if key=='out_time_us':
                        try:row['percent']=min(99.9,max(0,float(value)/1000000/duration*100))
                        except ValueError:pass
                    row['encode_seconds']=time.monotonic()-start
                child.stdout.close();code=child.wait()
                if self.stop.is_set():raise InterruptedError('Worker stopping')
                if code:raise RuntimeError('Encoding failed; see private job log')
            row.update(phase='waiting for validation',encode_seconds=time.monotonic()-start);self.save()
        with self.slot(self.validate_gate):
            row['phase']='validating';self.save()
            return self.validate(job,source,output,info,audio,fps,duration)

    def validate(self,job,source,output,info,audio,fps,duration):
        row=self.row(job)
        codec=self.config['profile']['codec']
        self.command(['ffmpeg','-v','error','-nostdin','-xerror','-threads','0','-i','/work/'+output.name,'-map','0:v:0','-map','0:a:0?','-f','null','-'])
        out=self.probe(output);out_video=self.media.video_stream(out)
        if out_video['codec_name']!=codec:raise RuntimeError('Output video codec does not match selected profile')
        delta=float(out['format']['duration'])-duration
        if abs(delta)>=max(0.6,1/fps):raise RuntimeError('Output duration differs from source by %.3fs'%delta)
        out_audio=next((s for s in out['streams'] if s['codec_type']=='audio'),None)
        if bool(out_audio)!=bool(audio):raise RuntimeError('Output audio presence differs from source')
        if out_audio and not (out_audio['codec_name']=='aac' and out_audio['profile']=='LC' and int(out_audio['channels'])<=2):raise RuntimeError('Output audio is not compatible AAC-LC mono/stereo')
        frames=json.loads(self.command(['ffprobe','-v','error','-select_streams','v:0','-skip_frame','nokey','-show_frames','-show_entries','frame=best_effort_timestamp_time','-of','json','/work/'+output.name],True))
        times=[float(f['best_effort_timestamp_time']) for f in frames['frames'] if 'best_effort_timestamp_time' in f]
        duration_out=float(out['format']['duration'])
        gap=max([b-a for a,b in zip(times,times[1:])]+[max(0,duration_out-times[-1])]) if times else float('inf')
        source_frames=output_frames=None
        if gap>1+1/fps+0.03:
            source_frames=self.video_timeline(source);output_frames=self.video_timeline(output)
        gap,timing_exception=validate_keyframe_timing(times,duration_out,fps,source_frames,output_frames)
        row.pop('error',None)
        row['source_timing_exception']=timing_exception
        source_video,source_audio=self.packet_rates(source,info);output_video,output_audio=self.packet_rates(output,out)
        row.update(phase='uploading',percent=100,source_video_bps=source_video,output_video_bps=output_video,source_audio_bps=source_audio,output_audio_bps=output_audio,
            output_size=output.stat().st_size,size_change_percent=100*(output.stat().st_size/job['source_size']-1),review=bool(source_video and output_video>source_video*(1+self.config.get('review_increase',0.20))),
            output_sha256=checksum(output),keyframe_max_seconds=gap,profile_hash=self.state['active_profile'])
        self.save();return output

    def video_timeline(self,path):
        data=json.loads(self.command(['ffprobe','-v','error','-select_streams','v:0','-show_packets','-show_entries','packet=pts_time','-of','json','/work/'+path.name],True))
        frames=[]
        for packet in data['packets']:
            if 'pts_time' not in packet:raise RuntimeError('Source frame timestamps are unavailable')
            frames.append(float(packet['pts_time']))
        return frames

    def recover_outputs(self):
        if self.config.get('enabled'):raise RuntimeError('Stop and disable the worker before recovering retained outputs')
        result={'recovered':[],'still_failed':[],'unfinished':[]}
        self.ensure_runtime()
        try:
            for job in self.jobs:
                row=self.row(job);output=self.root/(job['id']+'.mp4')
                if row['phase']=='completed' or not output.exists():continue
                previous=row['phase']
                try:
                    if not row.get('output_sha256'):
                        try:self.probe(output)
                        except RuntimeError:
                            if previous=='failed':raise
                            result['unfinished'].append(job['number']);continue
                        source=self.prepare(job);info=self.probe(source);video=self.media.video_stream(info);audio=next((a for a in info['streams'] if a['codec_type']=='audio'),None)
                        n,d=map(float,video.get('avg_frame_rate','30/1').split('/'));fps=max(0.01,n/d if d else 30)
                        self.validate(job,source,output,info,audio,fps,float(info['format']['duration']))
                    elif checksum(output)!=row['output_sha256']:raise RuntimeError('Retained output checksum changed')
                    self.deliver(job,output)
                    if row['phase']!='completed':raise RuntimeError(row.get('error','Publication failed'))
                    (self.root/(job['id']+'.input')).unlink(missing_ok=True)
                    result['recovered'].append(job['number'])
                    print(json.dumps({'number':job['number'],'recovered':True,'source_timing_exception':row.get('source_timing_exception',False)}),flush=True)
                except Exception as e:
                    row.update(phase='failed',error=str(e));result['still_failed'].append({'number':job['number'],'error':str(e)});self.save()
                    print(json.dumps({'number':job['number'],'error':str(e)}),flush=True)
            self.save();self.status();return result
        finally:
            subprocess.run(self.engine+['rm','-f',self.container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);self.runtime=False
            self.tasks.shutdown(wait=True)

    def verify_remote(self,job,row):
        path=str(PurePosixPath('/folder/pics')/relative(job['output_relative']))
        modified=self.source_modified(job)
        code="\n".join([
            "import hashlib,json,os",
            "with os.fdopen(os.open("+repr(path)+",os.O_RDONLY|os.O_NOFOLLOW),'rb') as f:",
            " s=os.fstat(f.fileno());h=hashlib.file_digest(f,'sha256').hexdigest();a=os.fstat(f.fileno())",
            " if (s.st_ino,s.st_size,s.st_mtime_ns,s.st_ctime_ns)!=(a.st_ino,a.st_size,a.st_mtime_ns,a.st_ctime_ns) or s.st_size!="+repr(row['output_size'])+" or h!="+repr(row['output_sha256'])+":raise RuntimeError('Published checksum mismatch')",
            " os.utime(f.fileno(),ns=(s.st_atime_ns,"+repr(modified)+"));os.fsync(f.fileno())",
            " print(json.dumps({'size':s.st_size,'sha256':h,'modified_ns':os.fstat(f.fileno()).st_mtime_ns}))"])
        result=json.loads(subprocess.check_output(['dev','ssh','-n','vps-admin','sudo python3 -c '+shlex.quote(code)],timeout=60))
        if result['size']!=row['output_size'] or result['sha256']!=row['output_sha256']:raise RuntimeError('Published checksum mismatch')
        if result['modified_ns']!=modified:raise RuntimeError('Published modification date mismatch')

    def source_modified(self,job):
        row=self.row(job)
        if 'source_modified_ns' not in row:
            if self.role=='vm':
                root=Path(self.config['source_mount'])
                if not root.is_mount():raise RuntimeError('Source library mount unavailable')
                info=(root/relative(job['relative_path'])).stat()
                if info.st_size!=job['source_size']:raise RuntimeError('Source size changed')
                row['source_modified_ns']=info.st_mtime_ns
            else:
                c=json.loads(Path(self.config['companion_config']).read_text())
                container=json.loads(subprocess.check_output(['docker','inspect','filebeans-companion']))[0]
                ip=next(x['IPAddress'] for x in container['NetworkSettings']['Networks'].values() if x['IPAddress'])
                url='http://'+ip+':'+c['listen'].rsplit(':',1)[-1]+'/v1/drive/files/'+urllib.parse.quote(job['drive_id'],safe='')
                with urllib.request.urlopen(urllib.request.Request(url,headers={'Authorization':'Bearer '+c['token']}),timeout=60) as response:meta=json.load(response)
                if str(meta['version'])!=str(job['source_version']) or int(meta['size'])!=job['source_size']:raise RuntimeError('Drive source changed')
                row['source_modified_ns']=modified_ns(meta['modifiedTime'])
            self.save()
        return row['source_modified_ns']

    def deliver(self,job,output):
        row=self.row(job);row['phase']='uploading';self.save();start=time.monotonic()
        try:
            if self.role=='vm':
                api=PublicAPI(self.config);path='/pics/'+relative(job['output_relative']);q={'source':'VPS','path':path};parent=PurePosixPath(path).parent
                for n in range(2,len(parent.parts)+1):
                    folder=str(PurePosixPath(*parent.parts[:n]));query={'source':'VPS','path':folder}
                    if api.call('GET','fs/resources',query,allow404=True) is None:api.call('POST','fs/folders',query)
                row.setdefault('upload_session',str(uuid.uuid4()));self.save();route='fs/uploads/'+row['upload_session']
                if api.call('GET','fs/resources',q,allow404=True) is None:
                    api.call('POST',route,q,{'size':row['output_size']});state=api.call('GET',route,q)
                    while not state.get('completed') and state['received']<row['output_size']:
                        offset=state['received']
                        with output.open('rb') as f:f.seek(offset);chunk=f.read(4*1024*1024)
                        try:api.call('PUT',route,q,chunk,{'Content-Type':'application/octet-stream','Content-Range':'bytes '+str(offset)+'-'+str(offset+len(chunk)-1)+'/'+str(row['output_size'])})
                        except (OSError,http.client.HTTPException):api.close()
                        state=api.call('GET',route,q)
                    if not state.get('completed'):api.call('POST',route+'/complete',q)
                self.verify_remote(job,row);api.call('DELETE',route,q,allow404=True);api.close()
            else:
                destination=Path(self.config['destination'])/relative(job['output_relative'])
                root=Path(self.config['destination'])
                for parent in list(destination.parent.relative_to(root).parents)[::-1]+[destination.parent.relative_to(root)]:
                    folder=root/parent
                    try:folder.mkdir(mode=0o770);os.chown(folder,1000,1000);folder.chmod(0o770)
                    except FileExistsError:
                        if not folder.is_dir() or folder.is_symlink():raise RuntimeError('Unsafe output directory')
                if destination.exists():
                    if checksum(destination)!=row['output_sha256']:raise RuntimeError('Destination conflict')
                else:
                    os.chown(output,1000,1000);output.chmod(0o660);os.link(output,destination)
                    fd=os.open(destination.parent,os.O_DIRECTORY);os.fsync(fd);os.close(fd)
                modified=self.source_modified(job)
                with os.fdopen(os.open(destination,os.O_RDONLY|os.O_NOFOLLOW),'rb') as published:
                    info=os.fstat(published.fileno())
                    if hashlib.file_digest(published,'sha256').hexdigest()!=row['output_sha256']:raise RuntimeError('Published checksum mismatch')
                    current=os.fstat(published.fileno())
                    if (info.st_size,info.st_mtime_ns,info.st_ctime_ns)!=(current.st_size,current.st_mtime_ns,current.st_ctime_ns):raise RuntimeError('Published file changed')
                    os.utime(published.fileno(),ns=(info.st_atime_ns,modified));os.fsync(published.fileno())
                    if os.fstat(published.fileno()).st_mtime_ns!=modified:raise RuntimeError('Published modification date mismatch')
            row.update(phase='completed',delivered_at=time.time(),delivery_seconds=time.monotonic()-start);self.save();output.unlink(missing_ok=True)
            (self.root/(job['id']+'.log')).unlink(missing_ok=True)
        except Exception as e:
            if self.stop.is_set():raise InterruptedError('Worker stopping') from e
            row.update(phase='failed',error=str(e));self.save()

    @contextlib.contextmanager
    def slot(self,gate):
        while not self.stop.is_set():
            if gate.acquire(timeout=0.5):break
        else:raise InterruptedError('Worker stopping')
        try:
            if self.stop.is_set():raise InterruptedError('Worker stopping')
            yield
        finally:gate.release()

    def memory_usage(self):
        if not self.runtime:return 0
        try:
            r=subprocess.run(self.engine+['exec',self.container,'cat','/sys/fs/cgroup/memory.current','/sys/fs/cgroup/memory.stat'],capture_output=True,text=True,timeout=5)
            if r.returncode:return None
            lines=r.stdout.splitlines();current=int(lines[0]);inactive=next((int(line.split()[1]) for line in lines[1:] if line.startswith('inactive_file ')),0)
            return max(0,current-inactive)
        except (ValueError,subprocess.TimeoutExpired):return None

    def wait_for_memory(self):
        limit=int(self.config['memory_bytes']*self.config.get('memory_admission_ratio',0.85))
        reserve=int(self.config.get('encoder_reserve_bytes',512*1024**2))
        while not self.stop.is_set():
            memory=self.memory_usage()
            active=sum(r['phase']=='encoding' for r in self.state['jobs'].values())
            if (memory is not None and memory+reserve<limit) or (memory is None and active==0):return
            self.stop.wait(1)
        raise InterruptedError('Worker stopping')

    def request_stop(self,*_):
        self.stop.set()
        if self.runtime:
            subprocess.run(self.engine+['kill',self.container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=15)

    def process_job(self,job,fingerprint):
        row=self.row(job)
        try:
            with self.slot(self.prepare_gate):source=self.prepare(job)
            if not approved(self.load()) or profile_hash(self.load()['profile'])!=fingerprint:raise InterruptedError('Queue paused')
            with self.slot(self.pending_outputs):
                output=self.encode(job,source);source.unlink(missing_ok=True)
                with self.slot(self.publish_gate):self.deliver(job,output)
        except InterruptedError:
            if row['phase']!='completed':
                row['phase']='ready' if (self.root/(job['id']+'.input')).exists() else 'pending'
                row.pop('error',None);self.save()
        except OSError as e:
            if self.role=='vm' and row['phase']=='preparing':
                row['phase']='pending';self.state['blocked']='Source library temporarily unavailable';self.save()
            else:row.update(phase='failed',error=str(e));self.save()
        except Exception as e:
            if self.stop.is_set():
                row['phase']='ready' if (self.root/(job['id']+'.input')).exists() else 'pending';row.pop('error',None)
            else:row.update(phase='failed',error=str(e))
            self.save()

    def run(self):
        for sig in [signal.SIGTERM,signal.SIGINT]:signal.signal(sig,self.request_stop)
        observer=threading.Thread(target=self.observe,daemon=True);observer.start()
        try:
            while not self.stop.is_set():
                self.config=self.load()
                for future in list(self.futures):
                    if future.done():future.result();del self.futures[future]
                if not approved(self.config):self.stop.wait(1);continue
                if self.role=='vm' and not Path(self.config['source_mount']).is_mount():
                    self.state['blocked']='Source library mount unavailable';self.save();self.stop.wait(10);continue
                self.state.pop('blocked',None)
                fingerprint=profile_hash(self.config['profile'])
                if self.state['active_profile'] and self.state['active_profile']!=fingerprint:
                    self.state['blocked']='Encoding profile changed; explicit queue review required';self.save();self.stop.wait(3);continue
                self.state['active_profile']=fingerprint;self.state['started_at']=self.state.get('started_at') or time.time();self.ensure_runtime();self.save()
                active_ids={j['id'] for j in self.futures.values()}
                pending=[j for j in self.jobs if j['id'] not in active_ids and self.row(j)['phase'] not in ['completed','failed']]
                for job in pending[:max(0,self.max_active-len(self.futures))]:
                    self.futures[self.tasks.submit(self.process_job,job,fingerprint)]=job
                self.stop.wait(1)
        finally:
            self.request_stop();self.tasks.shutdown(wait=True,cancel_futures=True)
            if self.runtime:subprocess.run(self.engine+['rm','-f',self.container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            observer.join(timeout=12);self.status();self.save()


def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--config',type=Path,required=True);parser.add_argument('--check',action='store_true');parser.add_argument('--recover',action='store_true');a=parser.parse_args()
    worker=Worker(a.config)
    if a.check:worker.status();worker.save();print(json.dumps({'role':worker.role,'jobs':len(worker.jobs),'approved':approved(worker.config)}))
    elif a.recover:print(json.dumps(worker.recover_outputs()),flush=True)
    else:worker.run()


if __name__=='__main__':main()
