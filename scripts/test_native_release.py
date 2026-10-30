"""Native release payload integrity and executable mode regression tests."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('native_release', Path(__file__).with_name('native-release-go.py'))
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class NativeReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        previous = Path.cwd()
        os.chdir(self.temp.name)
        self.addCleanup(os.chdir, previous)
        env = patch.dict(os.environ, {'GOOS': 'linux', 'GOARCH': 'amd64', 'NATIVE_RELEASE_BUILD': ''})
        env.start()
        self.addCleanup(env.stop)
        git = patch.object(adapter, 'git', return_value='exact-commit')
        git.start()
        self.addCleanup(git.stop)
        self.args = ['-ldflags=-s -w -X github.com/strongo/buildinfo.version=1.2.3', '-o', 'dist/output/datatug', '.']
        self.payload = Path('native-release/request-linux-amd64')
        self.payload.mkdir(parents=True)
        self.binary = self.payload / 'datatug'
        self.binary.write_bytes(b'original native binary')
        self.metadata = {'binary': 'datatug', 'goos': 'linux', 'goarch': 'amd64', 'commit': 'exact-commit',
                         'tag': 'v1.2.3', 'version': '1.2.3', 'sha256': adapter.digest(self.binary)}
        self.save()

    def save(self):
        (self.payload / 'manifest.json').write_text(json.dumps(self.metadata))

    def test_import_preserves_bytes_and_mode_without_compilation(self):
        with patch.object(adapter.subprocess, 'run', side_effect=AssertionError('must not compile in publisher')):
            adapter.main(self.args)
        output = Path('dist/output/datatug')
        self.assertEqual(output.read_bytes(), self.binary.read_bytes())
        if os.name != 'nt':
            self.assertEqual(output.stat().st_mode & 0o777, 0o755)

    def test_rejects_mismatched_identity_and_hash(self):
        for key in ['commit', 'tag', 'version', 'sha256', 'binary']:
            with self.subTest(key=key):
                original = self.metadata[key]
                self.metadata[key] = 'mismatch'
                self.save()
                with self.assertRaises(ValueError):
                    adapter.main(self.args)
                self.assertFalse(Path('dist/output/datatug').exists())
                self.metadata[key] = original

    def test_rejects_changed_payload(self):
        self.binary.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'checksum'):
            adapter.main(self.args)

    def test_rejects_missing_and_ambiguous_targets(self):
        self.metadata['goarch'] = 'arm64'
        self.save()
        with self.assertRaisesRegex(ValueError, 'found 0'):
            adapter.main(self.args)
        self.metadata['goarch'] = 'amd64'
        self.save()
        duplicate = Path('native-release/duplicate')
        duplicate.mkdir()
        (duplicate / 'manifest.json').write_text(json.dumps(self.metadata))
        with self.assertRaisesRegex(ValueError, 'found 2'):
            adapter.main(self.args)

    def test_rejects_tag_pointing_elsewhere(self):
        with patch.object(adapter, 'git', side_effect=['exact-commit', 'other-commit']):
            with self.assertRaisesRegex(ValueError, 'tag does not point'):
                adapter.main(self.args)

    def test_native_compile_rejects_cross_target(self):
        with patch.dict(os.environ, {'NATIVE_RELEASE_BUILD': '1'}):
            with patch.object(adapter.subprocess, 'check_output', return_value='darwin\narm64\n'):
                with self.assertRaisesRegex(ValueError, 'native runner'):
                    adapter.main(self.args)

    @unittest.skipIf(os.name == 'nt', 'Unix executable mode regression')
    def test_packed_native_binary_remains_executable(self):
        def build(command, check):
            self.assertEqual(command[:2], ['go', 'build'])
            self.assertTrue(check)
            output = Path('dist/output/datatug')
            output.parent.mkdir(parents=True)
            output.write_text('#!/bin/sh\nprintf native-smoke\\n\n')
            output.chmod(0o755)
        with patch.dict(os.environ, {'NATIVE_RELEASE_BUILD': '1', 'GORELEASER_CURRENT_TAG': 'v1.2.3'}):
            with patch.object(adapter.subprocess, 'check_output', return_value='linux\namd64\n'):
                with patch.object(adapter.subprocess, 'run', side_effect=build):
                    adapter.main(self.args)
        manifest = json.loads(Path('dist/output/datatug.native.json').read_text())
        self.assertEqual(manifest['commit'], 'exact-commit')
        self.assertEqual(manifest['tag'], 'v1.2.3')
        adapter.main(['--pack'])
        packed = Path('native-payload/datatug')
        self.assertEqual(packed.stat().st_mode & 0o777, 0o755)
        self.assertIn('native-smoke', subprocess.check_output([str(packed)], text=True))


if __name__ == '__main__':
    unittest.main()
