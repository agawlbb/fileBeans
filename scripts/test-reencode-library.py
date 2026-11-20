import importlib.util
import json
from pathlib import Path
import tempfile
import subprocess
import sys
import threading
import time
import unittest

spec=importlib.util.spec_from_file_location('worker',Path(__file__).with_name('reencode-library.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)


class PreparationTests(unittest.TestCase):
    def test_approval_requires_both_gates_and_one_second_keyframes(self):
        profile={'approved':False,'keyframe_seconds':1}
        self.assertFalse(m.approved({'enabled':True,'profile':profile}))
        profile['approved']=True
        self.assertFalse(m.approved({'enabled':False,'profile':profile}))
        self.assertTrue(m.approved({'enabled':True,'profile':profile}))
        profile['keyframe_seconds']=2
        self.assertFalse(m.approved({'enabled':True,'profile':profile}))

    def test_compatible_aac_is_not_reencoded(self):
        self.assertEqual(m.audio_args({'codec_name':'aac','profile':'LC','channels':2,'sample_rate':'44100','bit_rate':'128000'}),['-c:a','copy'])

    def test_comparable_low_bitrate_audio_is_not_increased(self):
        args=m.audio_args({'codec_name':'mp3','channels':1,'sample_rate':'22050','bit_rate':'64000'})
        self.assertEqual(args[args.index('-b:a')+1],'64000')
        self.assertEqual(args[args.index('-ac')+1],'1')
        self.assertEqual(args[args.index('-ar')+1],'22050')

    def test_paths_cannot_escape_or_include_reserved_caches(self):
        for path in ['../x.mp4','/x.mp4','.','album/.FILEBEANS-thumbnails/x.mp4']:
            with self.assertRaises(ValueError):m.relative(path)
        self.assertEqual(m.relative('ordinary/thumbnails/x.mp4'),'ordinary/thumbnails/x.mp4')

    def test_held_status_preserves_existing_completion_and_hides_names(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);queue=root/'queue.json';config=root/'config.json';status=root/'public.json'
            job={'id':'test','number':1,'relative_path':'private title.mp4','output_relative':'private title.mp4','source_size':100,'duration_seconds':10}
            queue.write_text(json.dumps({'role':'vps','jobs':[job]}))
            (root/'state.json').write_text(json.dumps({'active_profile':None,'jobs':{'test':{'phase':'completed','percent':100}}}))
            config.write_text(json.dumps({'role':'vps','queue':str(queue),'work':str(root),'public_status':str(status),'memory_bytes':123,'enabled':False,'profile':{'name':'undecided','approved':False,'keyframe_seconds':1}}))
            worker=m.Worker(config);worker.status();result=json.loads(status.read_text())
            self.assertEqual(result['phase'],'held');self.assertEqual(result['completed'],1)
            self.assertNotIn('private title',status.read_text());self.assertFalse(worker.runtime)
            worker.guard.close();worker.tasks.shutdown()

    def test_small_video_validates_publishes_and_preserves_original(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);source=root/'fixture.input';destination=root/'library';destination.mkdir()
            subprocess.run(['ffmpeg','-v','error','-f','lavfi','-i','testsrc2=size=320x180:rate=30','-f','lavfi','-i','sine=frequency=440:sample_rate=22050','-t','2','-c:v','libx264','-preset','ultrafast','-c:a','aac','-ac','1','-b:a','64k', '-f','mp4',str(source)],check=True)
            original_hash=m.checksum(source)
            adapter=root/'engine.py'
            adapter.write_text("import os,sys\nfrom pathlib import Path\nr=Path(__file__).parent\na=[str(r/x[6:]) if x.startswith('/work/') else x for x in sys.argv[3:]]\nos.execvp(a[0],a)\n")
            job={'id':'fixture','number':1,'relative_path':'fixture.mp4','output_relative':'sub/fixture.mp4','source_size':source.stat().st_size,'source_md5':m.checksum(source,'md5'),'duration_seconds':2}
            queue=root/'queue.json';queue.write_text(json.dumps({'role':'vps','jobs':[job]}))
            config=root/'config.json';config.write_text(json.dumps({'role':'vps','queue':str(queue),'work':str(root),'public_status':str(root/'public.json'),'destination':str(destination),'memory_bytes':2147483648,'enabled':False,'profile':{'name':'draft','approved':False,'keyframe_seconds':1,'codec':'hevc','crf':22,'preset':'medium'}}))
            worker=m.Worker(config);worker.engine=[sys.executable,str(adapter)];worker.container='fixture';worker.state['active_profile']=m.profile_hash(worker.config['profile'])
            output=worker.encode(job,source);worker.deliver(job,output)
            self.assertEqual(worker.row(job)['phase'],'completed')
            self.assertEqual(worker.row(job)['audio_mode'],'copy')
            self.assertLessEqual(worker.row(job)['keyframe_max_seconds'],1.04)
            self.assertEqual(m.checksum(source),original_hash)
            self.assertEqual(m.checksum(destination/'sub/fixture.mp4'),worker.row(job)['output_sha256'])
            worker.guard.close();worker.tasks.shutdown()


    def test_two_and_four_encoder_pipelines_overlap_with_serial_stage_gates(self):
        for encoders in [2,4]:
            with self.subTest(encoders=encoders), tempfile.TemporaryDirectory() as d:
                root=Path(d);jobs=[{'id':str(i),'number':i,'relative_path':str(i)+'.mp4','output_relative':str(i)+'.mp4','source_size':1} for i in range(8)]
                queue=root/'queue.json';queue.write_text(json.dumps({'role':'vps','jobs':jobs}))
                profile={'name':'test','approved':True,'keyframe_seconds':1}
                config=root/'config.json';config.write_text(json.dumps({'role':'vps','queue':str(queue),'work':str(root),'public_status':str(root/'public.json'),'memory_bytes':2147483648,'enabled':True,'profile':profile,'encoders':encoders,'max_active_jobs':encoders+2}))
                worker=m.Worker(config);lock=threading.Lock();counts={k:0 for k in ['prepare','encode','validate','publish']};peaks=counts.copy();barrier=threading.Barrier(encoders)
                def stage(key,delay):
                    with lock:counts[key]+=1;peaks[key]=max(peaks[key],counts[key])
                    if key=='encode':barrier.wait(timeout=3)
                    time.sleep(delay)
                    with lock:counts[key]-=1
                def prepare(job):
                    stage('prepare',0.005);path=root/(job['id']+'.input');path.write_bytes(b'x');return path
                def encode(job,source):
                    with worker.slot(worker.encode_gate):stage('encode',0.01)
                    with worker.slot(worker.validate_gate):stage('validate',0.005)
                    output=root/(job['id']+'.mp4');output.write_bytes(b'x');return output
                def deliver(job,output):stage('publish',0.005);worker.row(job)['phase']='completed'
                worker.prepare=prepare;worker.encode=encode;worker.deliver=deliver
                futures=[worker.tasks.submit(worker.process_job,j,m.profile_hash(profile)) for j in jobs]
                for future in futures:future.result(timeout=10)
                worker.tasks.shutdown();worker.guard.close()
                self.assertTrue(all(worker.row(j)['phase']=='completed' for j in jobs))
                self.assertEqual(peaks,{'prepare':1,'encode':encoders,'validate':1,'publish':1})

    def test_requested_stop_keeps_cached_job_runnable(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);job={'id':'cached','number':1,'relative_path':'cached.mp4','output_relative':'cached.mp4','source_size':1}
            queue=root/'queue.json';queue.write_text(json.dumps({'role':'vps','jobs':[job]}));config=root/'config.json';profile={'name':'test','approved':True,'keyframe_seconds':1}
            config.write_text(json.dumps({'role':'vps','queue':str(queue),'work':str(root),'public_status':str(root/'public.json'),'memory_bytes':2147483648,'enabled':True,'profile':profile}))
            worker=m.Worker(config);(root/'cached.input').write_bytes(b'x');worker.stop.set();worker.process_job(job,m.profile_hash(profile))
            self.assertEqual(worker.row(job)['phase'],'ready');self.assertNotIn('error',worker.row(job));worker.guard.close();worker.tasks.shutdown()

if __name__=='__main__':unittest.main()
