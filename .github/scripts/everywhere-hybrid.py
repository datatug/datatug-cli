#!/usr/bin/env python3
"""Real TLS Coordinator/Runner acceptance; owns and tears down its random project.

Uses synthetic data, ephemeral scoped tokens and a private test CA. It never
installs a CA into the host trust store or contacts a notification provider.
"""
import argparse
import json
from pathlib import Path
import secrets
import shutil
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid



def wait_for_completed_run(request, watch_id, timeout=40, sleep=time.sleep, monotonic=time.monotonic):
    """Poll read-only run state; a temporary 503 is never a Check result."""
    deadline = monotonic()+timeout
    unavailable_reads = 0
    while monotonic() < deadline:
        try:
            runs = request('/v1/watch/runs?watchId='+watch_id).get('runs', [])
        except urllib.error.HTTPError as error:
            if error.code != 503:
                raise
            unavailable_reads += 1
        else:
            completed = next((run for run in runs if run.get('executionStatus') == 'completed'), None)
            if completed:
                return completed, unavailable_reads
        sleep(1)
    raise AssertionError('Runner did not complete a real Check before the polling deadline')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    name = 'dt-hybrid-'+uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix=name) as directory:
        root = Path(directory)
        private, tls = root/'private', root/'private/tls'
        tls.mkdir(parents=True)
        private.chmod(0o700)
        fixture = private/'project'
        shutil.copytree('testdata/checks-demo', fixture)
        manifest = fixture/'datatug-project.json'
        body = json.loads(manifest.read_text())
        body['coordination'] = {'endpoint':'https://coordinator:8990','namespace':'checks-demo'}
        manifest.write_text(json.dumps(body))
        (tls/'extensions.txt').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nsubjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\nsubjectAltName=DNS:coordinator,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n')
        for command in [
            ['req','-x509','-newkey','rsa:2048','-nodes','-keyout','ca.key','-out','ca.crt','-days','1','-subj','/CN=DataTug isolated test CA','-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign','-addext','subjectKeyIdentifier=hash'],
            ['req','-newkey','rsa:2048','-nodes','-keyout','coordinator.key','-out','coordinator.csr','-subj','/CN=coordinator'],
            ['x509','-req','-in','coordinator.csr','-CA','ca.crt','-CAkey','ca.key','-CAcreateserial','-out','coordinator.crt','-days','1','-extfile','extensions.txt']]:
            subprocess.run(['openssl',*command],cwd=tls,check=True,capture_output=True)
        # Host ancestor remains 0700; container UID 65532 can read only the
        # explicitly mounted files. CA signing key is never mounted.
        for filename in ['coordinator.key','coordinator.crt','ca.crt']:
            (tls/filename).chmod(0o644)
        user_token, runner_token = secrets.token_urlsafe(48), secrets.token_urlsafe(48)
        (root/'.env').write_text('DATATUG_IMAGE='+args.image+'\nDATATUG_LOCAL_OWNER=true\nDATATUG_WATCH_ALICE_TOKEN='+user_token+'\nDATATUG_WATCH_RUNNER_TOKEN='+runner_token+'\n')
        (root/'.env').chmod(0o600)
        scope = {'namespace':'checks-demo','project':'checks-demo','environment':'local'}
        coordinator = {'namespace':'checks-demo','listen':'0.0.0.0:8990',
            'tlsCertFile':'/run/tls/coordinator.crt','tlsKeyFile':'/run/tls/coordinator.key',
            'storage':{'dalgoConnectionEnv':'DATATUG_COORDINATOR_DB_URL'},'credentials':[
                dict(scope,kind='user',id='operator',accountId='operator',tier='free',sharedEditor=True,tokenEnv='DATATUG_WATCH_ALICE_TOKEN'),
                dict(scope,kind='runner',id='runner-a',tokenEnv='DATATUG_WATCH_RUNNER_TOKEN')]}
        runner = {'namespace':'checks-demo','runner':{'tokenEnv':'DATATUG_WATCH_RUNNER_TOKEN',
            'coordinatorEndpoint':'https://coordinator:8990','projectDir':'/project','environment':'local',
            'principal':'operator-local-owner','evidenceDir':'/var/lib/datatug/evidence','privateExecutionEvidence':True}}
        for filename, value in [('coordinator',coordinator),('runner',runner)]:
            (private/(filename+'.json')).write_text(json.dumps(value))
        shutil.copyfile('distribution/compose.hybrid.yaml',root/'compose.yaml')
        (root/'port.yaml').write_text(json.dumps({'services':{'coordinator':{'ports':['127.0.0.1::8990']}}}))
        compose = ['docker','compose','--project-name',name,'--env-file',str(root/'.env'),'-f',str(root/'compose.yaml'),'-f',str(root/'port.yaml')]
        try:
            result = subprocess.run(compose+['up','-d','--wait','--wait-timeout','75'],capture_output=True,text=True,timeout=90)
            if result.returncode:
                logs = subprocess.run(compose+['logs','--tail','12'],capture_output=True,text=True)
                diagnostic = (result.stderr+logs.stdout+logs.stderr).replace(user_token,'[redacted]').replace(runner_token,'[redacted]')
                raise RuntimeError('hybrid startup failed: '+diagnostic[-2500:])
            port = subprocess.check_output(compose+['port','coordinator','8990'],text=True).strip().rsplit(':',1)[1]
            base = 'https://127.0.0.1:'+port
            context = ssl.create_default_context(cafile=str(tls/'ca.crt'))
            def request(path, value=None, token=user_token):
                payload = None if value is None else json.dumps(value).encode()
                req = urllib.request.Request(base+path,data=payload,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                with urllib.request.urlopen(req,context=context,timeout=5) as response:
                    return json.load(response)
            try:
                request('/v1/watch/status',token='not-an-authorized-token')
                raise AssertionError('invalid credential admitted')
            except urllib.error.HTTPError as error:
                assert error.code in (401,403)
            definition = dict(scope,id='demo-once',title='Synthetic one-shot',owner='operator',visibility='personal',
                checkId='pass',checkVersion='1',schedule={'intervalSeconds':5},trigger='changes',
                initialObservation='baseline',mode='one_shot',lifecycle='enabled',enabled=True,
                notificationPolicy={'cooldownSeconds':0,'recovery':False})
            request('/v1/watch/definition',{'definition':definition,'operationId':'create-synthetic','expectedVersion':0})
            completed, unavailable_reads = wait_for_completed_run(request, 'demo-once')
            assert completed and completed.get('outcome')=='pass', 'Runner did not complete real Check'
            assert completed.get('id'), 'Coordinator omitted the immutable run identity'
            subprocess.run(compose+['restart','coordinator'],check=True,capture_output=True,timeout=20)
            deadline = time.monotonic()+40
            while True:
                try:
                    # An ephemeral host port may be reallocated on restart.
                    port = subprocess.check_output(compose+['port','coordinator','8990'],text=True).strip().rsplit(':',1)[1]
                    base = 'https://127.0.0.1:'+port
                    retained = request('/v1/watch/runs?watchId=demo-once').get('runs',[])
                    assert any(run.get('id')==completed.get('id') for run in retained)
                    break
                except (OSError,urllib.error.URLError):
                    if time.monotonic()>deadline:
                        raise
                    time.sleep(1)
            # The Runner must not inherit the Coordinator's user/editor token.
            runner_id = subprocess.check_output(compose+['ps','-q','runner'],text=True).strip()
            environment = json.loads(subprocess.check_output(['docker','inspect',runner_id],text=True))[0]['Config']['Env']
            assert not any(item.startswith('DATATUG_WATCH_ALICE_TOKEN=') for item in environment)
            args.output.parent.mkdir(parents=True,exist_ok=True)
            args.output.write_text(json.dumps({'schemaVersion':1,'tlsVerified':True,'invalidCredentialsDenied':True,
                'outboundRealCheck':'pass','temporaryReadFailures':unavailable_reads,'coordinatorStateSurvivedRestart':True,'runnerCredentialsScoped':True,
                'publicDatabasePorts':0},indent=2)+'\n')
            print('TLS hybrid acceptance passed')
        except Exception:
            # Keep failure diagnostics private and remove synthetic credentials;
            # the artifact allowlist never uploads this file.
            logs = subprocess.run(compose+['logs','--no-color','--tail','120'],capture_output=True,text=True,timeout=10)
            safe = (logs.stdout+logs.stderr).replace(user_token,'[redacted-user-token]').replace(runner_token,'[redacted-runner-token]').replace(str(root),'[owned-fixture]')
            diagnostic = args.output.with_suffix('.private.log')
            diagnostic.parent.mkdir(parents=True,exist_ok=True)
            diagnostic.write_text(safe)
            diagnostic.chmod(0o600)
            raise
        finally:
            cleanup = subprocess.run(compose+['down','--volumes','--remove-orphans'],capture_output=True,timeout=30)
            if cleanup.returncode:
                raise RuntimeError('owned hybrid cleanup failed')


if __name__ == '__main__':
    main()
