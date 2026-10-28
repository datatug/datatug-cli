'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const b = require('../src/boundary');
const cli = process.env.DATATUG_TEST_CLI;
test('candidate CLI synthetic SQLite saved query and Check pass/fail/error use actual contracts', { skip: !cli }, async t => {
  const project = await fs.realpath(path.resolve(__dirname, '../../../testdata/checks-demo'));
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-vscode-cli-'));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const listing = await b.catalog(project);
  assert.ok(listing.queries.some(query => query.id === 'items'));
  const query = b.parseResult('query', await b.runCLI(cli, b.argumentsFor('query', project, 'local', 'items'), { cwd: project }));
  assert.equal(JSON.parse(query.content)[0].ID, 1);
  for (const [id, expected] of [['pass', 'pass'], ['fail', 'fail'], ['source-error', 'error']]) {
    const evidence = await b.evidenceDirectory(path.join(temporary, 'private'), [project]);
    const result = await b.runCLI(cli, b.argumentsFor('check', project, 'local', id, evidence), { cwd: project });
    assert.equal(b.parseResult('check', result).status, expected);
    assert.ok(!b.parseResult('check', result).content.includes('synthetic-private-row-sentinel'));
  }
});
test('actual candidate summary opt-in passes fixed args/env; denial preserves pass/fail and excludes evidence', { skip: !cli }, async t => {
  const http = require('node:http');
  const requests = [];
  const server = http.createServer((req, res) => {
    let body = '';
    req.on('data', chunk => { body += chunk; });
    req.on('end', () => { requests.push({ path: req.url, tokenMatches: req.headers.authorization === 'Bearer synthetic-summary-token', body: JSON.parse(body) }); res.writeHead(403, { 'Content-Type': 'application/json' }); res.end('{"error":"PRIVATE_RAW_SERVER_TEXT"}'); });
  });
  // This ephemeral test-only listener is owned by this test and closed below.
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  t.after(() => new Promise(resolve => server.close(resolve)));
  const project = await fs.realpath(path.resolve(__dirname, '../../../testdata/checks-demo'));
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-vscode-summary-'));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const scope = { spaceID: 'space', projectID: 'checks-demo', environment: 'local' };
  for (const [id, status, code] of [['pass', 'pass', 0], ['fail', 'fail', 1]]) {
    const evidence = await b.evidenceDirectory(path.join(temporary, 'private'), [project]);
    const args = b.argumentsFor('check', project, 'local', id, evidence, scope);
    assert.deepEqual(args.slice(-5), ['--publish-summary', '--summary-space', 'space', '--summary-token-env', 'DATATUG_SUMMARY_TOKEN']);
    assert.ok(!args.includes('--summary-api-url')); assert.ok(!JSON.stringify(args).includes('synthetic-summary-token'));
    // Only this process-level fixture adds the CLI's loopback test endpoint.
    // The extension command has no API URL input, setting, or override.
    const result = await b.runCLI(cli, [...args, '--summary-api-url', `http://127.0.0.1:${server.address().port}/v0/`], { cwd: project, summaryToken: 'synthetic-summary-token', timeoutMs: 31000 });
    const parsed = b.parseResult('check', result, scope);
    assert.equal(result.code, code); assert.equal(parsed.status, status); assert.match(parsed.summary.reason, /denied/);
    for (const forbidden of ['synthetic-summary-token', 'PRIVATE_RAW_SERVER_TEXT', 'synthetic-private-row-sentinel']) assert.ok(!JSON.stringify({ result, parsed, requests }).includes(forbidden));
  }
  assert.equal(requests.length, 2);
  for (const request of requests) {
    assert.equal(request.path, '/v0/datatug/shipping/check-summary'); assert.equal(request.tokenMatches, true);
    assert.deepEqual(Object.keys(request.body).sort(), ['checks', 'environment', 'executedAt', 'operationId', 'project', 'schemaVersion', 'spaceID']);
    assert.equal(request.body.project, 'checks-demo'); assert.equal(request.body.spaceID, 'space');
    assert.deepEqual(Object.keys(request.body.checks[0]).sort(), ['checkId', 'checkVersion', 'durationMs', 'executionStatus', 'outcome', 'severity']);
  }
  const evidence = await b.evidenceDirectory(path.join(temporary, 'private'), [project]);
  const opted = b.argumentsFor('check', project, 'local', 'pass', evidence, scope);
  const missing = await b.runCLI(cli, opted, { cwd: project, env: { ...process.env, DATATUG_SUMMARY_TOKEN: 'ambient-must-not-be-used' } });
  assert.equal(JSON.parse(missing.stdout).deepLinkReason, 'summary_publish_auth_required');
  assert.equal(b.parseResult('check', missing, scope).status, 'pass');
  const localEvidence = await b.evidenceDirectory(path.join(temporary, 'private'), [project]);
  const local = await b.runCLI(cli, b.argumentsFor('check', project, 'local', 'pass', localEvidence), { cwd: project, env: { ...process.env, DATATUG_SUMMARY_TOKEN: 'ambient-must-not-be-used' } });
  assert.equal(JSON.parse(local.stdout).deepLinkReason, 'hosted_result_reference_unsupported');
  assert.equal(b.parseResult('check', local).summary, undefined); assert.equal(requests.length, 2);
});
