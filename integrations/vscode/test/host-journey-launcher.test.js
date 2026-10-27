'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

test('journey CLI failure never emits an environment-selected private CLI path', async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-journey-stderr-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.mkdir(path.join(root, 'scripts'));
  const script = path.join(root, 'scripts', 'host-journey.js');
  await fs.copyFile(path.resolve(__dirname, '../scripts/host-journey.js'), script);
  const marker = 'PRIVATE-SENTINEL-CLI';
  const environment = { ...process.env, DATATUG_TEST_CLI: path.join(root, marker, 'missing'), TMPDIR: root };
  delete environment.NODE_OPTIONS; delete environment.ELECTRON_RUN_AS_NODE;
  const result = spawnSync(process.execPath, [script], { env: environment, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 });
  assert.equal(result.error, undefined); assert.equal(result.status, 1); assert.equal(result.signal, null);
  assert.equal(result.stdout, '');
  assert.ok(!result.stderr.includes(marker), 'private CLI marker must not reach stderr');
  assert.equal(result.stderr, 'DATATUG_VSCODE_JOURNEY_FAILED\n');
  assert.deepEqual(await fs.readdir(root), ['scripts']);
});


test('journey module import is inert and stages fixed driver plus exact trusted module', async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "datatug-journey-quote'-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const launcher = path.resolve(__dirname, '../scripts/host-journey.js');
  const environment = { ...process.env, DATATUG_TEST_CLI: path.join(root, 'missing-private-cli'), TMPDIR: root };
  delete environment.NODE_OPTIONS; delete environment.ELECTRON_RUN_AS_NODE;
  const imported = spawnSync(process.execPath, ['-e', 'require(process.argv[1]);', launcher], { env: environment, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 });
  assert.equal(imported.error, undefined); assert.equal(imported.status, 0);
  assert.equal(imported.stdout, ''); assert.equal(imported.stderr, '');
  assert.deepEqual(await fs.readdir(root), []);
  const { stageJourneyDriver } = require('../scripts/host-journey');
  const first = path.join(root, 'first'), second = path.join(root, 'second');
  await fs.mkdir(first); await fs.mkdir(second);
  const driver = await stageJourneyDriver(first), other = await stageJourneyDriver(second);
  assert.deepEqual((await fs.readdir(driver)).sort(), ['driver.js', 'journey.js', 'package.json']);
  const body = await fs.readFile(path.join(driver, 'driver.js'), 'utf8');
  assert.equal(body, await fs.readFile(path.join(other, 'driver.js'), 'utf8'));
  assert.match(body, /require\('\.\/journey\.js'\)/);
  assert.ok(!body.includes(root) && !body.includes(path.resolve(__dirname, '..')));
  assert.deepEqual(await fs.readFile(path.join(driver, 'journey.js')), await fs.readFile(path.resolve(__dirname, 'host-journey.js')));
  assert.equal(require.resolve(path.join(driver, 'journey.js')), await fs.realpath(path.join(driver, 'journey.js')));
  const manifest = JSON.parse(await fs.readFile(path.join(driver, 'package.json'), 'utf8'));
  assert.equal(manifest.main, './driver.js');
  assert.deepEqual(manifest.activationEvents, ['onStartupFinished']);
  assert.deepEqual(manifest.capabilities, { untrustedWorkspaces: { supported: true } });
  // Syntax checks parse the fixed sources without loading vscode or executing run.
  for (const name of ['driver.js', 'journey.js']) {
    const checked = spawnSync(process.execPath, ['--check', path.join(driver, name)], { env: environment, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 });
    assert.equal(checked.error, undefined); assert.equal(checked.status, 0);
    assert.equal(checked.stdout, ''); assert.equal(checked.stderr, '');
  }
});
