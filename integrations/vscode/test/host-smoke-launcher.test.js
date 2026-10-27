'use strict';

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');
const { runHostSmoke } = require('../scripts/host-smoke');

// Inert executable fixture: no VS Code, UI, services or network calls.
const editorFixture = `#!${process.execPath}
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const args = process.argv.slice(2);
if (args.length !== 7 || args.some(arg => /no-sandbox|disable-workspace-trust/.test(arg))) throw Error('unsafe host arguments');
for (const key of ['ELECTRON_RUN_AS_NODE', 'NODE_OPTIONS', 'VSCODE_PORTABLE', 'VSCODE_IPC_HOOK_CLI']) if (process.env[key]) throw Error('unsafe host environment');
const profile = args.find(arg => arg.startsWith('--user-data-dir=')).split('=').slice(1).join('=');
const extensions = args.find(arg => arg.startsWith('--extensions-dir=')).split('=').slice(1).join('=');
if (!path.isAbsolute(profile) || !path.isAbsolute(extensions)) throw Error('relative profile');
if (Object.keys(JSON.parse(fs.readFileSync(path.join(profile, 'User/settings.json'), 'utf8'))).some(key => key.includes('trust'))) throw Error('trust override');
fs.writeFileSync(process.env.FIXTURE_MAIN_PID, String(process.pid));
const receipt = { schemaVersion:'datatug.vscode.host-smoke/v1', vscodeVersion:'1.143.1', appName:'Inert fixture',
  nodeVersion:process.version, platform:process.platform, arch:process.arch,
  extensionID:'datatug.datatug-everywhere', active:true, noProject:true, registeredCommands:12,
  emptyTree:true, nativeViewFocus:true, workspaceTrusted:true, remoteName:null,
  cliExecuted:false, workspaceTrustBypassed:false };
const mode = process.env.FIXTURE_MODE;
if (mode === 'timeout' || mode === 'orphan') {
  const child = spawn(process.execPath, ['-e', 'process.on("SIGTERM",()=>{});setInterval(()=>{},1000)'], {stdio:'ignore'});
  fs.writeFileSync(process.env.FIXTURE_CHILD_PID, String(child.pid));
  child.unref();
}
if (mode === 'timeout') { process.on('SIGTERM',()=>{}); setInterval(()=>{},1000); }
else if (mode === 'nonzero') { process.stderr.write('No usable sandbox! PRIVATE-SENTINEL'+'x'.repeat(100000)); process.exitCode=7; }
else if (mode === 'missing') {}
else if (mode === 'malformed') fs.writeFileSync(process.env.DATATUG_VSCODE_HOST_RECEIPT, 'PRIVATE-SENTINEL-not-json');
else if (mode === 'oversized') fs.writeFileSync(process.env.DATATUG_VSCODE_HOST_RECEIPT, 'x'.repeat(8193));
else if (mode === 'symlink') fs.symlinkSync(process.env.FIXTURE_MAIN_PID, process.env.DATATUG_VSCODE_HOST_RECEIPT);
else {
  if (mode === 'unexpected') receipt.privateData='PRIVATE-SENTINEL';
  fs.writeFileSync(process.env.DATATUG_VSCODE_HOST_RECEIPT, JSON.stringify(receipt));
}
`;

async function fixture(t, mode = 'success') {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-host-launcher-test-'));
  await fs.chmod(root, 0o700);
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const executable = path.join(root, 'inert-editor');
  await fs.writeFile(executable, editorFixture, { mode: 0o700 });
  const options = {
    binary: executable, temporaryRoot: root, output: path.join(root, 'output.json'),
    timeoutMs: 2000, killAfterMs: 50, processGroup: process.platform !== 'win32',
    environment: { ...process.env, FIXTURE_MODE: mode, FIXTURE_MAIN_PID: path.join(root, 'main.pid'), FIXTURE_CHILD_PID: path.join(root, 'child.pid') }
  };
  return { root, options };
}

