import importlib.util
import json
from pathlib import Path
import tempfile
import subprocess
import sys
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
            worker.guard.close();worker.prepares.shutdown();worker.uploads.shutdown()

    def test_small_video_validates_publishes_and_preserves_original(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);source=root/'fixture.input';destination=root/'library';destination.mkdir()
            subprocess.run(['ffmpeg','-v','error','-f','lavfi','-i','testsrc2=size=320x180:rate=30','-f','lavfi','-i','sine=frequency=440:sample_rate=22050','-t','2','-c:v','libx264','-preset','ultrafast','-c:a','aac','-ac','1','-b:a','64k', '-f','mp4',str(source)],check=True)
            original_hash=m.checksum(source)
            adapter=root/'engine.py'
            adapter.write_text("import os,sys\nfrom pathlib import Path\nr=Path(__file__).parent\na=[str(r/x[6:]) if x.startswith('/work/') else x for x in sys.argv[3:]]\nos.execvp(a[0],a)\n")
            job={'id':'fixture','number':1,'relative_path':'fixture.mp4','output_relative':'sub/fixture.mp4','source_size':source.stat().st_size,'source_md5':m.checksum(source,'md5'),'duration_seconds':2}
            queue=root/'queue.json';queue.write_text(json.dumps({'role':'vps','jobs':[job]}))
            config=root/'config.json';config.write_text(json.dumps({'role':'vps','queue':str(queue),'work':str(root),'public_status':str(root/'public.json'),'destination':str(destination),'memory_bytes':123,'enabled':False,'profile':{'name':'draft','approved':False,'keyframe_seconds':1,'codec':'hevc','crf':22,'preset':'medium'}}))
            worker=m.Worker(config);worker.engine=[sys.executable,str(adapter)];worker.container='fixture';worker.state['active_profile']=m.profile_hash(worker.config['profile'])
            worker.pending_outputs.acquire();output=worker.encode(job,source);worker.deliver(job,output)
            self.assertEqual(worker.row(job)['phase'],'completed')
            self.assertEqual(worker.row(job)['audio_mode'],'copy')
            self.assertLessEqual(worker.row(job)['keyframe_max_seconds'],1.04)
            self.assertEqual(m.checksum(source),original_hash)
            self.assertEqual(m.checksum(destination/'sub/fixture.mp4'),worker.row(job)['output_sha256'])
            worker.guard.close();worker.prepares.shutdown();worker.uploads.shutdown()


if __name__=='__main__':unittest.main()
