'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const http = require('node:http');
const g = require('./summary-guard');
async function fixture(t) {
  const root = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-summary-guard-'))); t.after(() => fs.rm(root, { recursive: true, force: true }));
  const candidate = path.join(root, 'candidate'), profile = path.join(root, 'profile'), project = path.join(root, 'project'), cli = path.join(root, 'cli');
  const storage = path.join(profile, 'User/globalStorage/datatug.datatug-everywhere');
  for (const folder of [candidate, profile, project, storage]) await fs.mkdir(folder, { recursive: true, mode: 0o700 });
  await fs.writeFile(cli, 'unit fixture bytes'); await fs.writeFile(path.join(project, 'fixture.json'), '{}'); await fs.writeFile(path.join(candidate, 'package.json'), '{}');
  const plan = { cli, cliSHA256: g.digest('unit fixture bytes'), project, profile, api: 'http://127.0.0.1:12345/v0/', scope: { spaceID: 'space', projectID: 'project', environment: 'demo' }, tokenHashes: { original: g.digest('fake-original'), foreign: g.digest('fake-foreign') }, packagedSources: {}, candidateManifestHash: g.digest('{}') };
  plan.projectMaps = { normal: await g.fileMap(project), version: await g.fileMap(project) };
  const context = { extension: { id: 'datatug.datatug-everywhere', extensionPath: candidate, packageJSON: { main: './test-bootstrap.js' } }, globalStorageUri: { fsPath: storage } };
  const evidence = path.join(storage, 'check-one'); await fs.mkdir(evidence, { mode: 0o700 });
  const args = ['checks','run','--project',project,'--env','demo','--check','pass','--local-owner','--evidence-dir',evidence,'--timeout','30s','--warnings','fail','--format','json','--publish-summary','--summary-space','space','--summary-token-env','DATATUG_SUMMARY_TOKEN'];
  return { plan, context, candidate, storage, args, options: { cwd: project, summaryToken: 'fake-original', timeoutMs: 31000 }, state: { handshake: true, phase: 'pass' } };
}
test('summary acceptance requires actual candidate namespace/path/profile identity', async t => {
  const f = await fixture(t); assert.equal(await g.verifyActivation(f.context, f.plan, f.candidate, f.plan.profile), f.storage);
  for (const alter of [c => { c.extension.id = 'localtest.driver'; }, c => { c.extension.extensionPath = f.plan.project; }, c => { c.globalStorageUri.fsPath = f.plan.profile; }, c => { c.extension.packageJSON.main = './src/extension.js'; }]) {
    const context = structuredClone(f.context); alter(context); await assert.rejects(g.verifyActivation(context, f.plan, f.candidate, f.plan.profile), /Acceptance boundary refused/);
  }
  await fs.writeFile(path.join(f.candidate, 'package.json'), '{"unexpected":true}'); await assert.rejects(g.verifyActivation(f.context, f.plan, f.candidate, f.plan.profile));
});
test('summary acceptance guards exact real launch and only appends literal loopback flag', async t => {
  const f = await fixture(t); const result = await g.validateLaunch(f.plan, f.state, f.plan.cli, f.args, f.options, f.storage);
  assert.deepEqual(result.args, [...f.args, '--summary-api-url', f.plan.api]);
  for (const alter of [f => { f.state.handshake = false; }, f => { f.state.phase = 'missing'; }, f => { f.options.summaryToken = 'foreign-real-credential'; }, f => { f.options.env = {}; }, f => { f.options.secret = { value: 'private' }; }, f => { f.args.push('--principal', 'other'); }, f => { f.plan.cliSHA256 = 'bad'; }, f => { f.plan.api = 'https://api.sneat.cloud/v0/'; }, f => { f.args[10] = f.plan.project; }, f => { f.plan.api = 'http://127.0.0.1:0/v0/'; }]) {
    const next = await fixture(t); alter(next); await assert.rejects(g.validateLaunch(next.plan, next.state, next.plan.cli, next.args, next.options, next.storage));
  }
});
test('summary acceptance distinguishes permitted version mutation and local no-token invocation', async t => {
  const f = await fixture(t); await fs.writeFile(path.join(f.plan.project, 'fixture.json'), '{"version":2}'); f.plan.projectMaps.version = await g.fileMap(f.plan.project);
  await assert.rejects(g.validateLaunch(f.plan, f.state, f.plan.cli, f.args, f.options, f.storage));
  f.state.phase = 'version'; await g.validateLaunch(f.plan, f.state, f.plan.cli, f.args, f.options, f.storage);
  await fs.writeFile(path.join(f.plan.project, 'fixture.json'), '{}'); f.state.phase = 'local'; f.args = f.args.slice(0,17); f.options = { cwd: f.plan.project };
  const local = await g.validateLaunch(f.plan, f.state, f.plan.cli, f.args, f.options, f.storage); assert.deepEqual(local.args, f.args);
  await fs.symlink(f.plan.cli, path.join(f.plan.project, 'escape')); await assert.rejects(g.fileMap(f.plan.project));
});

