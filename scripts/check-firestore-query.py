#!/usr/bin/env python3
"""Required real-CLI Firestore acceptance; run under the owned emulator gate."""
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import urllib.request

endpoint = os.environ['FIRESTORE_EMULATOR_HOST']
assert endpoint.startswith('127.0.0.1:'), 'gate requires owned loopback emulator'
cli = sys.argv[1]
project = 'demo-datatug-query-ci'

def seed(database, ident, name, score, owner):
    fields = {'name': {'stringValue': name}, 'score': {'integerValue': str(score)},
              'ownerID': {'stringValue': owner}, 'passwordHash': {'stringValue': 'hidden'}}
    url = f'http://{endpoint}/v1/projects/{project}/databases/{database}/documents/customers/{ident}'
    request = urllib.request.Request(url, data=json.dumps({'fields': fields}).encode(),
                                    headers={'Content-Type': 'application/json'}, method='PATCH')
    with urllib.request.urlopen(request, timeout=10) as response:
        assert response.status == 200

def run(database, args, success=True):
    result = subprocess.run([cli, 'query', 'run', '--db', f'firestore://{project}/{database}',
                             '--format', 'json', *args], capture_output=True, text=True, timeout=30)
    assert (result.returncode == 0) == success, (result.returncode, result.stdout, result.stderr)
    return json.loads(result.stdout) if success else result.stderr

seed('(default)', 'a', 'Alice', 42, 'alice')
seed('(default)', 'b', 'Bob', 5, 'bob')
seed('named-db', 'c', 'Named', 99, 'alice')
with tempfile.TemporaryDirectory(prefix='datatug-firestore-query-') as directory:
    query = pathlib.Path(directory) / 'query.json'
    document = {'from': {'name': 'customers'}, 'columns': [{'field': 'name'}],
                'where': {'op': '>=', 'left': {'field': 'score'}, 'right': {'value': 10}},
                'orderBy': [{'field': 'score', 'desc': True}], 'limit': 1}
    query.write_text(json.dumps(document))
    assert run('(default)', ['-f', str(query), '--no-policies'])[0]['name'] == 'Alice'
    assert run('named-db', ['--from', 'customers', '--no-policies'])[0]['name'] == 'Named'
    # Offsets are applied at the provider, after explicit ordering.
    document.pop('where')
    document['offset'] = 1
    query.write_text(json.dumps(document))
    assert run('(default)', ['-f', str(query), '--no-policies'])[0]['name'] == 'Bob'
    document['columns'][0]['as'] = 'customer'
    query.write_text(json.dumps(document))
    alias_error = run('(default)', ['-f', str(query), '--no-policies'], False)
    assert 'aliases are unsupported' in ' '.join(alias_error.split()), alias_error
    policies = pathlib.Path(directory) / 'policies'
    policies.mkdir()
    (policies / 'global.yaml').write_text(pathlib.Path('docs/policies/global.yaml').read_text())
    rows = run('(default)', ['--from', 'customers', '--as', 'alice', '--policies-dir', str(policies)])
    assert len(rows) == 1 and rows[0]['name'] == 'Alice' and 'passwordHash' not in rows[0], rows
    assert 'access denied' in run('(default)', ['--from', 'unknown', '--as', 'alice', '--policies-dir', str(policies)], False).lower()
print('Firestore CLI: filters/order/window/projection/alias refusal/named database/policy redaction passed')
