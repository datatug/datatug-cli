'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { EventEmitter } = require('node:events');
const ownership = require('./summary-ownership');
async function fixture(t) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-summary-owned-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  return root;
}
test('owned cleanup joins exact SIGKILL close before removing profile and tokens', async t => {
  const root = await fixture(t), owned = new Set(), profile = path.join(root, 'profile'), tokens = path.join(root, 'tokens.json');
  await ownership.ownDirectory(owned, profile); await ownership.ownFile(owned, tokens, 'synthetic-only');
  const child = new EventEmitter(); child.exitCode = null; child.signalCode = null;
  const signals = []; let complete = false;
  child.kill = signal => { signals.push(signal); if (signal === 'SIGKILL') setTimeout(() => { child.signalCode = signal; child.emit('close', null, signal); }, 25); };
  const closed = ownership.joinChild(child);
  const cleaning = ownership.cleanup(child, closed, owned, 1).then(() => { complete = true; });
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.deepEqual(signals, ['SIGTERM', 'SIGKILL']); assert.equal(complete, false);
  await fs.access(profile); await fs.access(tokens);
  await cleaning; assert.equal(complete, true);
  await assert.rejects(fs.access(profile)); await assert.rejects(fs.access(tokens));
});
test('early setup failures remove only exclusively created paths and preserve earlier evidence', async t => {
  const root = await fixture(t), owned = new Set(), profile = path.join(root, 'profile'), evidence = path.join(root, 'checkpoint.json'), foreign = path.join(root, 'tokens.json');
  await fs.writeFile(evidence, 'retained checkpoint'); await fs.writeFile(foreign, 'preexisting synthetic value');
  await ownership.ownDirectory(owned, profile);
  await assert.rejects(ownership.ownFile(owned, foreign, 'replacement'), { code: 'EEXIST' });
  await assert.rejects(ownership.ownDirectory(owned, profile), { code: 'EEXIST' });
  await ownership.cleanup(undefined, undefined, owned);
  await assert.rejects(fs.access(profile));
  assert.equal(await fs.readFile(evidence, 'utf8'), 'retained checkpoint'); assert.equal(await fs.readFile(foreign, 'utf8'), 'preexisting synthetic value');
});
test('harness entrypoints never use clipboard transfer or unguarded direct fetch', async () => {
  for (const file of ['summary-driver.js', '../scripts/summary-host-journey.js']) {
    const source = await fs.readFile(path.join(__dirname, file), 'utf8');
    assert.doesNotMatch(source, /(?:env\.clipboard|pbcopy|pbpaste|(?<![\w.])fetch\s*\()/);
  }
});