async function assertStopped(pid) {
  const end = Date.now() + 2000;
  while (Date.now() < end) {
    try { process.kill(pid, 0); } catch (error) { if (error.code === 'ESRCH') return; throw error; }
    // Linux may retain an already terminated orphan briefly as a zombie.
    if (process.platform === 'linux') {
      try {
        const stat = await fs.readFile(`/proc/${pid}/stat`, 'utf8');
        if (stat.slice(stat.lastIndexOf(')') + 2).startsWith('Z ')) return;
      } catch (error) { if (error.code === 'ENOENT') return; throw error; }
    }
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.fail(`Owned inert process ${pid} is still running.`);
}

test('host launcher removes stale receipt before a missing executable fails', async t => {
  const { root, options } = await fixture(t);
  await fs.writeFile(options.output, 'stale success');
  options.binary = path.join(root, 'absent');
  await assert.rejects(runHostSmoke(options), { code: 'ENOENT' });
  await assert.rejects(fs.access(options.output), { code: 'ENOENT' });
});

test('host launcher isolates its profile, sanitizes environment and emits only the validated receipt', async t => {
  const { root, options } = await fixture(t);
  Object.assign(options.environment, { ELECTRON_RUN_AS_NODE:'1', NODE_OPTIONS:'--require=invalid', VSCODE_PORTABLE:'/not-used', VSCODE_IPC_HOOK_CLI:'/not-used',
    DATATUG_VSCODE_EXPECTED_VERSION:'1.143.1', DATATUG_VSCODE_EXPECTED_PLATFORM:process.platform, DATATUG_VSCODE_EXPECTED_ARCH:process.arch,
    DATATUG_VSCODE_SOURCE_REVISION:'a'.repeat(40), DATATUG_VSCODE_RUNTIME_SHA256:'b'.repeat(64), RUNNER_OS:'Linux' });
  const receipt = await runHostSmoke(options);
  assert.equal(receipt.hostExitCode, 0); assert.equal(receipt.isolatedProfileRemoved, true); assert.equal(receipt.defaultProfileUsed, false);
  assert.equal(receipt.sourceRevision, 'a'.repeat(40)); assert.equal(receipt.runtimeArtifactSha256, 'b'.repeat(64)); assert.equal(receipt.runnerOS, 'Linux');
  assert.deepEqual(JSON.parse(await fs.readFile(options.output, 'utf8')), receipt);
  assert.equal((await fs.stat(options.output)).mode & 0o777, 0o600);
  assert.deepEqual((await fs.readdir(root)).sort(), ['inert-editor', 'main.pid', 'output.json']);
  await assertStopped(Number(await fs.readFile(options.environment.FIXTURE_MAIN_PID, 'utf8')));
});

test('host launcher rejects missing, malformed, oversized, symlink and unexpected receipts', async t => {
  for (const mode of ['missing', 'malformed', 'oversized', 'symlink', 'unexpected']) {
    await t.test(mode, async t => {
      const { options } = await fixture(t, mode);
      await assert.rejects(runHostSmoke(options), error => !error.message.includes('PRIVATE-SENTINEL'));
      await assert.rejects(fs.access(options.output), { code:'ENOENT' });
    });
  }
});

test('host launcher refuses a different editor version and keeps bounded diagnostics private', async t => {
  const { root, options } = await fixture(t, 'nonzero');
  await assert.rejects(runHostSmoke(options), error => /sandbox prerequisite failed/.test(error.message) && !error.message.includes('PRIVATE-SENTINEL'));
  const [folder] = (await fs.readdir(root)).filter(name => name.startsWith('datatug-vscode-host-'));
  const diagnostics = path.join(root, folder, 'host.log');
  assert.equal((await fs.stat(diagnostics)).mode & 0o777, 0o600);
  assert.ok((await fs.stat(diagnostics)).size <= 65536);
  const mismatch = await fixture(t);
  mismatch.options.environment.DATATUG_VSCODE_EXPECTED_VERSION = '0.0.0';
  await assert.rejects(runHostSmoke(mismatch.options), /differs from the pinned CI expectation/);
  await assert.rejects(fs.access(mismatch.options.output), { code:'ENOENT' });
});

test('host launcher timeout force-stops its owned process group and joins the main child', { skip:process.platform === 'win32' }, async t => {
  const { options } = await fixture(t, 'timeout');
  await assert.rejects(runHostSmoke(options), /exceeded 2000 milliseconds/);
  for (const name of ['FIXTURE_MAIN_PID', 'FIXTURE_CHILD_PID']) await assertStopped(Number(await fs.readFile(options.environment[name], 'utf8')));
  await assert.rejects(fs.access(options.output), { code:'ENOENT' });
});

test('host launcher stops an owned descendant even after its main child exits successfully', { skip:process.platform === 'win32' }, async t => {
  const { options } = await fixture(t, 'orphan');
  await runHostSmoke(options);
  for (const name of ['FIXTURE_MAIN_PID', 'FIXTURE_CHILD_PID']) await assertStopped(Number(await fs.readFile(options.environment[name], 'utf8')));
});

test('host launcher handles termination by stopping its owned group before returning failure', { skip:process.platform === 'win32', timeout:8000 }, async t => {
  const { options } = await fixture(t, 'timeout');
  options.timeoutMs = 5000;
  const launcher = path.resolve(__dirname, '../scripts/host-smoke.js');
  const script = `require(${JSON.stringify(launcher)}).runHostSmoke(${JSON.stringify(options)}).then(()=>process.exitCode=0,error=>{console.error(error.message);process.exitCode=1;});`;
  const child = spawn(process.execPath, ['-e', script], { stdio:['ignore', 'pipe', 'pipe'] });
  const closed = new Promise(resolve => child.once('close', (code, signal) => resolve({ code, signal })));
  t.after(async () => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); await closed; });
  let diagnostic = '';
  child.stdout.resume(); child.stderr.on('data', chunk => { diagnostic += chunk.toString('utf8'); });
  const end = Date.now() + 3000;
  while (true) {
    try { await fs.access(options.environment.FIXTURE_CHILD_PID); break; }
    catch (error) { if (error.code !== 'ENOENT') throw error; }
    assert.ok(Date.now() < end, 'The inert editor must start before launcher termination.');
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  child.kill('SIGTERM');
  assert.deepEqual(await closed, { code:1, signal:null });
  assert.match(diagnostic, /interrupted by SIGTERM/);
  for (const name of ['FIXTURE_MAIN_PID', 'FIXTURE_CHILD_PID']) await assertStopped(Number(await fs.readFile(options.environment[name], 'utf8')));
  await assert.rejects(fs.access(options.output), { code:'ENOENT' });
});

