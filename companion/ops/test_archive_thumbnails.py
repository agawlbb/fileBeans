import contextlib
import importlib.util
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('archive_thumbnails',Path(__file__).with_name('archive_thumbnails.py'))
m=importlib.util.module_from_spec(spec);sys.modules[spec.name]=m;spec.loader.exec_module(m)
JPEG=b'\xff\xd8\xfffixture-thumbnail\xff\xd9'

class ArchiveThumbnailsTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.library=Path(self.tmp.name)/'library';self.archive=Path(self.tmp.name)/'archive'
        self.library.mkdir();self.archive.mkdir()
    def file(self,root,path,data=b'original'):
        p=root/path;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(data);return p
    def thumb(self,root,path,data=JPEG):
        with contextlib.closing(m.Tree(root)) as tree:
            inventory=tree.inventory();p=root/Path(path).parent/m.CACHE/m.cache_name(path,inventory[path]);p.parent.mkdir(exist_ok=True);p.write_bytes(data);return p
    def test_different_encodings_rekey_and_preserve_media(self):
        left=self.file(self.library,'nested/clip.mp4',b'encoded-small')
        right=self.file(self.archive,'nested/clip.mov',b'original-encoding-is-larger')
        source_thumb=self.thumb(self.library,'nested/clip.mp4')
        before=(right.stat().st_size,right.stat().st_mtime_ns)
        opened=[];original=os.open
        def track(path,*a,**kw):
            opened.append(os.fspath(path));return original(path,*a,**kw)
        with mock.patch.object(m.os,'open',side_effect=track):result=m.prepare(self.library,self.archive,copy=True)
        self.assertTrue(result['matched']);self.assertEqual(result['copied'],1)
        self.assertEqual(before,(right.stat().st_size,right.stat().st_mtime_ns))
        self.assertFalse(any(p in ('clip.mp4','clip.mov') for p in opened))
        files=list((self.archive/'nested'/m.CACHE).glob('*.jpg'));self.assertEqual(len(files),1)
        self.assertNotEqual(files[0].name,source_thumb.name);self.assertEqual(files[0].read_bytes(),JPEG)
        self.assertEqual(m.prepare(self.library,self.archive,copy=True)['unchanged'],1)
    def test_mismatch_performs_no_cache_writes(self):
        self.file(self.library,'same.jpg');self.file(self.archive,'same.png');self.file(self.archive,'extra.png');self.thumb(self.library,'same.jpg')
        result=m.prepare(self.library,self.archive,copy=True)
        self.assertFalse(result['matched']);self.assertEqual(result['missingLibrary'],['extra.png'])
        self.assertFalse((self.archive/m.CACHE).exists())
    def test_ambiguous_stems_stop_copy(self):
        self.file(self.library,'same.jpg');self.file(self.library,'same.png');self.file(self.archive,'same.mov')
        result=m.prepare(self.library,self.archive,copy=True)
        self.assertFalse(result['matched']);self.assertEqual(len(result['ambiguous']),1)
    def test_stale_missing_and_invalid_thumbnails_are_not_generated(self):
        p=self.file(self.library,'same.jpg');self.file(self.archive,'same.png');self.thumb(self.library,'same.jpg')
        os.utime(p,ns=(1,1))
        result=m.prepare(self.library,self.archive,copy=True)
        self.assertEqual(result['missingThumbnails'],['same.jpg']);self.assertEqual(result['copied'],0)
        self.thumb(self.library,'same.jpg',b'not-jpeg')
        self.assertEqual(m.prepare(self.library,self.archive,copy=True)['invalidThumbnails'],['same.jpg'])
    def test_internal_caches_are_excluded_but_names_case_sensitive(self):
        self.file(self.library,'A.jpg');self.file(self.archive,'a.png');self.file(self.library,m.CACHE+'/old.jpg')
        result=m.prepare(self.library,self.archive)
        self.assertEqual(result['libraryFiles'],1);self.assertFalse(result['matched'])
        self.assertEqual(m.stem_key('sub/.env'),('sub','.env'))
        self.assertEqual(m.stem_key('sub/photo.raw.png'),('sub','photo.raw'))
    def test_cache_symlink_and_hardlink_cannot_read_original(self):
        media=self.file(self.library,'same.jpg');self.file(self.archive,'same.png')
        cache=self.thumb(self.library,'same.jpg');cache.unlink();cache.symlink_to(media)
        with self.assertRaises(OSError):m.prepare(self.library,self.archive,copy=True)
        cache.unlink();os.link(media,cache)
        with self.assertRaisesRegex(ValueError,'aliases an original'):m.prepare(self.library,self.archive,copy=True)
    def test_failed_publication_cleans_temporary_thumbnail(self):
        self.file(self.library,'same.jpg');self.file(self.archive,'same.png');self.thumb(self.library,'same.jpg')
        with mock.patch.object(m.os,'replace',side_effect=OSError('simulated failure')):
            with self.assertRaises(OSError):m.prepare(self.library,self.archive,copy=True)
        self.assertEqual(list((self.archive/m.CACHE).iterdir()),[])
    def test_parallel_copy_is_idempotent_and_budget_bounded(self):
        for i in range(12):
            self.file(self.library,f'nested/{i}.mp4');self.file(self.archive,f'nested/{i}.mov');self.thumb(self.library,f'nested/{i}.mp4')
        result=m.prepare(self.library,self.archive,copy=True,workers=4)
        self.assertEqual(result['copied'],12);self.assertEqual(result['thumbnailBytes'],12*len(JPEG))
        self.assertEqual(m.prepare(self.library,self.archive,copy=True,workers=4)['unchanged'],12)
        for p in (self.archive/'nested'/m.CACHE).iterdir():p.unlink()
        with mock.patch.object(m,'MAX_COPY',len(JPEG)*3):
            with self.assertRaisesRegex(ValueError,'128 MiB'):m.prepare(self.library,self.archive,copy=True,workers=4)
        caches=list((self.archive/'nested'/m.CACHE).iterdir())
        self.assertLessEqual(len(caches),3);self.assertTrue(all(p.suffix=='.jpg' for p in caches))

if __name__=='__main__':unittest.main()
