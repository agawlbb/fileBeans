#!/usr/bin/env python3
"""Thirty-minute, isolated option-3 delivery benchmark. No normalization jobs are changed.

Run this controller on each host against the same prepared FFmpeg image and manifest.
VM sources are staged before timing. VPS sources are downloaded during timing.
"""
import argparse
import concurrent.futures
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import queue
import shlex
import signal
import subprocess
import threading
import time
import urllib.request


def atomic(path, value):
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(value, indent=2))
    os.replace(temp, path)


def source_access(config_path, drive_id):
    config = json.loads(Path(config_path).read_text())
    container = json.loads(subprocess.check_output(['docker', 'inspect', 'filebeans-companion']))[0]
    ip = next(v['IPAddress'] for v in container['NetworkSettings']['Networks'].values() if v['IPAddress'])
    base = 'http://' + ip + ':' + config['listen'].rsplit(':', 1)[-1]
    request = urllib.request.Request(base + '/v1/drive/files/' + drive_id,
                                     headers={'Authorization': 'Bearer ' + config['token']})
    with urllib.request.urlopen(request, timeout=30) as response:
        metadata = json.load(response)
    state = next(Path(m['Source']) for m in container['Mounts'] if m['Destination'] == '/state')
    token = json.loads((state / 'google.json').read_text())['access_token']
    return metadata, token