test('host launcher rejects late cancellation and removes any newly published success receipt', async t => {
  for (const signal of ['SIGTERM', 'SIGINT']) {
    for (const phase of ['receipt-read', 'profile-cleanup', 'before-publication', 'after-publication', 'final-join']) {
      await t.test(`${signal} ${phase}`, async t => {
        const { root, options } = await fixture(t);
        const original = { readFile:fs.readFile, rm:fs.rm, writeFile:fs.writeFile, open:fs.open };
        const listenerCount = process.listenerCount(signal);
        let injected = false;
        const interrupt = () => { if (!injected) { injected = true; process.emit(signal); } };
        const afterWrite = (deferFinalJoin = true) => {
          if (phase === 'after-publication') interrupt();
          if (deferFinalJoin && phase === 'final-join') queueMicrotask(() => queueMicrotask(interrupt));
        };
        fs.readFile = async (file, ...args) => {
          const result = await original.readFile(file, ...args);
          if (phase === 'receipt-read' && String(file).startsWith(root) && path.basename(String(file)) === 'receipt.json') interrupt();
          return result;
        };
        fs.rm = async (file, ...args) => {
          const result = await original.rm(file, ...args);
          if (phase === 'profile-cleanup' && String(file).startsWith(root) && path.basename(String(file)).startsWith('datatug-vscode-host-')) interrupt();
          return result;
        };
        fs.writeFile = async (file, ...args) => {
          if (file === options.output && phase === 'before-publication') interrupt();
          const result = await original.writeFile(file, ...args);
          if (file === options.output) afterWrite();
          return result;
        };
        fs.open = async (file, ...args) => {
          const handle = await original.open(file, ...args);
          if (file === options.output) {
            const write = handle.writeFile.bind(handle);
            const close = handle.close.bind(handle);
            handle.writeFile = async (...values) => {
              if (phase === 'before-publication') interrupt();
              const result = await write(...values); afterWrite(false); return result;
            };
            handle.close = async (...values) => {
              const result = await close(...values);
              // The nested microtask lands while the launcher awaits its final
              // already-closed child, after publication's close has completed.
              if (phase === 'final-join') queueMicrotask(() => queueMicrotask(interrupt));
              return result;
            };
          }
          return handle;
        };
        try {
          await assert.rejects(runHostSmoke(options), new RegExp(`interrupted by ${signal}`));
          assert.equal(injected, true, 'The cancellation must reach the intended awaited phase.');
          await assert.rejects(original.readFile(options.output), { code:'ENOENT' });
          const [folder] = (await fs.readdir(root)).filter(name => name.startsWith('datatug-vscode-host-'));
          const diagnostics = path.join(root, folder, 'host.log');
          assert.ok((await fs.stat(diagnostics)).size <= 65536);
          assert.equal((await fs.stat(diagnostics)).mode & 0o777, 0o600);
          assert.equal(process.listenerCount(signal), listenerCount);
        } finally { Object.assign(fs, original); }
      });
    }
  }
});


