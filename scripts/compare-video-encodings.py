#!/usr/bin/env python3
"""Encode five comparable video samples on the development VM; report progress.

Credentials are never written to disk or command arguments. The public status contains only
option settings, timing and size data. Outputs are copied to a separate user
folder, never the normalization library or its job queue.
"""
import argparse
import csv
import concurrent.futures
import hashlib
import http.server
import secrets
import urllib.request
import urllib.error
import json
import math
import os
from pathlib import Path
import re
import shlex
import subprocess
import threading
import time

OPTIONS = [
    (1, "Current", "hevc", 18, "medium", None),
    (2, "Frequent keyframes", "hevc", 18, "medium", 1.0),
    (3, "Lower-quality HEVC", "hevc", 22, "medium", 1.0),
    (4, "H.264", "h264", 22, "fast", 1.0),
    (5, "H.264 half-second", "h264", 22, "fast", 0.5),
]

REMOTE_DOWNLOAD = r"""
import json,sys,subprocess,urllib.request
config_path,drive_id,expected_size=sys.argv[1:]
config=json.load(open(config_path))
container=json.loads(subprocess.check_output(['docker','inspect','filebeans-companion']))[0]
ip=next(v['IPAddress'] for v in container['NetworkSettings']['Networks'].values() if v['IPAddress'])
base='http://'+ip+':'+config['listen'].rsplit(':',1)[-1]+'/v1/drive/files/'+drive_id
headers={'Authorization':'Bearer '+config['token']}
def metadata():
 with urllib.request.urlopen(urllib.request.Request(base,headers=headers),timeout=60) as response:return json.load(response)
info=metadata();size=int(info['size']);assert size==int(expected_size)
manifest={k:info.get(k) for k in ['size','version','md5Checksum','sha256Checksum']}
sys.stdout.buffer.write((json.dumps(manifest)+'\n').encode());sys.stdout.buffer.flush()
for start in range(0,size,64*1024*1024):
 end=min(start+64*1024*1024,size)-1
 request=urllib.request.Request(base+'/content',headers=dict(headers,Range='bytes='+str(start)+'-'+str(end)))
 with urllib.request.urlopen(request,timeout=90) as response:
  assert response.status==206
  assert response.headers.get('Content-Range','').startswith('bytes '+str(start)+'-')
  remaining=end-start+1
  while remaining:
   data=response.read(min(1024*1024,remaining))
   if not data:raise RuntimeError('incomplete source')
   sys.stdout.buffer.write(data);remaining-=len(data)
  sys.stdout.buffer.flush()
assert metadata()['version']==info['version']
"""


REMOTE_SOURCE_ACCESS = r"""
import json,sys,subprocess,urllib.request
from pathlib import Path
config_path,drive_id,expected_size=sys.argv[1:]
config=json.load(open(config_path))
container=json.loads(subprocess.check_output(['docker','inspect','filebeans-companion']))[0]
ip=next(v['IPAddress'] for v in container['NetworkSettings']['Networks'].values() if v['IPAddress'])
url='http://'+ip+':'+config['listen'].rsplit(':',1)[-1]+'/v1/drive/files/'+drive_id
with urllib.request.urlopen(urllib.request.Request(url,headers={'Authorization':'Bearer '+config['token']}),timeout=60) as response:metadata=json.load(response)
assert int(metadata['size'])==int(expected_size)
state=Path(config['state'])
for mount in container['Mounts']:
 destination=Path(mount['Destination'])
 if state==destination or destination in state.parents:
  state=Path(mount['Source'])/state.relative_to(destination);break
grant=json.load(open(state/'google.json'))
# The metadata request above refreshes the existing grant if needed. Only the
# short-lived access token crosses SSH, directly into the VM process memory.
print(json.dumps({'token':grant['access_token'],'metadata':{k:metadata.get(k) for k in ['size','version','md5Checksum','sha256Checksum']}}))
"""

class SourceProxy(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, comparison, token):
        self.comparison = comparison
        self.token = token
        self.secret_path = '/' + secrets.token_urlsafe(24)
        super().__init__(('127.0.0.1', 0), SourceHandler)


class SourceHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_HEAD(self):
        if self.path != self.server.secret_path:
            self.send_error(404); return
        self.send_response(200)
        self.send_header('Content-Length', str(self.server.comparison.args.expected_size))
        self.send_header('Content-Type', 'video/mp4')
        self.send_header('Accept-Ranges', 'bytes')
        self.end_headers()

    def do_GET(self):
        if self.path != self.server.secret_path:
            self.send_error(404); return
        owner = self.server.comparison
        headers = {'Authorization':'Bearer '+self.server.token}
        if self.headers.get('Range'):
            headers['Range'] = self.headers['Range']
        url = 'https://www.googleapis.com/drive/v3/files/'+owner.args.drive_id+'?alt=media&supportsAllDrives=true'
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=90) as response:
                self.send_response(response.status)
                for name in ['Content-Length','Content-Range','Content-Type','Accept-Ranges']:
                    if response.headers.get(name): self.send_header(name,response.headers[name])
                self.send_header('Connection','close')
                self.end_headers()
                while True:
                    data = response.read(128*1024)
                    if not data: break
                    self.wfile.write(data)
                    with owner.lock:
                        owner.state['source_download_bytes'] += len(data)
        except (BrokenPipeError,ConnectionResetError):
            pass # Normal when FFmpeg seeks to a different range.
        except Exception:
            self.close_connection = True


def read_probe(path):
    return json.loads(subprocess.check_output([
        "ffprobe", "-v", "error", "-show_format", "-show_streams", "-of", "json", str(path)
    ]))


def video_stream(info):
    return next(s for s in info["streams"] if s["codec_type"] == "video" and not s.get("disposition", {}).get("attached_pic"))


def keyframes(path, interval=None):
    args = ["ffprobe", "-v", "error", "-select_streams", "v:0", "-skip_frame", "nokey"]
    if interval:
        args += ["-read_intervals", interval]
    args += ["-show_frames", "-show_entries", "frame=best_effort_timestamp_time", "-of", "json", str(path)]
    info = json.loads(subprocess.check_output(args))
    return [float(f["best_effort_timestamp_time"]) for f in info["frames"] if "best_effort_timestamp_time" in f]


def filters(hdr):
    result = ["bwdif=mode=send_frame:parity=auto:deint=interlaced"]
    if hdr:
        result += ["zscale=t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=hable:desat=0,zscale=p=bt709:t=bt709:m=bt709:r=tv"]
    result += ["scale=w='max(2,trunc(iw*sar/2)*2)':h='max(2,trunc(ih/2)*2)',setsar=1",
               "scale=w='if(gte(iw,ih),min(iw,2560),min(iw,1080))':h='if(gte(iw,ih),min(ih,1080),min(ih,2560))':force_original_aspect_ratio=decrease:force_divisible_by=2"]
    return ",".join(result)


def encode_args(source, output, info, option):
    number, label, codec, crf, preset, spacing = option
    stream = video_stream(info)
    rate = stream.get("avg_frame_rate", "30/1").split("/")
    fps = float(rate[0]) / float(rate[1]) if len(rate) == 2 and float(rate[1]) else 30
    hdr = stream.get("color_transfer") in ("smpte2084", "arib-std-b67")
    args = ["ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-filter_threads", "2", "-i", str(source),
            "-map", "0:v:0", "-map", "0:a:0?", "-vf", filters(hdr),
            "-c:a", "aac", "-profile:a", "aac_low", "-b:a", "192k", "-ac", "2", "-ar", "48000",
            "-c:v", "libx265" if codec == "hevc" else "libx264", "-preset", preset, "-crf", str(crf),
            "-pix_fmt", "yuv420p10le" if codec == "hevc" else "yuv420p", "-threads", "4"]
    if codec == "hevc":
        params = "pools=4:frame-threads=2:log-level=error"
        if spacing:
            params += ":open-gop=0:keyint=" + str(math.ceil(fps * spacing)) + ":min-keyint=1"
        args += ["-x265-params", params, "-tag:v", "hvc1"]
        if spacing:
            args += ["-forced-idr", "1"]
    else:
        args += ["-profile:v", "high", "-x264-params", "open-gop=0", "-tag:v", "avc1"]
    if spacing:
        args += ["-g", str(math.ceil(fps * spacing)), "-keyint_min", "1",
                 "-force_key_frames", "expr:gte(t,n_forced*" + str(spacing) + ")"]
    if hdr:
        args += ["-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"]
    return args + ["-movflags", "+faststart", "-progress", "pipe:1", "-nostats", str(output)]


