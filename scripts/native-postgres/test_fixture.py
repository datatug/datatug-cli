"""Resource ownership checks using a fake Docker executable; no services."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from cleanup_private import cleanup

ROOT = Path(__file__).resolve().parent
IMAGE = 'sha256:' + 'b' * 64
CONTAINER = 'a' * 64
OWNER = 'dt-native-123-1-17'
RUNTIME = 'dt-native-pooler-' + 'c' * 24
REVISION = 'f03e9bf263d7ccd5d1d152a61811bf3258dd6b87'

FAKE = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
with open(os.environ['TRACE'], 'a') as file:
    file.write(json.dumps(args) + '\n')
mode = os.environ.get('MODE', '')
state = Path(os.environ['STATE'])
if args[:2] == ['buildx', 'ls']:
    if mode == 'existing-builder':
        print(os.environ['DATATUG_NATIVE_POOLER_BUILD'])
    sys.exit(0)
if args[:2] == ['container', 'ls']:
    if mode == 'inventory-error':
        sys.exit(1)
    if state.exists() or mode == 'empty':
        sys.exit(0)
    print('a' * 64)
elif args[0] == 'inspect':
    if 'pooler-runtime' in args[2]:
        print('another-owner' if mode == 'wrong-label' else os.environ['DATATUG_NATIVE_POOLER_RUNTIME'])
    elif '.Name' in args[2]:
        print('/' + os.environ['DATATUG_NATIVE_POOLER_RUNTIME'])
    elif '.Image' in args[2]:
        print('sha256:' + ('d' if mode == 'wrong-image' else 'b') * 64)
    else:
        sys.exit(2)
elif args[0] == 'rm':
    assert args == ['rm', '--force', 'a' * 64]
    state.write_text('removed')
elif args[:2] == ['image', 'inspect']:
    print(os.environ['DATATUG_NATIVE_POOLER_BUILD'] if 'pooler-build' in args[3] else os.environ['REVISION'])
elif args[:2] == ['image', 'rm']:
    assert args == ['image', 'rm', 'sha256:' + 'b' * 64]
elif args[:2] == ['image', 'ls']:
    pass
else:
    sys.exit(2)
'''


class ResourceContracts(unittest.TestCase):
    def invoke(self, script, mode='', ci=True):
        with tempfile.TemporaryDirectory(prefix='pooler-contract-') as tmp:
            directory = Path(tmp)
            docker = directory / 'docker'
            docker.write_text(FAKE)
            docker.chmod(0o700)
            env = {**os.environ, 'PATH': str(directory) + os.pathsep + os.environ['PATH'],
                   'GITHUB_ACTIONS': 'true' if ci else 'false', 'RUNNER_OS': 'Linux',
                   'DATATUG_TEST_POSTGRES_MAJOR': '17', 'GITHUB_RUN_ID': '123',
                   'GITHUB_RUN_ATTEMPT': '1', 'DATATUG_NATIVE_POOLER_IMAGE': IMAGE,
                   'DATATUG_NATIVE_POOLER_BUILD': OWNER, 'DATATUG_NATIVE_POOLER_RUNTIME': RUNTIME,
                   'DATATUG_NATIVE_POOLER_DIRECTORY': '',
                   'MODE': mode, 'TRACE': str(directory / 'trace'),
                   'STATE': str(directory / 'state'), 'REVISION': REVISION}
            result = subprocess.run(['bash', str(ROOT / script)], env=env,
                                    capture_output=True, text=True, timeout=10)
            trace = directory / 'trace'
            calls = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
            return result.returncode, calls

    def test_non_ci_build_and_cleanup_never_call_docker(self):
        for script in ['build-fixture.sh', 'cleanup-fixture.sh']:
            code, calls = self.invoke(script, ci=False)
            self.assertNotEqual(code, 0)
            self.assertEqual(calls, [])

    def test_existing_builder_is_preserved(self):
        code, calls = self.invoke('build-fixture.sh', 'existing-builder')
        self.assertNotEqual(code, 0)
        self.assertEqual(calls, [['buildx', 'ls', '--format', '{{.Name}}']])

    def test_fallback_removes_only_exact_runtime_then_image(self):
        code, calls = self.invoke('cleanup-fixture.sh')
        self.assertEqual(code, 0)
        removals = [call for call in calls if call[0] == 'rm' or call[:2] == ['image', 'rm']]
        self.assertEqual(removals, [['rm', '--force', CONTAINER], ['image', 'rm', IMAGE]])
        self.assertTrue(any('id=' + CONTAINER in call for call in calls))

    def test_unowned_or_unknown_runtime_is_never_removed(self):
        for mode in ['wrong-label', 'wrong-image', 'inventory-error']:
            code, calls = self.invoke('cleanup-fixture.sh', mode)
            self.assertNotEqual(code, 0)
            self.assertFalse(any(call[0] == 'rm' or call[:2] == ['image', 'rm'] for call in calls))

    def test_private_cleanup_rejects_wrong_owner_and_unexpected_files(self):
        with tempfile.TemporaryDirectory(prefix='pooler-contract-') as tmp:
            parent = Path(tmp).resolve()
            path = parent / 'dt-native-pooler-owned'
            path.mkdir(mode=0o700)
            marker = path / '.owner.json'
            marker.write_text(json.dumps({'runtime': RUNTIME, 'image': IMAGE}))
            (path / 'users.txt').write_text('private fixture password')
            with self.assertRaises(ValueError):
                cleanup(str(path), str(parent), 'different-owner', IMAGE)
            self.assertTrue((path / 'users.txt').exists())
            extra = path / 'unexpected'
            extra.write_text('preserve')
            with self.assertRaises(ValueError):
                cleanup(str(path), str(parent), RUNTIME, IMAGE)
            self.assertTrue(extra.exists())
            extra.unlink()
            cleanup(str(path), str(parent), RUNTIME, IMAGE)
            self.assertFalse(path.exists())

    def test_private_cleanup_preserves_outside_paths_and_symlinks(self):
        with tempfile.TemporaryDirectory(prefix='pooler-contract-') as tmp:
            parent = Path(tmp).resolve()
            outside = parent / 'other'
            outside.mkdir()
            with self.assertRaises(ValueError):
                cleanup(str(outside), str(parent), RUNTIME, IMAGE)
            self.assertTrue(outside.exists())
            link = parent / 'dt-native-pooler-link'
            link.symlink_to(outside, target_is_directory=True)
            with self.assertRaises(ValueError):
                cleanup(str(link), str(parent), RUNTIME, IMAGE)
            self.assertTrue(link.is_symlink())
            path = parent / 'dt-native-pooler-owned'
            path.mkdir()
            (path / '.owner.json').write_text(json.dumps({'runtime': RUNTIME, 'image': IMAGE}))
            (path / 'users.txt').symlink_to(outside)
            with self.assertRaises(ValueError):
                cleanup(str(path), str(parent), RUNTIME, IMAGE)
            self.assertTrue(outside.exists())


if __name__ == '__main__':
    unittest.main()