test('smoke CLI failure never emits an environment-selected private executable path', async t => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-smoke-stderr-'));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.mkdir(path.join(root, 'scripts')); await fs.mkdir(path.join(root, 'dist'));
  const script = path.join(root, 'scripts', 'host-smoke.js');
  await fs.copyFile(path.resolve(__dirname, '../scripts/host-smoke.js'), script);
  const receipt = path.join(root, 'dist', 'host-smoke.json');
  await fs.writeFile(receipt, 'stale success');
  const marker = 'PRIVATE-SENTINEL-EXECUTABLE';
  const environment = { ...process.env, DATATUG_VSCODE_TEST_EXECUTABLE: path.join(root, marker, 'missing'), TMPDIR: root };
  delete environment.NODE_OPTIONS; delete environment.ELECTRON_RUN_AS_NODE;
  const result = spawnSync(process.execPath, [script], { env: environment, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 });
  assert.equal(result.error, undefined); assert.equal(result.status, 1); assert.equal(result.signal, null);
  assert.equal(result.stdout, '');
  assert.ok(!result.stderr.includes(marker), 'private executable marker must not reach stderr');
  assert.equal(result.stderr, 'DATATUG_VSCODE_HOST_SMOKE_FAILED\n');
  await assert.rejects(fs.access(receipt), { code: 'ENOENT' });
  assert.deepEqual((await fs.readdir(root)).sort(), ['dist', 'scripts']);
});


test('smoke CLI success keeps validated metadata in private artifact with fixed stdout', async t => {
  const { root, options } = await fixture(t);
  await fs.mkdir(path.join(root, 'scripts')); await fs.mkdir(path.join(root, 'dist'));
  const script = path.join(root, 'scripts', 'host-smoke.js');
  await fs.copyFile(path.resolve(__dirname, '../scripts/host-smoke.js'), script);
  const environment = { ...options.environment, DATATUG_VSCODE_TEST_EXECUTABLE: options.binary,
    DATATUG_VSCODE_SOURCE_REVISION: 'a'.repeat(40), DATATUG_VSCODE_RUNTIME_SHA256: 'b'.repeat(64), TMPDIR: root };
  delete environment.NODE_OPTIONS; delete environment.ELECTRON_RUN_AS_NODE;
  const result = spawnSync(process.execPath, [script], { env: environment, encoding: 'utf8', timeout: 5000, maxBuffer: 4096 });
  assert.equal(result.error, undefined); assert.equal(result.status, 0); assert.equal(result.signal, null);
  assert.equal(result.stderr, '');
  assert.equal(result.stdout, 'DATATUG_VSCODE_HOST_SMOKE_PASSED\n');
  const output = path.join(root, 'dist', 'host-smoke.json');
  const receipt = JSON.parse(await fs.readFile(output, 'utf8'));
  assert.equal(receipt.sourceRevision, 'a'.repeat(40));
  assert.equal(receipt.runtimeArtifactSha256, 'b'.repeat(64));
  assert.equal(receipt.hostExitCode, 0); assert.equal(receipt.isolatedProfileRemoved, true);
  assert.equal((await fs.stat(output)).mode & 0o777, 0o600);
  await assertStopped(Number(await fs.readFile(options.environment.FIXTURE_MAIN_PID, 'utf8')));
});