class Comparison:
    def __init__(self, args):
        self.args = args
        self.root = args.output.resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.root.chmod(0o755)
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.state = {"phase": "preparing", "updated_at": time.time(), "duration_seconds": args.duration,
                      "source_download_bytes": 0, "source_download_total": args.expected_size,
                      "options": [{"number": n, "label": label, "codec": codec, "crf": crf, "preset": preset,
                                   "keyframe_target": spacing, "state": "queued", "percent": 0,
                                   "elapsed_seconds": 0, "eta_seconds": None, "output_bytes": 0}
                                  for n, label, codec, crf, preset, spacing in OPTIONS]}
        if (self.root / "progress.json").exists():
            previous = json.loads((self.root / "progress.json").read_text())
            if previous.get("phase") not in ("failed", "upload failed"):
                raise RuntimeError("comparison already exists")
            for key in ['source_download_bytes','source_excerpt_bytes','duration_seconds']:
                if key in previous: self.state[key] = previous[key]
            for row in previous.get("options", []):
                output = self.root / ('Option '+str(row['number'])+'.mp4')
                if row.get('state') == 'completed' and output.exists():
                    self.state['options'][row['number']-1] = row
                    row['delivery'] = 'pending'
                elif output.exists(): output.unlink()
            for name in ['source.partial', 'source.mp4']:
                path = self.root / name
                if path.exists(): path.unlink()
        self.upload_pool = concurrent.futures.ThreadPoolExecutor(max_workers=1)
        self.pending_uploads = []
        self.active = None
        self.started = None
        self.active_output = None
        self.child = None

    def write(self):
        with self.lock:
            self.state["updated_at"] = time.time()
            if self.active is not None and self.started is not None:
                self.state["options"][self.active]["elapsed_seconds"] = time.monotonic() - self.started
                if self.active_output and self.active_output.exists():
                    self.state["options"][self.active]["output_bytes"] = self.active_output.stat().st_size
            temp = self.root / "progress.tmp"
            temp.write_text(json.dumps(self.state))
            temp.chmod(0o644)
            os.replace(temp, self.root / "progress.json")

    def heartbeat(self):
        while not self.stop.wait(2):
            self.write()

    def download(self):
        self.state["phase"] = "downloading source"
        self.write()
        output = self.root / "source.partial"
        remote = shlex.join(["sudo", "python3", "-", self.args.remote_config, self.args.drive_id, str(self.args.expected_size)])
        with (self.root / "download.log").open("wb") as error:
            proc = subprocess.Popen(["ssh", self.args.host, remote], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=error)
            proc.stdin.write(REMOTE_DOWNLOAD.encode())
            proc.stdin.close()
            metadata = json.loads(proc.stdout.readline())
            sha, md5 = hashlib.sha256(), hashlib.md5()
            downloaded = 0
            with output.open("xb") as file:
                while True:
                    data = proc.stdout.read(1024 * 1024)
                    if not data:
                        break
                    file.write(data)
                    sha.update(data)
                    md5.update(data)
                    downloaded += len(data)
                    self.state["source_download_bytes"] = downloaded
            if proc.wait() or downloaded != self.args.expected_size:
                raise RuntimeError("source download failed")
            if metadata.get("sha256Checksum") and sha.hexdigest().lower() != metadata["sha256Checksum"].lower():
                raise RuntimeError("source checksum mismatch")
            if metadata.get("md5Checksum") and md5.hexdigest().lower() != metadata["md5Checksum"].lower():
                raise RuntimeError("source checksum mismatch")
            metadata.update(download_sha256=sha.hexdigest(), download_md5=md5.hexdigest())
            (self.root / "source-metadata.json").write_text(json.dumps(metadata, indent=2))
            original = self.root / "source.mp4"
            output.rename(original)
            return original

    def prepare(self, original):
        self.state["phase"] = "preparing excerpt"
        self.write()
        info = read_probe(original)
        maximum = float(info["format"]["duration"]) - self.args.duration
        assert maximum > 0
        desired = min(self.args.start, maximum)
        times = keyframes(original, str(max(0, desired - 15)) + "%+40")
        earlier = [t for t in times if t <= desired + 0.001]
        if not earlier:
            times = keyframes(original)
            earlier = [t for t in times if t <= desired + 0.001]
        start = max(earlier) if earlier else 0
        excerpt = self.root / "Original sample.mp4"
        subprocess.run(["ffmpeg", "-v", "error", "-nostdin", "-ss", str(start), "-i", str(original), "-t", str(self.args.duration),
                        "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-avoid_negative_ts", "make_zero", "-movflags", "+faststart", str(excerpt)], check=True)
        info = read_probe(excerpt)
        duration = float(info["format"]["duration"])
        assert abs(duration - self.args.duration) < 2
        self.state.update(duration_seconds=duration, source_excerpt_bytes=excerpt.stat().st_size)
        (self.root / "sample.json").write_text(json.dumps({"start_seconds": start, "duration_seconds": duration, "original": info}, indent=2))
        original.unlink()  # Retain only the excerpt; the Drive original remains unchanged.
        self.write()
        return excerpt, info

    def source_access(self):
        remote = shlex.join(["sudo", "python3", "-", self.args.remote_config, self.args.drive_id, str(self.args.expected_size)])
        reply = subprocess.check_output(["ssh", self.args.host, remote], input=REMOTE_SOURCE_ACCESS.encode())
        return json.loads(reply)

    def prepare_remote(self):
        self.state['phase'] = 'preparing excerpt'
        self.state['preparation_percent'] = 0
        self.write()
        access = self.source_access()
        original_version = access['metadata']['version']
        proxy = SourceProxy(self,access['token'])
        del access['token']
        thread = threading.Thread(target=proxy.serve_forever,daemon=True)
        thread.start()
        excerpt = self.root / 'Original sample.mp4'
        url = 'http://127.0.0.1:'+str(proxy.server_port)+proxy.secret_path
        try:
            # FFmpeg seeks by byte range; only metadata and the selected interval
            # are fetched, not the whole original. Source remains in its codec.
            with (self.root/'preparation.log').open('wb') as error:
                args=['ffmpeg','-v','error','-nostdin','-reconnect','1','-reconnect_streamed','1','-reconnect_delay_max','3',
                      '-ss',str(self.args.start),'-i',url,'-t',str(self.args.duration),'-map','0:v:0','-map','0:a:0?',
                      '-c','copy','-avoid_negative_ts','make_zero','-movflags','+faststart','-progress','pipe:1','-nostats',str(excerpt)]
                self.child=subprocess.Popen(args,stdout=subprocess.PIPE,stderr=error,text=True)
                for line in self.child.stdout:
                    key,_,value=line.strip().partition('=')
                    if key=='out_time_us':
                        try:self.state['preparation_percent']=min(99.9,max(0,int(value)/1_000_000/self.args.duration*100))
                        except ValueError:pass
                if self.child.wait(): raise RuntimeError('excerpt preparation failed')
                self.child=None
        finally:
            proxy.shutdown();proxy.server_close();thread.join()
            proxy.token=''
        assert self.source_access()['metadata']['version']==original_version,'Source changed during preparation'
        info=read_probe(excerpt)
        duration=float(info['format']['duration'])
        assert abs(duration-self.args.duration)<12
        self.state.update(duration_seconds=duration,source_excerpt_bytes=excerpt.stat().st_size,preparation_percent=100)
        (self.root/'sample.json').write_text(json.dumps({'requested_start_seconds':self.args.start,'duration_seconds':duration,'original':info},indent=2))
        self.write()
        return excerpt,info

    def publish(self, paths):
        if not self.args.publish_directory:
            return
        remote_directory = self.args.publish_directory
        # Refuse protected cache components or the watched library.
        assert remote_directory.startswith("/folder/Encoding comparisons/") and ".." not in Path(remote_directory).parts
        command = shlex.join(["sudo", "install", "-d", "-o", "1000", "-g", "1000", "-m", "0770", str(Path(remote_directory).parent), remote_directory])
        subprocess.run(["ssh", self.args.host, command], check=True)
        destination = self.args.host + ":" + remote_directory.rstrip("/") + "/"
        subprocess.run(["rsync", "-rt", "--partial-dir=.filebeans-stage", "--protect-args", "--chown=1000:1000", "--chmod=F660,D770", "--rsync-path=sudo rsync", *map(str, paths), destination], check=True)
        names = [p.name for p in paths]
        verify = "from pathlib import Path;import json;d=Path(" + repr(remote_directory) + ");print(json.dumps({n:(d/n).stat().st_size for n in " + repr(names) + "}))"
        sizes = json.loads(subprocess.check_output(["ssh", self.args.host, shlex.join(["sudo", "python3", "-c", verify])]))
        assert all(sizes[path.name] == path.stat().st_size for path in paths)

    def queue_upload(self, paths, index=None):
        if index is not None:
            self.state['options'][index]['delivery'] = 'queued'
        def transfer():
            started=time.monotonic()
            if index is not None: self.state['options'][index]['delivery']='uploading'
            try:
                for attempt in range(3):
                    try:
                        self.publish(paths)
                        break
                    except subprocess.CalledProcessError:
                        if attempt==2: raise
                        time.sleep(2*(attempt+1))
                if index is not None:
                    self.state['options'][index].update(delivery='available',upload_seconds=time.monotonic()-started)
            except Exception:
                if index is not None: self.state['options'][index]['delivery']='failed'
                raise
        self.pending_uploads.append(self.upload_pool.submit(transfer))

    def encode(self, excerpt, info, option):
        index = option[0] - 1
        self.active = index
        self.started = time.monotonic()
        output = self.root / ("Option " + str(option[0]) + ".mp4")
        self.active_output = output
        row = self.state["options"][index]
        row["state"] = "running"
        self.state["phase"] = "encoding"
        self.write()
        args = encode_args(excerpt, output, info, option)
        (self.root / ("option-" + str(option[0]) + "-command.json")).write_text(json.dumps(args))
        with (self.root / ("option-" + str(option[0]) + ".log")).open("wb") as error:
            self.child = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=error, text=True)
            for line in self.child.stdout:
                key, _, value = line.strip().partition("=")
                if key == "out_time_us":
                    try:
                        seconds = max(0, int(value) / 1_000_000)
                    except ValueError:
                        continue
                    row["percent"] = min(99.9, 100 * seconds / self.state["duration_seconds"])
                    if seconds > 0:
                        row["eta_seconds"] = max(0, (self.state["duration_seconds"] - seconds) * (time.monotonic() - self.started) / seconds)
            if self.child.wait():
                raise RuntimeError("encode failed")
            self.child = None
        row['encode_seconds'] = time.monotonic()-self.started
        row['elapsed_seconds'] = row['encode_seconds']
        self.started = None
        validation_started = time.monotonic()
        row["state"] = "validating"
        row["eta_seconds"] = None
        self.state["phase"] = "validating"
        self.write()
        with (self.root / ("option-" + str(option[0]) + "-validation.log")).open("wb") as error:
            subprocess.run(["ffmpeg", "-v", "error", "-nostdin", "-xerror", "-threads", "2", "-i", str(output), "-map", "0:v:0", "-map", "0:a:0?", "-f", "null", "-"], stderr=error, check=True)
        result = read_probe(output)
        stream = video_stream(result)
        assert stream["codec_name"] == option[2]
        assert stream["pix_fmt"] == ("yuv420p10le" if option[2] == "hevc" else "yuv420p")
        assert abs(float(result["format"]["duration"]) - self.state["duration_seconds"]) < 0.5
        audio = [s for s in result["streams"] if s["codec_type"] == "audio"]
        assert bool(audio) == any(s["codec_type"] == "audio" for s in info["streams"])
        if audio:
            assert audio[0]["codec_name"] == "aac" and audio[0]["profile"] == "LC" and audio[0]["channels"] == 2 and audio[0]["sample_rate"] == "48000"
        times = keyframes(output)
        assert times
        gaps = [b-a for a,b in zip(times,times[1:])] + [max(0,float(result["format"]["duration"])-times[-1])]
        maximum = max(gaps)
        if option[5]:
            num, den = stream["avg_frame_rate"].split("/")
            assert maximum <= option[5] + float(den)/float(num) + 0.02
        row.update(output_bytes=output.stat().st_size, compression_ratio=self.state["source_excerpt_bytes"] / output.stat().st_size,
                   reduction_percent=100 * (1-output.stat().st_size/self.state["source_excerpt_bytes"]), keyframe_max_seconds=maximum,
                   validation_seconds=time.monotonic()-validation_started)
        (self.root / ("option-" + str(option[0]) + "-probe.json")).write_text(json.dumps(result,indent=2))
        row.update(state="completed", percent=100, eta_seconds=0)
        self.queue_upload([output],index)
        self.active = self.started = self.active_output = None
        self.write()

    def report(self):
        fields = ["number", "label", "codec", "crf", "preset", "keyframe_target", "keyframe_max_seconds", "output_bytes", "compression_ratio", "reduction_percent", "encode_seconds", "validation_seconds", "upload_seconds"]
        with (self.root / "Comparison.csv").open("w") as file:
            writer = csv.DictWriter(file,fieldnames=fields,extrasaction="ignore")
            writer.writeheader()
            writer.writerows(self.state["options"])
        lines = ["Encoding comparison", "", "Source excerpt: %.3f seconds; %d bytes" % (self.state["duration_seconds"],self.state["source_excerpt_bytes"]), ""]
        for row in self.state["options"]:
            lines += ["Option %d: %s; %s CRF %d; %.2f MiB; %.2f:1; maximum keyframe gap %.3f s; VM encoding %.2f seconds" % (row["number"],row["label"],row["codec"],row["crf"],row["output_bytes"]/1024**2,row["compression_ratio"],row["keyframe_max_seconds"],row["encode_seconds"])]
        (self.root / "Comparison.txt").write_text("\n".join(lines)+"\n")
        self.queue_upload([self.root / "Comparison.csv", self.root / "Comparison.txt"])

    def run(self):
        self.write()
        thread = threading.Thread(target=self.heartbeat,daemon=True)
        thread.start()
        try:
            excerpt = self.root / 'Original sample.mp4'
            if excerpt.exists():
                info=read_probe(excerpt)
                manifest=json.loads((self.root/'sample.json').read_text())
                assert manifest.get('requested_start_seconds') == self.args.start
                self.state.update(duration_seconds=float(info['format']['duration']),source_excerpt_bytes=excerpt.stat().st_size)
            else:
                excerpt, info = self.prepare_remote()
            for option in OPTIONS:
                row=self.state['options'][option[0]-1]
                if row.get('state')=='completed':
                    self.queue_upload([self.root/('Option '+str(option[0])+'.mp4')],option[0]-1)
                else:
                    self.encode(excerpt,info,option)
            self.report()
            self.queue_upload([excerpt])
            self.state['phase']='transferring samples'
            self.write()
            for future in self.pending_uploads: future.result()
            self.state["phase"] = "complete"
        except BaseException as error:
            if self.child and self.child.poll() is None:
                self.child.terminate()
                self.child.wait()
            self.state["phase"] = "failed"
            self.state["error"] = type(error).__name__
            if self.active is not None:
                self.state["options"][self.active]["state"] = "failed"
            raise
        finally:
            self.upload_pool.shutdown(wait=True)
            self.stop.set()
            thread.join()
            self.write()


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output",type=Path,required=True)
    parser.add_argument("--drive-id",required=True)
    parser.add_argument("--expected-size",type=int,required=True)
    parser.add_argument("--host",default="vps-admin")
    parser.add_argument("--remote-config",required=True)
    parser.add_argument("--start",type=float,default=180)
    parser.add_argument("--duration",type=float,default=180)
    parser.add_argument("--publish-directory")
    args=parser.parse_args()
    assert re.fullmatch(r"[A-Za-z0-9_-]+",args.drive_id)
    assert 0 < args.duration <= 181 and args.expected_size > 0
    Comparison(args).run()


if __name__ == "__main__":
    main()
