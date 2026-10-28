'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { packageVSIX } = require('../scripts/package');
const { auditVSIX } = require('../scripts/audit-vsix');

const target = 'dist/datatug-everywhere-0.1.0.vsix';
function packager(result, { produce = true, regular = true, bytes = 'fresh archive', spawnError, resolveError } = {}) {
  let output = 'old archive';
  let spawned = false;
  const fileSystem = {
    mkdirSync(dir) { assert.equal(dir, 'dist'); },
    rmSync(file) { assert.equal(file, target); output = undefined; },
    lstatSync(file) {
      assert.equal(file, target);
      if (output === undefined) throw Object.assign(new Error('no output'), { code: 'ENOENT' });
      return { isFile: () => regular, size: output.length };
    },
  };
  const dependencies = {
    fileSystem,
    resolve(name) {
      assert.equal(name, '@vscode/vsce/vsce');
      assert.equal(output, undefined, 'old artifact removed even before dependency resolution');
      if (resolveError) throw resolveError;
      return '/inert/vsce';
    },
    spawn(executable, args, options) {
      spawned = true;
      assert.equal(executable, process.execPath);
      assert.deepEqual(args, ['/inert/vsce', 'package', '--no-dependencies', '--out', target]);
      assert.equal(options.env.VSCE_PAT, '');
      assert.equal(output, undefined, 'a stale archive cannot satisfy the new invocation');
      if (spawnError) throw spawnError;
      if (produce) output = bytes;
      return result;
    },
  };
  return { dependencies, output: () => output, spawned: () => spawned };
}

test('packager accepts explicit zero completion only with newly produced regular output', () => {
  const f = packager({ status: 0, signal: null });
  assert.equal(packageVSIX(f.dependencies), 0);
  assert.equal(f.output(), 'fresh archive');
});
for (const [name, result, expected] of [
  ['nonzero exit', { status: 7, signal: null }, 7],
  ['SIGTERM', { status: null, signal: 'SIGTERM' }, 1],
  ['SIGKILL', { status: null, signal: 'SIGKILL' }, 1],
  ['null status without a signal', { status: null, signal: null }, 1],
  ['spawn error', { status: null, error: new Error('cannot start') }, 1],
  ['zero plus error', { status: 0, error: new Error('inconsistent completion') }, 1],
  ['zero plus signal', { status: 0, signal: 'SIGTERM' }, 1],
]) {
  test(`packager rejects ${name} and removes partial/stale output`, () => {
    const f = packager(result);
    assert.equal(packageVSIX(f.dependencies), expected);
    assert.equal(f.output(), undefined);
  });
}
for (const [name, options] of [
  ['no output', { produce: false }],
  ['empty output', { bytes: '' }],
  ['nonregular output', { regular: false }],
  ['thrown spawn failure', { spawnError: new Error('cannot start') }],
  ['dependency resolution failure', { resolveError: new Error('tool unavailable') }],
]) {
  test(`packager rejects ${name} without preserving old output`, () => {
    const f = packager({ status: 0 }, options);
    assert.equal(packageVSIX(f.dependencies), 1);
    assert.equal(f.output(), undefined);
    if (options.resolveError) assert.equal(f.spawned(), false);
  });
}

const paths = ['[Content_Types].xml', 'extension.vsixmanifest', 'extension/package.json', 'extension/readme.md', 'extension/SECURITY.md', 'extension/changelog.md', 'extension/LICENSE.txt', 'extension/src/extension.js', 'extension/src/boundary.js'];
function archive(entries = paths, { manifest = {}, restricted = false } = {}) {
  let payloadReads = 0;
  return {
    payloadReads: () => payloadReads,
    dependencies: {
      execute(command, args) {
        assert.equal(command, 'unzip');
        if (args[0] === '-Z1') return `${entries.join('\n')}\n`;
        assert.equal(args[0], '-p');
        payloadReads++;
        const data = args[2] === 'extension/package.json' ? JSON.stringify(manifest) : restricted ? 'ag-grid' : 'inert file';
        return Buffer.from(data);
      },
      fileSystem: { readFileSync: () => Buffer.from('inert archive bytes') },
    },
  };
}
test('VSIX audit accepts exactly the nine unique required paths', () => {
  const f = archive();
  const result = auditVSIX('inert.vsix', f.dependencies);
  assert.equal(result.files, 9);
  assert.equal(result.runtimeDependencies, 0);
  assert.equal(result.restrictedAssets, 0);
  assert.match(result.sha256, /^[a-f0-9]{64}$/);
  assert.equal(f.payloadReads(), 10);
});
for (const [name, entries] of [
  ['extra duplicate', [...paths, paths[3]]],
  ['duplicate replacing a required file', [...paths.slice(0, -1), paths[3]]],
  ['missing file', paths.slice(0, -1)],
  ['unknown file at the expected count', [...paths.slice(0, -1), 'extension/unexpected.js']],
]) {
  test(`VSIX audit rejects ${name} before reading any payload`, () => {
    const f = archive(entries);
    assert.throws(() => auditVSIX('inert.vsix', f.dependencies));
    assert.equal(f.payloadReads(), 0);
  });
}
test('VSIX audit retains restricted asset rejection', () => {
  const f = archive(paths, { restricted: true });
  assert.throws(() => auditVSIX('inert.vsix', f.dependencies), /Restricted asset/);
});
test('VSIX audit retains runtime dependency rejection', () => {
  const f = archive(paths, { manifest: { dependencies: { unexpected: '1.0.0' } } });
  assert.throws(() => auditVSIX('inert.vsix', f.dependencies), /runtime dependencies/);
});
