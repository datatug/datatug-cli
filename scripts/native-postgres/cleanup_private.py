"""Remove only the exact owned credential directory, after container removal."""
import json
import os
from pathlib import Path
import sys


def cleanup(directory, base, runtime, image):
    path, parent = Path(directory), Path(base)
    if (not path.is_absolute() or not parent.is_absolute()
            or path.parent != parent or not path.name.startswith('dt-native-pooler-')
            or path.is_symlink()):
        raise ValueError()
    if not path.exists():
        return
    if path.resolve() != path or parent.resolve() != parent:
        raise ValueError()
    marker = path / '.owner.json'
    if marker.is_symlink() or json.loads(marker.read_text()) != {'runtime': runtime, 'image': image}:
        raise ValueError()
    files = list(path.iterdir())
    if any(file.name not in {'.owner.json', 'users.txt', 'pgbouncer.ini'}
           or file.is_symlink() or not file.is_file() for file in files):
        raise ValueError()
    for file in files:
        file.unlink()
    path.rmdir()
    if path.exists():
        raise ValueError()


if __name__ == '__main__':
    try:
        directory = os.environ.get('DATATUG_NATIVE_POOLER_DIRECTORY', '')
        if directory:
            cleanup(directory, os.environ['RUNNER_TEMP'],
                    os.environ['DATATUG_NATIVE_POOLER_RUNTIME'], os.environ['DATATUG_NATIVE_POOLER_IMAGE'])
    except Exception:
        # Never print auth file content, paths or exception details.
        sys.exit('native pooler: private directory ownership/cleanup unverified')