class Benchmark:
    def __init__(self, args):
        self.args = args
        self.root = args.work.resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.manifest = json.loads((self.root / 'manifest.json').read_text())
        self.lock = threading.RLock()
        self.halt = threading.Event()
        self.engine = (['sudo'] if args.role == 'vm' else []) + ['docker']
        self.name = 'filebeans-benchmark-' + args.role
        self.end = args.start + args.seconds
        self.rows = [dict(number=n+1, state='queued', duration_seconds=x['duration_seconds'],
                          source_bytes=int(x['size']), encode_seconds=0, percent=0)
                     for n, x in enumerate(self.manifest)]
        self.state = dict(role=args.role, phase='preparing', start_at=args.start,
                          deadline=self.end, memory_limit_bytes=args.memory,
                          delivered_count=0, delivered_video_seconds=0,
                          encoded_count=0, encoded_video_seconds=0,
                          download_bytes=0, upload_bytes=0, encoder_idle_seconds=0,
                          peak_memory_bytes=0, cpu_percent=0, cpu_count=os.cpu_count(),
                          peak_host_memory_total_bytes=0, options=self.rows)
        self.upload_pool = concurrent.futures.ThreadPoolExecutor(max_workers=1)
        self.uploads = []
        self.spec = importlib.util.spec_from_file_location('comparison', self.root / 'compare-video-encodings.py')
        self.media = importlib.util.module_from_spec(self.spec)
        self.spec.loader.exec_module(self.media)

    def save(self):
        with self.lock:
            self.state['updated_at'] = time.time()
            self.state['elapsed_seconds'] = max(0, min(self.args.seconds, time.time()-self.args.start))
            atomic(self.root / (self.args.role + '.json'), self.state)

    def remaining(self):
        return max(0, self.end - time.time())

    def command(self, args, capture=False):
        if self.remaining() <= 0:
            raise TimeoutError('benchmark deadline')
        return subprocess.run(args, check=True, timeout=self.remaining(),
                              stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                              stderr=subprocess.PIPE if capture else subprocess.DEVNULL)

    def monitor(self):
        while not self.halt.is_set():
            memory={line.split(':',1)[0]:int(line.split()[1])*1024 for line in Path('/proc/meminfo').read_text().splitlines() if line.startswith(('MemTotal:', 'MemAvailable:'))}
            self.state.update(host_memory_total_bytes=memory['MemTotal'],host_memory_available_bytes=memory['MemAvailable'])
            self.state['peak_host_memory_total_bytes']=max(self.state['peak_host_memory_total_bytes'],memory['MemTotal'])
            try:
                r = subprocess.run(self.engine + ['stats', '--no-stream', '--format', '{{json .}}', self.name],
                                   capture_output=True, text=True, timeout=4)
                if r.returncode == 0 and r.stdout.strip():
                    stats = json.loads(r.stdout)
                    self.state['cpu_percent'] = float(stats['CPUPerc'].rstrip('%'))
                r = subprocess.run(self.engine + ['exec', self.name, 'cat', '/sys/fs/cgroup/memory.peak'],
                                   capture_output=True, text=True, timeout=3)
                if r.returncode == 0:
                    self.state['peak_memory_bytes'] = max(self.state['peak_memory_bytes'], int(r.stdout))
            except (ValueError, subprocess.TimeoutExpired):
                pass
            self.save()
            if self.args.role == 'vm':
                try:
                    r = subprocess.run(['dev', 'ssh', 'vps-admin', 'sudo cat ' + shlex.quote(str(self.args.remote_work / 'vps.json'))],
                                       capture_output=True, text=True, timeout=5)
                    if r.returncode == 0:
                        remote = json.loads(r.stdout)
                        atomic(self.root / 'status.json', dict(updated_at=time.time(), vm=self.state, vps=remote))
                except (ValueError, subprocess.TimeoutExpired):
                    pass
            self.halt.wait(5)

    def download(self, item, row):
        row['state'] = 'downloading'
        started = time.monotonic()
        metadata, token = source_access(self.args.config, item['id'])
        assert int(metadata['size']) == int(item['size']) and str(metadata['version']) == str(item['version'])
        target = self.root / 'inputs' / (str(row['number']) + '.source')
        request = urllib.request.Request('https://www.googleapis.com/drive/v3/files/' + item['id'] + '?alt=media&supportsAllDrives=true',
                                         headers={'Authorization': 'Bearer ' + token})
        digest = hashlib.md5()
        with urllib.request.urlopen(request, timeout=min(30, max(1, self.remaining()))) as response, target.open('wb') as output:
            while self.remaining() > 0 and not self.halt.is_set():
                data = response.read(256*1024)
                if not data:
                    break
                output.write(data)
                digest.update(data)
                with self.lock:
                    self.state['download_bytes'] += len(data)
        if self.remaining() <= 0:
            raise TimeoutError('download deadline')
        assert target.stat().st_size == int(item['size']) and digest.hexdigest() == item['md5Checksum']
        row.update(download_seconds=time.monotonic()-started, state='ready')
        return target

    def encode(self, item, row, source):
        index = row['number']
        output = self.root / ('Sample ' + str(index) + '.mp4')
        command = self.media.encode_args('/work/inputs/' + source.name, '/work/' + output.name,
                                         item['probe'], self.media.OPTIONS[2])
        command[command.index('-threads')+1] = '0'
        command[command.index('-filter_threads')+1] = '0'
        rate = self.media.video_stream(item['probe'])['avg_frame_rate'].split('/')
        fps = float(rate[0])/float(rate[1])
        command[command.index('-x265-params')+1] = 'log-level=error:open-gop=0:keyint=' + str(math.ceil(fps)) + ':min-keyint=1'
        command[command.index('-progress')+1] = '/work/encode-progress.txt'
        row['state'] = 'encoding'
        started = time.monotonic()
        with (self.root / ('encode-' + str(index) + '.log')).open('wb') as log:
            child = subprocess.Popen(self.engine + ['exec', self.name, 'nice', '-n', '10', 'ionice', '-c', '3'] + command,
                                     stdout=subprocess.DEVNULL, stderr=log)
            try:
                while child.poll() is None:
                    if self.remaining() <= 0:
                        raise TimeoutError('encoding deadline')
                    row['encode_seconds'] = time.monotonic()-started
                    progress = self.root / 'encode-progress.txt'
                    if progress.exists():
                        values = [line.split('=',1)[1] for line in progress.read_text().splitlines() if line.startswith('out_time_us=')]
                        if values:
                            row['percent'] = min(99.9, max(0, float(values[-1])/1000000/item['duration_seconds']*100))
                    self.halt.wait(min(1, self.remaining()))
                if child.returncode:
                    raise RuntimeError('encode failed; see private log')
            finally:
                if child.poll() is None:
                    subprocess.run(self.engine + ['kill', self.name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    child.wait()
        row.update(encode_seconds=time.monotonic()-started, state='validating', percent=100, output_bytes=output.stat().st_size)
        validated = time.monotonic()
        self.command(self.engine + ['exec', self.name, 'nice', '-n', '10', 'ionice', '-c', '3', 'ffmpeg', '-v', 'error', '-nostdin', '-xerror', '-threads', '0',
                                    '-i', '/work/' + output.name, '-map', '0:v:0', '-map', '0:a:0?', '-f', 'null', '-'])
        probe = json.loads(self.command(self.engine + ['exec', self.name, 'ffprobe', '-v', 'error', '-show_streams', '-show_format', '-of', 'json', '/work/' + output.name], True).stdout)
        video = self.media.video_stream(probe)
        assert video['codec_name']=='hevc' and video['pix_fmt']=='yuv420p10le'
        assert abs(float(probe['format']['duration'])-item['duration_seconds'])<0.6
        audio = [s for s in probe['streams'] if s['codec_type']=='audio']
        assert bool(audio)==any(s['codec_type']=='audio' for s in item['probe']['streams'])
        if audio:
            assert audio[0]['codec_name']=='aac' and audio[0]['profile']=='LC' and audio[0]['channels']==2 and audio[0]['sample_rate']=='48000'
        keys = json.loads(self.command(self.engine + ['exec', self.name, 'ffprobe', '-v', 'error', '-select_streams', 'v:0', '-skip_frame', 'nokey',
                    '-show_frames', '-show_entries', 'frame=best_effort_timestamp_time', '-of', 'json', '/work/' + output.name], True).stdout)
        times=[float(x['best_effort_timestamp_time']) for x in keys['frames'] if 'best_effort_timestamp_time' in x]
        assert times
        maximum=max([b-a for a,b in zip(times,times[1:])]+[float(probe['format']['duration'])-times[-1]])
        assert maximum<=1+1/fps+0.03
        row.update(validation_seconds=time.monotonic()-validated, keyframe_max_seconds=maximum, state='upload queued')
        with self.lock:
            self.state['encoded_count'] += 1
            self.state['encoded_video_seconds'] += item['duration_seconds']
        self.uploads.append(self.upload_pool.submit(self.deliver, output, row))

    def deliver(self, output, row):
        row['state'] = 'uploading' if self.args.role=='vm' else 'publishing'
        started = time.monotonic()
        destination = self.args.destination / self.args.role
        try:
            if self.args.role=='vm':
                self.command(['dev', 'rsync', '-rt', '--protect-args', '--chown=1000:1000', '--chmod=F660,D770', '--rsync-path=sudo rsync',
                              str(output), 'vps-admin:' + str(destination) + '/'])
                check = 'sudo sha256sum ' + shlex.quote(str(destination / output.name))
                remote_hash = self.command(['dev', 'ssh', 'vps-admin', check], True).stdout.decode().split()[0]
            else:
                # Destination and scratch must be on the same production filesystem.
                os.chown(output,1000,1000);output.chmod(0o660)
                os.replace(output, destination/output.name)
                output=destination/output.name
                remote_hash = hashlib.file_digest(output.open('rb'), 'sha256').hexdigest()
            digest = hashlib.file_digest(output.open('rb'), 'sha256').hexdigest()
            assert digest==remote_hash
            if self.remaining()<=0:
                raise TimeoutError('delivery deadline')
            row.update(state='delivered', delivery_seconds=time.monotonic()-started, delivered_at=time.time())
            with self.lock:
                self.state['delivered_count'] += 1
                self.state['delivered_video_seconds'] += row['duration_seconds']
                if self.args.role=='vm':self.state['upload_bytes'] += row['output_bytes']
        except (subprocess.TimeoutExpired,TimeoutError):
            row['state']='delivery incomplete at deadline'
        except Exception as error:
            row.update(state='delivery failed', error=type(error).__name__)
        self.save()

    def run(self):
        for part in ['inputs']:(self.root/part).mkdir(exist_ok=True)
        if (self.root/(self.args.role+'.json')).exists():
            raise RuntimeError('refusing to overwrite an existing run')
        self.save()
        subprocess.run(self.engine + ['run', '-d', '--name', self.name, '--network', 'none', '--read-only',
                       '--memory', str(self.args.memory), '--memory-swap', str(self.args.memory), '--cpu-shares', '128',
                       '--pids-limit', '256', '--oom-score-adj', '500', '--mount', 'type=bind,src='+str(self.root)+',dst=/work',
                       '--entrypoint', 'sleep', self.args.image, 'infinity'], check=True, stdout=subprocess.DEVNULL)
        monitor=threading.Thread(target=self.monitor,daemon=True);monitor.start()
        ready=queue.Queue(maxsize=1)
        def prepare():
            for item,row in zip(self.manifest,self.rows):
                if self.remaining()<=0 or self.halt.is_set():break
                try:
                    source=self.download(item,row)
                    while not self.halt.is_set() and self.remaining()>0:
                        try:ready.put((item,row,source),timeout=1);break
                        except queue.Full:pass
                except Exception as error:
                    row.update(state='download failed',error=type(error).__name__)
                    if self.remaining()<=0:break
            ready.put(None) if self.remaining()>0 and not self.halt.is_set() else None
        try:
            self.state['phase']='waiting for start'
            while time.time()<self.args.start:time.sleep(min(0.25,self.args.start-time.time()))
            self.state['phase']='running'
            if self.args.role=='vps':threading.Thread(target=prepare,daemon=True).start()
            for item,row in zip(self.manifest,self.rows):
                if self.remaining()<=0:break
                if self.args.role=='vps':
                    waiting=time.monotonic()
                    try:entry=ready.get(timeout=self.remaining())
                    except queue.Empty:break
                    self.state['encoder_idle_seconds']+=time.monotonic()-waiting
                    if entry is None:break
                    item,row,source=entry
                else:source=self.root/'inputs'/(str(row['number'])+'.source')
                try:self.encode(item,row,source)
                except (TimeoutError,subprocess.TimeoutExpired):
                    row['state']='unfinished at deadline';break
                except Exception as error:
                    row.update(state='failed',error=type(error).__name__)
                finally:
                    if self.args.role=='vps' and source.exists():source.unlink()
            self.state['phase']='finishing transfers'
            for future in self.uploads:
                try:future.result(timeout=max(0.1,self.remaining()))
                except concurrent.futures.TimeoutError:break
            self.upload_pool.shutdown(wait=True)
            self.state['phase']='finished'
            self.state['finished_at']=time.time()
            self.save()
            # Preserve the full observation window even if this queue finishes early.
            while self.remaining()>0:self.halt.wait(min(1,self.remaining()))
        except BaseException as error:
            self.state.update(phase='failed',error=type(error).__name__)
            raise
        finally:
            self.halt.set();self.upload_pool.shutdown(wait=True)
            subprocess.run(self.engine+['rm','-f',self.name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            monitor.join(timeout=12)
            self.save()
            if self.args.role=='vm':
                try:
                    r=subprocess.run(['dev','ssh','vps-admin','sudo cat '+shlex.quote(str(self.args.remote_work/'vps.json'))],capture_output=True,text=True,timeout=10)
                    if r.returncode==0:atomic(self.root/'status.json',dict(updated_at=time.time(),vm=self.state,vps=json.loads(r.stdout)))
                except (ValueError,subprocess.TimeoutExpired):pass


def stage(args):
    root=args.work.resolve()
    root.mkdir(parents=True,exist_ok=True)
    (root/'inputs').mkdir(exist_ok=True)
    spec=importlib.util.spec_from_file_location('comparison',root/'compare-video-encodings.py')
    media=importlib.util.module_from_spec(spec);spec.loader.exec_module(media)
    manifest=json.loads((root/'manifest.json').read_text())
    for number,item in enumerate(manifest,1):
        target=root/'inputs'/(str(number)+'.source')
        if target.exists() and 'probe' in item and target.stat().st_size==int(item['size']):
            with target.open('rb') as existing:
                if hashlib.file_digest(existing,'md5').hexdigest()==item['md5Checksum']:continue
        atomic(root/'stage.json',dict(phase='staging VM inputs',current=number,total=len(manifest),updated_at=time.time()))
        command=shlex.join(['sudo','python3','-',str(args.config),item['id'],item['size']])
        access=json.loads(subprocess.check_output(['ssh','vps-admin',command],input=media.REMOTE_SOURCE_ACCESS.encode()))
        assert str(access['metadata']['version'])==str(item['version'])
        request=urllib.request.Request('https://www.googleapis.com/drive/v3/files/'+item['id']+'?alt=media&supportsAllDrives=true',headers={'Authorization':'Bearer '+access['token']})
        digest=hashlib.md5()
        with urllib.request.urlopen(request,timeout=60) as response,target.open('wb') as output:
            while True:
                data=response.read(1024*1024)
                if not data:break
                output.write(data);digest.update(data)
        assert target.stat().st_size==int(item['size']) and digest.hexdigest()==item['md5Checksum']
        item['probe']=media.read_probe(target)
        item['duration_seconds']=float(item['probe']['format']['duration'])
        video=media.video_stream(item['probe'])
        assert video['avg_frame_rate'] not in ('0/0','0/1')
        atomic(root/'manifest.json',manifest)
        print('Staged sample',number,'duration',round(item['duration_seconds']),'resolution',video['width'],video['height'],'codec',video['codec_name'],flush=True)
    atomic(root/'stage.json',dict(phase='ready',total=len(manifest),updated_at=time.time()))


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--role',choices=['vm','vps','stage'],required=True)
    p.add_argument('--work',type=Path,required=True)
    p.add_argument('--remote-work',type=Path,default=Path('/folder/.filebeans-stage/encoding-benchmark'))
    p.add_argument('--destination',type=Path,default=Path('/folder/Encoding comparisons/VM-VPS benchmark'))
    p.add_argument('--config',type=Path,default=Path('/opt/services/filebeans/private/config.json'))
    p.add_argument('--start',type=float,default=0)
    p.add_argument('--seconds',type=int,default=1800)
    p.add_argument('--memory',type=int,default=12884901888)
    p.add_argument('--image')
    a=p.parse_args()
    assert a.seconds>0 and a.memory>0
    assert str(a.destination).startswith('/folder/Encoding comparisons/')
    if a.role=='stage':stage(a)
    else:
        assert a.image and a.start>0
        Benchmark(a).run()


if __name__=='__main__':main()
