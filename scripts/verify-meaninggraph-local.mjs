#!/usr/bin/env node
// Verify unpublished sibling sources without changing either checkout's dependency files.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {copyFile, lstat, mkdir, mkdtemp, readFile, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {dirname, isAbsolute, join, relative, resolve, sep} from 'node:path';
import {fileURLToPath} from 'node:url';

const args = process.argv.slice(2);
if (args.includes('--help') || !args[0] || args.some((arg, i) => i > 0 && !['--all', '--build'].includes(arg))) {
  console.log('Usage: node scripts/verify-meaninggraph-local.mjs CORE_SOURCE [--all] [--build]\n' +
    'Copies tracked and nonignored source files to a private temporary workspace, records hashes,\n' +
    'and tests the copied CLI against the copied Core. --all runs the complete command package.\n' +
    'Source go.mod/go.sum are unchanged. Temporary evidence and optional binary are retained.');
  process.exit(args.includes('--help') ? 0 : 2);
}
process.umask(0o077);
const cli = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const core = resolve(args[0]);
const scratch = await mkdtemp(join(tmpdir(), 'datatug-meaninggraph-local-'));
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const git = (root, ...argv) => execFileSync('git', ['-C', root, ...argv], {encoding: 'utf8', maxBuffer: 20 * 1024 * 1024});

async function snapshot(source, name, expectedModule) {
  const target = join(scratch, name);
  await mkdir(target);
  const paths = [...new Set(git(source, 'ls-files', '-z', '--cached', '--others', '--exclude-standard').split('\0').filter(Boolean))].sort();
  const files = [];
  for (const path of paths) {
    assert.ok(!isAbsolute(path) && !path.split('/').some(part => part === '..' || part === '.git'), `unsafe source path: ${path}`);
    assert.ok(!['go.work', 'go.work.sum'].includes(path), 'source workspace links must be removed before snapshotting');
    const from = resolve(source, path);
    assert.ok(relative(source, from) !== '..' && !relative(source, from).startsWith(`..${sep}`));
    let stat;
    try { stat = await lstat(from); } catch (error) { if (error.code === 'ENOENT') continue; throw error; }
    assert.ok(stat.isFile(), `snapshot requires ordinary source files: ${path}`);
    const bytes = await readFile(from);
    const to = join(target, path);
    await mkdir(dirname(to), {recursive: true});
    await copyFile(from, to);
    assert.equal(hash(await readFile(to)), hash(bytes), `source changed during copy: ${path}`);
    files.push({path, sha256: hash(bytes)});
  }
  const mod = await readFile(join(target, 'go.mod'), 'utf8');
  assert.equal(/^module\s+(\S+)$/m.exec(mod)?.[1], expectedModule);
  return {source, target, commit: git(source, 'rev-parse', 'HEAD').trim(),
    status: git(source, 'status', '--porcelain').trim(), files,
    snapshotSHA256: hash(JSON.stringify(files)), go: /^go\s+(\S+)$/m.exec(mod)?.[1],
    toolchain: /^toolchain\s+(\S+)$/m.exec(mod)?.[1]};
}
const inputs = [await snapshot(cli, 'cli', 'github.com/datatug/datatug-cli'),
  await snapshot(core, 'core', 'github.com/datatug/datatug-core')];
const versions = values => values.filter(Boolean).sort((a, b) => a.replace(/^go/, '').localeCompare(b.replace(/^go/, ''), undefined, {numeric: true})).at(-1);
const go = versions(inputs.map(input => input.go));
assert.ok(/^\d+\.\d+\.\d+$/.test(go), 'expected exact Go directive');
const toolchain = versions(inputs.map(input => input.toolchain));
const workspace = join(scratch, 'go.work');
await writeFile(workspace, `go ${go}\n${toolchain ? `toolchain ${toolchain}\n` : ''}\nuse (\n\t./cli\n\t./core\n)\n`);
const manifest = {schema: 'datatug-local-source-verification/v1', mode: 'isolated-source-snapshots',
  publicDependencyPublished: false, workspace, inputs, commands: [], result: 'running'};
const manifestPath = join(scratch, 'verification.json');
const persist = () => writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
await persist();
console.log(JSON.stringify({workspace, manifest: manifestPath, snapshots: inputs.map(({commit, snapshotSHA256, source}) => ({source, commit, snapshotSHA256}))}));
const env = {...process.env, GOWORK: workspace, CI: 'true', DO_NOT_TRACK: '1', DATATUG_TELEMETRY: '0'};
// FileStore's existing permission assertions expect the usual 022 child umask.
// The enclosing scratch directory remains private (0700).
process.umask(0o022);
try {
  const test = ['test', './apps/datatugapp/commands'];
  if (!args.includes('--all')) test.push('-run', '^TestDataModels|^TestSaveProject_PersistsDbModels$');
  manifest.commands.push(['go', ...test]);
  execFileSync('go', test, {cwd: inputs[0].target, env, stdio: 'inherit'});
  if (args.includes('--build')) {
    manifest.binary = join(scratch, 'datatug');
    const build = ['build', '-o', manifest.binary, '.'];
    manifest.commands.push(['go', ...build]);
    execFileSync('go', build, {cwd: inputs[0].target, env, stdio: 'inherit'});
    manifest.binarySHA256 = hash(await readFile(manifest.binary));
  }
  manifest.result = 'passed';
} catch (error) {
  manifest.result = 'failed';
  process.exitCode = Number.isInteger(error.status) ? error.status : 1;
} finally {
  await persist();
  console.log(JSON.stringify({result: manifest.result, manifest: manifestPath, binary: manifest.binary}));
}
