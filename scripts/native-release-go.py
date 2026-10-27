#!/usr/bin/env python3
"""Compile natively or import an exact-tag native build for GoReleaser OSS."""
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys


def git(*args):
    return subprocess.check_output(['git', *args], text=True).strip()


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def version_arg(args):
    values = []
    for arg in args:
        if arg.startswith('-ldflags='):
            values += shlex.split(arg.split('=', 1)[1])
    for index, value in enumerate(values):
        if value == '-X' and index + 1 < len(values):
            setting = values[index + 1]
            if setting.startswith('github.com/strongo/buildinfo.version='):
                return setting.split('=', 1)[1]
    raise ValueError('missing release version linker argument')


def main(args):
    if args == ['--pack']:
        manifests = list(Path('dist').glob('**/*.native.json'))
        if len(manifests) != 1:
            raise ValueError(f'expected one native build manifest, found {len(manifests)}')
        manifest = manifests[0]
        metadata = json.loads(manifest.read_text())
        binary = manifest.with_name(metadata['binary'])
        if digest(binary) != metadata['sha256']:
            raise ValueError('native binary changed before packaging')
        payload = Path('native-payload')
        payload.mkdir()
        shutil.copy2(binary, payload / metadata['binary'])
        shutil.copyfile(manifest, payload / 'manifest.json')
        return
    output = Path(args[args.index('-o') + 1])
    version = version_arg(args)
    sha = git('rev-parse', 'HEAD')
    target = (os.environ['GOOS'], os.environ['GOARCH'])
    if os.environ.get('NATIVE_RELEASE_BUILD') == '1':
        # The runner must match the target: CGO cross compilation would undo the
        # point of this pipeline. A native go env query ignores target overrides.
        host = subprocess.check_output(['go', 'env', 'GOHOSTOS', 'GOHOSTARCH'], text=True).split()
        if tuple(host) != target:
            raise ValueError(f'native runner {host} cannot build target {target}')
        subprocess.run(['go', 'build', *args], check=True)
        metadata = {
            'binary': output.name, 'goos': target[0], 'goarch': target[1],
            'commit': sha, 'version': version,
            'tag': os.environ.get('GORELEASER_CURRENT_TAG', ''),
            'sha256': digest(output),
        }
        output.with_name(output.name + '.native.json').write_text(json.dumps(metadata))
        return
    tag = 'v' + version
    if git('rev-parse', f'refs/tags/{tag}^{{commit}}') != sha:
        raise ValueError('release version tag does not point at HEAD')
    matches = []
    for manifest in Path('native-release').glob('*/manifest.json'):
        metadata = json.loads(manifest.read_text())
        if (metadata.get('goos'), metadata.get('goarch')) == target:
            matches.append((manifest, metadata))
    if len(matches) != 1:
        raise ValueError(f'expected one native payload for {target}, found {len(matches)}')
    manifest, metadata = matches[0]
    expected_name = 'datatug.exe' if target[0] == 'windows' else 'datatug'
    if metadata.get('binary') != expected_name:
        raise ValueError('unexpected native binary name')
    if metadata.get('commit') != sha or metadata.get('tag') != tag or metadata.get('version') != version:
        raise ValueError('native payload commit/tag/version does not match release')
    binary = manifest.parent / expected_name
    if binary.is_symlink() or digest(binary) != metadata.get('sha256'):
        raise ValueError('native payload checksum mismatch')
    output.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(binary, output)
    output.chmod(0o755)


if __name__ == '__main__':
    try:
        main(sys.argv[1:])
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        sys.exit(f'native release build refused: {error}')
