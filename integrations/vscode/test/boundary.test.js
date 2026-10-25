'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const b = require('../src/boundary');

async function fixture(t) {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-vscode-test-'));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const project = path.join(temporary, 'project');
  await fs.mkdir(project);
  await fs.writeFile(path.join(project, 'datatug-project.json'), '{}');
  return { temporary: await fs.realpath(temporary), project: await fs.realpath(project) };
}
test('workspace selection rejects physical symlink escape and metadata escape', async t => {
  const { temporary, project } = await fixture(t);
  const outside = path.join(temporary, 'outside');
  await fs.mkdir(outside);
  await fs.writeFile(path.join(outside, 'datatug-project.json'), '{}');
  await fs.symlink(outside, path.join(project, 'linked'));
  await assert.rejects(b.projectPath(path.join(project, 'linked'), [project]), b.SafeError);
  await fs.symlink(path.join(outside, 'datatug-project.json'), path.join(project, 'external.json'));
  await assert.rejects(b.readJSON(project, 'external.json'), b.SafeError);
});
test('catalog reads saved query and no-input Check identifiers; rejects symlink tree', async t => {
  const { project } = await fixture(t);
  for (const dir of ['queries/folder', 'checks', 'environments/local']) await fs.mkdir(path.join(project, dir), { recursive: true });
  await fs.writeFile(path.join(project, 'queries/folder/items.query.json'), JSON.stringify({ title: '<script>\x1b\u202e' }));
  await fs.writeFile(path.join(project, 'checks/check.json'), JSON.stringify({ inputs: [{ name: 'private' }] }));
  await fs.writeFile(path.join(project, 'environments/local/environment-summary.json'), '{}');
  const listing = await b.catalog(project);
  assert.equal(listing.queries[0].id, 'folder/items');
  assert.equal(listing.queries[0].title, '<script>');
  assert.equal(listing.checks[0].hasInputs, true);
  assert.deepEqual(listing.environments, ['local']);
  await fs.symlink(project, path.join(project, 'queries/link'));
  await assert.rejects(b.catalog(project), b.SafeError);
});
test('metadata size bounds reject oversized input before JSON parsing', async t => {
  const { project } = await fixture(t);
  await fs.writeFile(path.join(project, 'large.json'), ' '.repeat(262145));
  await assert.rejects(b.readJSON(project, 'large.json'), b.SafeError);
});
test('CLI ignores workspace/default values and rejects workspace binary including symlinks', async t => {
  const { temporary, project } = await fixture(t);
  await assert.rejects(b.executable({ inspect: () => ({ workspaceValue: process.execPath, defaultValue: process.execPath }) }, [project]), b.SafeError);
  assert.equal(await b.executable({ inspect: () => ({ globalValue: process.execPath }) }, [project]), await fs.realpath(process.execPath));
  const binary = path.join(project, process.platform === 'win32' ? 'cli.exe' : 'cli');
  await fs.copyFile(process.execPath, binary); await fs.chmod(binary, 0o700);
  const alias = path.join(temporary, 'alias'); await fs.symlink(binary, alias);
  await assert.rejects(b.executable({ inspect: () => ({ globalValue: alias }) }, [project]), b.SafeError);
  await assert.rejects(b.executable({ inspect: () => ({ globalValue: binary }) }, [project]), b.SafeError);
});
test('only fixed argv; identifiers cannot inject flags; Check uses private owner contract', () => {
  assert.deepEqual(b.argumentsFor('query', '/project', 'local', 'folder/name'), ['query', 'run', '--project', '/project', '--query', 'folder/name', '--env', 'local', '--format', 'json', '--quiet']);
  const args = b.argumentsFor('check', '/project', 'local', 'pass', '/private');
  assert.deepEqual(args.slice(0, 2), ['checks', 'run']);
  assert.ok(args.includes('--local-owner')); assert.ok(args.includes('--evidence-dir'));
  for (const id of ['--client', '../escape', 'id;touch bad', 'https://secret']) assert.throws(() => b.argumentsFor('query', '/project', 'local', id), b.SafeError);
  assert.throws(() => b.argumentsFor('check', '/project', 'local', 'pass', '/project/private'), b.SafeError);
});
test('credential environment disables daemon/telemetry and denies runtime injection', () => {
  const env = b.childEnvironment({ DATATUG_CLIENT: 'codex-mod', NODE_OPTIONS: '--require attacker', LD_PRELOAD: '/evil', HOME: '/home' }, { variable: 'SHOP_PG_URL', value: 'postgres://secret' });
  assert.equal(env.SHOP_PG_URL, 'postgres://secret'); assert.equal(env.DATATUG_CLIENT, ''); assert.equal(env.DATATUG_TELEMETRY, '0'); assert.equal(env.NODE_OPTIONS, undefined); assert.equal(env.LD_PRELOAD, undefined);
  for (const variable of ['PATH', 'NODE_OPTIONS', 'DATATUG_URL', 'GIT_CONFIG_URL']) assert.throws(() => b.childEnvironment({}, { variable, value: 'bad' }), b.SafeError);
});
test('private evidence is mode 0700 outside every workspace', async t => {
  const { temporary, project } = await fixture(t);
  const dir = await b.evidenceDirectory(path.join(temporary, 'storage'), [project]);
  assert.equal((await fs.stat(dir)).mode & 0o777, 0o700);
  assert.equal(b.inside(project, dir), false);
  await assert.rejects(b.evidenceDirectory(path.join(project, 'storage'), [project]), b.SafeError);
});
test('process boundary passes shell false and preserves single argv/environment secret without logging', async () => {
  const literal = '$(touch SHOULD_NOT_EXIST); --evil';
  const result = await b.runCLI(process.execPath, ['-e', 'process.stdout.write(JSON.stringify({argument: process.argv[1], secret: process.env.SHOP_PG_URL, daemon: process.env.DATATUG_CLIENT}))', literal], { secret: { variable: 'SHOP_PG_URL', value: 'private' } });
  assert.deepEqual(JSON.parse(result.stdout), { argument: literal, secret: 'private', daemon: '' });
});
test('process limits stdout and stderr, discards oversized content, and bounds timeout', async () => {
  for (const stream of ['stdout', 'stderr']) {
    const result = await b.runCLI(process.execPath, ['-e', `process.${stream}.write('PRIVATE'.repeat(2000));setInterval(()=>{},1000)`], { maxBytes: 64, killAfterMs: 20 });
    assert.equal(result.reason, 'size_limit'); assert.equal(result.stdout, '');
    assert.throws(() => b.parseResult('query', result), /Output exceeded/);
  }
  const result = await b.runCLI(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { timeoutMs: 30, killAfterMs: 20 });
  assert.equal(result.reason, 'timeout');
});
test('cancellation terminates an ignoring process with bounded escalation and no partial output', async () => {
  let cancel;
  const token = { onCancellationRequested: callback => { cancel = callback; return { dispose() {} }; }, isCancellationRequested: false };
  const pending = b.runCLI(process.execPath, ['-e', "process.on('SIGTERM',()=>{});process.stdout.write('PRIVATE');setInterval(()=>{},1000)"], { token, killAfterMs: 20 });
  setTimeout(() => cancel(), 100);
  const result = await pending;
  assert.equal(result.reason, 'cancelled'); assert.equal(result.stdout, '');
});
test('start failures and invalid/expired-auth/offline responses never reflect raw output', async () => {
  await assert.rejects(b.runCLI('/definitely/absent/secret', []), e => e instanceof b.SafeError && !e.message.includes('/definitely'));
  for (const result of [{ code: 4, stdout: 'password PRIVATE expired' }, { code: 5, stdout: '{"error":"PRIVATE"}' }, { code: null, stdout: 'network PRIVATE' }]) assert.throws(() => b.parseResult('query', result), error => !error.message.includes('PRIVATE'));
});
test('query results are inert JSON; Check results allowlist fields and ignore arbitrary links', () => {
  const hostile = '<script>alert(1)</script> [click](command:evil) https://attacker/secret';
  const result = b.parseResult('query', { code: 0, stdout: JSON.stringify([{ row: hostile }]) });
  assert.equal(JSON.parse(result.content)[0].row, hostile);
  const data = { schemaVersion: 'datatug.checks.run/v1', status: 'pass', exitCode: 0, checks: [{ index: 1, status: 'pass', executionStatus: 'completed', outcome: 'pass', severity: '', durationMs: 1, rows: hostile }], deepLink: 'https://attacker/secret', password: hostile };
  const check = b.parseResult('check', { code: 0, stdout: JSON.stringify(data) });
  assert.ok(!check.content.includes(hostile)); assert.ok(!check.content.includes('deepLink'));
  data.checks[0].severity = hostile;
  assert.throws(() => b.parseResult('check', { code: 0, stdout: JSON.stringify(data) }), b.SafeError);
  data.checks = []; data.exitCode = 4;
  assert.throws(() => b.parseResult('check', { code: 0, stdout: JSON.stringify(data) }), b.SafeError);
});
test('deep links never accept project/CLI URLs, credentials, query strings, or unsupported context', () => {
  for (const input of ['javascript:evil', 'https://datatug.app@evil/', 'https://datatug.app/?token=PRIVATE', { project: '../', environment: 'PRIVATE' }]) {
    assert.equal(b.hostedLink(input).url, 'https://datatug.app');
    assert.match(b.hostedLink(input).reason, /unavailable/);
  }
});
