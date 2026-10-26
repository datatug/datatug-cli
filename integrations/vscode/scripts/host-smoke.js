'use strict';

const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

async function runHostSmoke({
  environment = process.env,
  binary = environment.DATATUG_VSCODE_TEST_EXECUTABLE || '/Applications/Visual Studio Code - Insiders.app/Contents/MacOS/Code - Insiders',
  temporaryRoot = os.tmpdir(), output = path.resolve(__dirname, '../dist/host-smoke.json'),
  timeoutMs = 60000, killAfterMs = 2000, processGroup = process.platform === 'linux'
} = {}) {
  // A previous success must never stand in for this invocation's receipt.
  await fs.rm(output, { force: true });
  if (!path.isAbsolute(binary)) throw new Error('Provide an explicit absolute installed VS Code executable path.');
  await fs.access(binary, fs.constants.X_OK);
  const temporary = await fs.mkdtemp(path.join(temporaryRoot, 'datatug-vscode-host-'));
  const receiptPath = path.join(temporary, 'receipt.json');
  const profile = path.join(temporary, 'profile');
  const extensions = path.join(temporary, 'extensions');
  const hostEnvironment = { ...environment, DATATUG_VSCODE_HOST_RECEIPT: receiptPath };
  for (const key of ['ELECTRON_RUN_AS_NODE', 'NODE_OPTIONS', 'VSCODE_PORTABLE', 'VSCODE_IPC_HOOK_CLI']) delete hostEnvironment[key];
  const args = [
    '--new-window', '--skip-welcome', '--skip-release-notes',
    `--user-data-dir=${profile}`, `--extensions-dir=${extensions}`,
    `--extensionDevelopmentPath=${path.resolve(__dirname, '..')}`,
    `--extensionTestsPath=${path.resolve(__dirname, '../test/host-smoke.js')}`
  ];
  let child, closed, log = Buffer.alloc(0), timedOut = false, forceTimer, timer, interrupted, spawnError;
  let receipt, failure, outputOwned = false;
  const interruption = () => new Error(`Real extension-host smoke interrupted by ${interrupted}.`);
  const checkInterrupted = () => { if (interrupted) throw interruption(); };
  const signalOwned = signal => {
    if (!child?.pid) return;
    try {
      if (processGroup) process.kill(-child.pid, signal);
      else if (child.exitCode === null && child.signalCode === null) child.kill(signal);
    } catch (error) { if (error.code !== 'ESRCH') throw error; }
  };
  const stopOwned = () => {
    signalOwned('SIGTERM');
    if (!forceTimer) forceTimer = setTimeout(() => signalOwned('SIGKILL'), killAfterMs);
  };
  const onTerm = () => { interrupted = 'SIGTERM'; stopOwned(); };
  const onInt = () => { interrupted = 'SIGINT'; stopOwned(); };
  try {
    await fs.chmod(temporary, 0o700);
    await fs.mkdir(path.join(profile, 'User'), { recursive: true });
    await fs.mkdir(extensions);
    await fs.writeFile(path.join(profile, 'User', 'settings.json'), JSON.stringify({
      'telemetry.telemetryLevel': 'off', 'update.mode': 'none',
      'extensions.autoUpdate': false, 'extensions.autoCheckUpdates': false,
      'workbench.enableExperiments': false, 'workbench.startupEditor': 'none',
      'window.restoreWindows': 'none'
    }));
    child = spawn(binary, args, { env: hostEnvironment, detached: processGroup, stdio: ['ignore', 'pipe', 'pipe'], shell: false });
    closed = new Promise(resolve => child.once('close', (exitCode, signal) => resolve({ exitCode, signal })));
    child.once('error', error => { spawnError = error; });
    const capture = data => { if (log.length < 65536) log = Buffer.concat([log, data.subarray(0, 65536 - log.length)]); };
    child.stdout.on('data', capture); child.stderr.on('data', capture);
    process.on('SIGTERM', onTerm); process.on('SIGINT', onInt);
    timer = setTimeout(() => { timedOut = true; stopOwned(); }, timeoutMs);
    const code = await closed;
    clearTimeout(timer);
    // Linux descendants may outlive the main process or close inherited pipes.
    // The detached group belongs only to this invocation; never search/kill by name.
    signalOwned('SIGKILL');
    clearTimeout(forceTimer);
    if (spawnError) throw spawnError;
    checkInterrupted();
    if (timedOut) throw new Error(`Real extension-host smoke exceeded ${timeoutMs} milliseconds.`);
    if (code.exitCode !== 0) {
      const prerequisite = /(?:No usable sandbox|SUID sandbox|setuid_sandbox_host|Failed to move to new namespace)/i.test(log)
        ? ' Linux Electron sandbox prerequisite failed; protections remain enabled.'
        : /(?:Missing X server|cannot open display|unable to open display)/i.test(log)
          ? ' Linux display prerequisite failed; Xvfb must be available.'
          : /error while loading shared libraries/i.test(log) ? ' Linux runtime shared-library prerequisite failed.' : '';
      throw new Error(`Real extension-host process exited ${code.exitCode} (${code.signal || 'no signal'}).${prerequisite}`);
    }
    const entry = await fs.lstat(receiptPath);
    checkInterrupted();
    if (!entry.isFile() || entry.size > 8192) throw new Error('The host receipt must be a bounded regular JSON file.');
    const contents = await fs.readFile(receiptPath, 'utf8');
    checkInterrupted();
    try { receipt = JSON.parse(contents); }
    catch { throw new Error('The host receipt is not valid JSON.'); }
    const required = {
      schemaVersion: 'datatug.vscode.host-smoke/v1', extensionID: 'datatug.datatug-everywhere',
      active: true, noProject: true, registeredCommands: 12, emptyTree: true, nativeViewFocus: true,
      cliExecuted: false, workspaceTrustBypassed: false, remoteName: null
    };
    for (const [key, value] of Object.entries(required)) if (receipt[key] !== value) throw new Error(`Invalid host receipt field: ${key}.`);
    for (const key of ['vscodeVersion', 'appName', 'nodeVersion', 'platform', 'arch']) {
      if (typeof receipt[key] !== 'string' || !/^[A-Za-z0-9 ._-]{1,80}$/.test(receipt[key])) throw new Error(`Invalid host receipt field: ${key}.`);
    }
    if (typeof receipt.workspaceTrusted !== 'boolean') throw new Error('Invalid host receipt field: workspaceTrusted.');
    const allowed = [...Object.keys(required), 'vscodeVersion', 'appName', 'nodeVersion', 'platform', 'arch', 'workspaceTrusted'];
    if (Object.keys(receipt).some(key => !allowed.includes(key))) throw new Error('The host receipt contains an unexpected field.');
    for (const [key, name] of [['vscodeVersion', 'VERSION'], ['platform', 'PLATFORM'], ['arch', 'ARCH']]) {
      if (environment[`DATATUG_VSCODE_EXPECTED_${name}`] && receipt[key] !== environment[`DATATUG_VSCODE_EXPECTED_${name}`]) throw new Error(`Host receipt ${key} differs from the pinned CI expectation.`);
    }
    for (const [key, name, pattern] of [
      ['sourceRevision', 'DATATUG_VSCODE_SOURCE_REVISION', /^[a-f0-9]{40}$/],
      ['runtimeArtifactSha256', 'DATATUG_VSCODE_RUNTIME_SHA256', /^[a-f0-9]{64}$/]
    ]) {
      if (environment[name]) {
        if (!pattern.test(environment[name])) throw new Error(`Invalid CI receipt metadata: ${key}.`);
        receipt[key] = environment[name];
      }
    }
    if (environment.RUNNER_OS) receipt.runnerOS = environment.RUNNER_OS === 'Linux' ? 'Linux' : 'other';
    await fs.rm(temporary, { recursive: true, force: true });
    checkInterrupted();
    receipt.hostExitCode = code.exitCode;
    receipt.isolatedProfileRemoved = true;
    receipt.defaultProfileUsed = false;
    await fs.mkdir(path.dirname(output), { recursive: true });
    checkInterrupted();
    const publication = await fs.open(output, 'wx', 0o600);
    outputOwned = true;
    try {
      checkInterrupted();
      await publication.writeFile(`${JSON.stringify(receipt, null, 2)}\n`);
      checkInterrupted();
    } finally { await publication.close(); }
    checkInterrupted();
  } catch (error) { failure = error; }
  try {
    clearTimeout(timer);
    // Keep cancellation listeners through this last awaited join. No await may
    // follow their removal on the success path below.
    try {
      signalOwned('SIGKILL');
      if (closed) await closed;
    } catch (error) { failure ||= error; }
    clearTimeout(forceTimer);
    if (interrupted) failure = interruption();
    if (failure) {
      // An exclusive open proves this invocation owns the publication, including
      // a partial write or a signal delivered while write/close was awaited.
      if (outputOwned) await fs.rm(output, { force: true });
      // Keep diagnostics private and bounded; never echo raw host logs.
      await fs.mkdir(temporary, { recursive: true, mode: 0o700 });
      await fs.writeFile(path.join(temporary, 'host.log'), log, { mode: 0o600 });
      if (interrupted) failure = interruption();
      failure.message += ` Private isolated-host diagnostics: ${temporary}`;
      throw failure;
    }
    // The check, listener removal and return are synchronous: a delivered signal
    // cannot be swallowed between a final awaited cleanup and successful return.
    checkInterrupted();
    return receipt;
  } finally {
    clearTimeout(timer);
    clearTimeout(forceTimer);
    process.off('SIGTERM', onTerm); process.off('SIGINT', onInt);
  }
}

if (require.main === module) runHostSmoke().then(receipt => console.log(JSON.stringify(receipt))).catch(error => { console.error(error.message); process.exitCode = 1; });
module.exports = { runHostSmoke };