test('manual native token entry leaves an intervening user copy untouched and requires a new store event', async () => {
  let userClipboard = 'previous user copy';
  const state = { phase: 'store', secretChanges: 4 };
  const native = { window: { async showInputBox(options) { assert.equal(options.password, true); await Promise.resolve(); userClipboard = 'intervening user copy'; return 'fake-token'; } }, env: { get clipboard() { throw new Error('Harness must never access clipboard'); } }, commands: {} };
  const observed = g.observeNativeUI(native, state);
  native.commands.executeCommand = async command => { assert.equal(command, 'datatug.storeHostedToken'); await observed.window.showInputBox({ title: 'Store hosted summary token in SecretStorage', password: true }); state.secretChanges++; };
  await g.storeNativeToken(observed, state, g.digest('fake-token'));
  assert.equal(userClipboard, 'intervening user copy');
  native.commands.executeCommand = async () => { await observed.window.showInputBox({ title: 'Store hosted summary token in SecretStorage', password: true }); };
  await assert.rejects(g.storeNativeToken(observed, state, g.digest('fake-token')), /Acceptance boundary refused/);
  await assert.rejects(g.storeNativeToken(observed, state, g.digest('other-token')), /Acceptance boundary refused/);
});
test('native JSON proof rejects stale same-status documents and binds exact current invocation content', () => {
  const text = JSON.stringify({ status: 'pass', exitCode: 0, checks: [] });
  const document = { isUntitled: true, languageId: 'json', getText: () => text };
  const actual = { phase: 'pass', contentHash: g.digest(text) };
  g.requireFreshResult(document, new Set(), actual, 'pass', 2, 1);
  assert.throws(() => g.requireFreshResult(document, new Set([document]), actual, 'pass', 2, 1));
  assert.throws(() => g.requireFreshResult({ ...document, getText: () => JSON.stringify({ status: 'pass', exitCode: 0, checks: ['stale'] }) }, new Set(), actual, 'pass', 2, 1));
  assert.throws(() => g.requireFreshResult(document, new Set(), actual, 'pass', 1, 1));
  assert.throws(() => g.requireFreshResult(document, new Set(), actual, 'local', 2, 1));
});
test('missing-token proof observes both forwarded genuine modal results and exact genuine error', async () => {
  const state = { phase: 'missing' }, calls = [];
  const native = { window: { async showWarningMessage(...args) { calls.push(args); return args[2]; }, async showErrorMessage(...args) { calls.push(args); } } };
  const observed = g.observeNativeUI(native, state);
  const ownerArgs = ['native owner consent text', { modal: true }, 'Run with local-owner consent'];
  const uploadArgs = ['native upload consent text', { modal: true }, 'Run and publish summary'];
  assert.equal(await observed.window.showWarningMessage(...ownerArgs), ownerArgs[2]);
  assert.equal(await observed.window.showWarningMessage(...uploadArgs), uploadArgs[2]);
  await observed.window.showErrorMessage(g.missingTokenError);
  assert.deepEqual(calls, [ownerArgs, uploadArgs, [g.missingTokenError]]);
  g.requireMissingTokenOutcome(state.uiEvents);
  for (const events of [[], state.uiEvents.slice(0, 2), state.uiEvents.slice(1), [{ ...state.uiEvents[0], accepted: false }, ...state.uiEvents.slice(1)], [state.uiEvents[0], { ...state.uiEvents[1], accepted: false }, state.uiEvents[2]], state.uiEvents.map(event => ({ ...event, phase: 'previous' }))]) assert.throws(() => g.requireMissingTokenOutcome(events));
  native.window.showWarningMessage = async () => undefined;
  state.uiEvents = []; await observed.window.showWarningMessage(...ownerArgs);
  assert.deepEqual(state.uiEvents, [{ phase: 'missing', kind: 'owner', accepted: false }]);
});
test('fixture URLs require literal HTTP loopback and explicit valid port and exact unadorned path', () => {
  for (const valid of ['http://127.0.0.1:1/v0/', 'http://127.0.0.1:80/v0/', 'http://127.0.0.1:65535/v0/']) assert.ok(g.fixtureURL(valid, '/v0/'));
  for (const invalid of ['https://127.0.0.1:123/v0/', 'http://localhost:123/v0/', 'http://127.1:123/v0/', 'http://2130706433:123/v0/', 'http://127.0.0.1/v0/', 'http://127.0.0.1:0/v0/', 'http://127.0.0.1:65536/v0/', 'http://127.0.0.1:0123/v0/', 'http://127.0.0.1:123/v0/?', 'http://127.0.0.1:123/v0/#', 'http://user@127.0.0.1:123/v0/', 'http://127.0.0.1:123/a/../v0/', 'http://127.0.0.1:123/other', 'http://127.0.0.1:123/v0/\n']) assert.throws(() => g.fixtureURL(invalid, '/v0/'));
});
test('passive observer supports frozen native API objects without mutating them', async () => {
  const state = { phase: 'missing' };
  const native = Object.freeze({ window: Object.freeze({ async showWarningMessage(...args) { return args[2]; } }) });
  const observed = g.observeNativeUI(native, state);
  assert.equal(await observed.window.showWarningMessage('genuine text', { modal: true }, 'Run with local-owner consent'), 'Run with local-owner consent');
  assert.deepEqual(state.uiEvents, [{ phase: 'missing', kind: 'owner', accepted: true }]);
  assert.ok(Object.isFrozen(native.window));
});
test('all direct fixture fetches force redirect error and refuse malformed bases before transport', async () => {
  let foreignRequests = 0, requests = 0;
  // A deterministic transport models a fixture redirect; no listening socket
  // or foreign network request is needed for this policy regression.
  const redirectingTransport = async (url, options) => { requests++; assert.equal(url.hostname, '127.0.0.1'); if (options.redirect === 'error') throw new TypeError('fixture redirect denied'); foreignRequests++; return {}; };
  for (const [base, basePath, route, query] of [['http://127.0.0.1:12345', '/', '/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword', { key: 'isolated-summary-test' }], ['http://127.0.0.1:12345/v0/', '/v0/', '/test/events'], ['http://127.0.0.1:12345/v0/', '/v0/', '/v0/datatug/shipping/check-summary', { summaryId: 'synthetic' }]]) {
    await assert.rejects(g.fixtureFetch(base, basePath, route, { redirect: 'follow', headers: { Authorization: 'synthetic-only' } }, query, redirectingTransport), /fixture redirect denied/);
  }
  assert.equal(requests, 3); assert.equal(foreignRequests, 0);
  await assert.rejects(g.fixtureFetch('http://example.test:12345/v0/', '/v0/', '/test/events', {}, undefined, redirectingTransport));
  await assert.rejects(g.fixtureFetch('http://127.0.0.1:12345/v0/', '/v0/', '//example.test/escape', {}, undefined, redirectingTransport));
  assert.equal(requests, 3);
});
test('actual fetch refuses malicious fixture redirect without contacting the destination', { timeout: 5000 }, async t => {
  const privateRoot = process.env.DATATUG_SUMMARY_TEST_EVIDENCE || await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-summary-redirect-'));
  const ownerPath = path.join(privateRoot, 'redirect-fixture-owner.json');
  const owner = { owner: 'summary-guard.test.js real redirect regression', runtime: 'native Node test runner on founder Mac', host: '127.0.0.1', requestedPorts: [0, 0], teardown: 'test after hook closes the two exact http.Server instances and their connections', ports: [], closed: false };
  await fs.writeFile(ownerPath, JSON.stringify(owner), { mode: 0o600 }); // Before either bind.
  let destinationRequests = 0, sourceRequests = 0;
  const destination = http.createServer((_request, response) => { destinationRequests++; response.end('must never be requested'); });
  let target;
  const source = http.createServer((_request, response) => { sourceRequests++; response.writeHead(302, { Location: target }); response.end(); });
  t.after(async () => {
    await Promise.all([source, destination].map(server => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); })));
    owner.closed = true; owner.sourceRequests = sourceRequests; owner.destinationRequests = destinationRequests;
    await fs.writeFile(ownerPath, JSON.stringify(owner), { mode: 0o600 });
    if (!process.env.DATATUG_SUMMARY_TEST_EVIDENCE) await fs.rm(privateRoot, { recursive: true, force: true });
  });
  for (const server of [destination, source]) await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  owner.ports = [source.address().port, destination.address().port]; await fs.writeFile(ownerPath, JSON.stringify(owner), { mode: 0o600 });
  target = `http://127.0.0.1:${destination.address().port}/foreign-destination`;
  const base = `http://127.0.0.1:${source.address().port}`;
  for (const [basePath, route] of [['/', '/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword'], ['/v0/', '/test/events'], ['/v0/', '/v0/datatug/shipping/check-summary']]) {
    await assert.rejects(g.fixtureFetch(base + (basePath === '/' ? '' : basePath), basePath, route, { headers: { Authorization: 'synthetic-only' }, signal: AbortSignal.timeout(2000) }), error => error instanceof TypeError && error.cause?.message === 'unexpected redirect');
  }
  assert.equal(sourceRequests, 3); assert.equal(destinationRequests, 0);
});
