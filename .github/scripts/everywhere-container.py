#!/usr/bin/env python3
"""Real, isolated execution-image acceptance. Removes only its own resources."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import uuid


def tree_hash(root):
    digest = hashlib.sha256()
    for path in sorted(root.rglob('*')):
        digest.update(str(path.relative_to(root)).encode())
        digest.update(str(path.lstat().st_mode).encode())
        if path.is_file():
            digest.update(path.read_bytes())
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    fixture = Path('testdata/checks-demo').resolve()
    original = tree_hash(fixture)
    prefix = 'dt-everywhere-' + uuid.uuid4().hex[:12]
    volume = prefix+'-evidence'
    names, receipts = [], []
    subprocess.run(['docker', 'volume', 'create', volume], check=True, capture_output=True)
    try:
        for index, (check, expected, status) in enumerate([
            ('pass', 0, 'pass'), ('fail', 1, 'fail'), ('warning', 1, 'warning'),
            ('unknown', 1, 'unknown'), ('source-error', 4, 'error'), ('absent', 3, 'not_found'),
            ('pass', 0, 'pass')]):
            name = prefix+'-'+str(index)
            names.append(name)
            command = ['docker', 'run', '--rm', '--name', name, '--network', 'none',
                '--read-only', '--user', '65532:65532', '--cap-drop', 'ALL',
                '--security-opt', 'no-new-privileges', '--memory', '512m', '--cpus', '1',
                '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m,mode=1777',
                '-v', str(fixture)+':/project:ro', '-v', volume+':/var/lib/datatug/evidence',
                args.image, 'checks', 'run', '--project', '/project', '--env', 'local',
                '--check', check, '--local-owner', '--evidence-dir', '/var/lib/datatug/evidence']
            result = subprocess.run(command, capture_output=True, text=True, timeout=45)
            assert result.returncode == expected, (check, result.returncode)
            envelope = json.loads(result.stdout)
            assert envelope['status'] == status and envelope['exitCode'] == expected
            assert 'synthetic-private-row-sentinel' not in result.stdout+result.stderr
            assert 'Trusted demo Check' not in result.stdout+result.stderr
            receipts.append({'case':check, 'exitCode':expected, 'status':status})
        # Every Cobra true spelling is denied before the Runner can read config.
        for flag in ['--run', '--run=true', '--run=1', '--run=t', '--run=TRUE']:
            result = subprocess.run(['docker','run','--rm',args.image,'--config','/missing','daemon',flag],
                capture_output=True,text=True,timeout=10)
            assert result.returncode != 0 and 'DATATUG_LOCAL_OWNER=true' in result.stderr
        # State survives replacement containers; inspect synthetic receipts only.
        inspector = prefix+'-inspect'
        names.append(inspector)
        subprocess.run(['docker','create','--name',inspector,'-v',volume+':/evidence',args.image],
            capture_output=True,check=True)
        with tempfile.TemporaryDirectory(prefix=prefix) as temporary:
            subprocess.run(['docker','cp',inspector+':/evidence/.',temporary],capture_output=True,check=True)
            evidence = Path(temporary)
            runs = list(evidence.glob('detective/*/runs/*.json'))
            executions = list(evidence.glob('check-executions/*/repository/executions/**/*.json'))
            assert len(runs) >= 6 and executions, 'private receipts did not survive restart'
            for path in evidence.rglob('*'):
                assert path.lstat().st_mode & 0o077 == 0, 'nonprivate evidence mode'
        assert tree_hash(fixture) == original, 'shared project changed'
        image = json.loads(subprocess.check_output(['docker','image','inspect',args.image],text=True))[0]
        assert image['Config']['User'] == '65532:65532'
        args.output.parent.mkdir(parents=True,exist_ok=True)
        args.output.write_text(json.dumps({'schemaVersion':1,'imageId':image['Id'],
            'architecture':image['Architecture'],'results':receipts,'projectUnchanged':True,
            'privateStateSurvivedRestart':True,'runnerConsentVariantsDenied':5},indent=2)+'\n')
        print('container acceptance passed:',image['Architecture'])
    finally:
        for name in names:
            subprocess.run(['docker','rm','-f',name],capture_output=True)
        result = subprocess.run(['docker','volume','rm',volume],capture_output=True)
        if result.returncode:
            raise RuntimeError('owned test volume cleanup failed')


if __name__ == '__main__':
    main()
