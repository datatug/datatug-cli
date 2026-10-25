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
