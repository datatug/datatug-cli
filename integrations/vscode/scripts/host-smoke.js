'use strict';

const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');

async function main() {
  const binary = process.env.DATATUG_VSCODE_TEST_EXECUTABLE || '/Applications/Visual Studio Code - Insiders.app/Contents/MacOS/Code - Insiders';
  if (!path.isAbsolute(binary)) throw new Error('Provide an explicit absolute installed VS Code executable path.');
  await fs.access(binary);
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-vscode-host-'));
  const receiptPath = path.join(temporary, 'receipt.json');
  const profile = path.join(temporary, 'profile');
  const extensions = path.join(temporary, 'extensions');
  await fs.chmod(temporary, 0o700);
  await fs.mkdir(path.join(profile, 'User'), { recursive: true });
  await fs.mkdir(extensions);
  await fs.writeFile(path.join(profile, 'User', 'settings.json'), JSON.stringify({
    'telemetry.telemetryLevel': 'off', 'update.mode': 'none',
    'extensions.autoUpdate': false, 'extensions.autoCheckUpdates': false,
    'workbench.enableExperiments': false, 'workbench.startupEditor': 'none',
    'window.restoreWindows': 'none'
  }));
  const environment = { ...process.env, DATATUG_VSCODE_HOST_RECEIPT: receiptPath };
  delete environment.ELECTRON_RUN_AS_NODE;
  delete environment.NODE_OPTIONS;
  const args = [
    '--new-window', '--skip-welcome', '--skip-release-notes',
    `--user-data-dir=${profile}`, `--extensions-dir=${extensions}`,
    `--extensionDevelopmentPath=${path.resolve(__dirname, '..')}`,
    `--extensionTestsPath=${path.resolve(__dirname, '../test/host-smoke.js')}`
  ];
  let child, log = '', timedOut = false, forceTimer, completed = false;
  try {
    const code = await new Promise((resolve, reject) => {
      child = spawn(binary, args, { env: environment, stdio: ['ignore', 'pipe', 'pipe'], shell: false });
      const capture = data => { if (log.length < 65536) log += data.toString('utf8').slice(0, 65536 - log.length); };
      child.stdout.on('data', capture); child.stderr.on('data', capture);
      const timeout = setTimeout(() => {
        timedOut = true; child.kill('SIGTERM');
        forceTimer = setTimeout(() => child.kill('SIGKILL'), 2000);
      }, 60000);
      child.once('error', error => { clearTimeout(timeout); reject(error); });
      child.once('close', (exitCode, signal) => { clearTimeout(timeout); clearTimeout(forceTimer); resolve({ exitCode, signal }); });
    });
    if (timedOut || code.exitCode !== 0) throw new Error(timedOut ? 'Real extension-host smoke exceeded 60 seconds.' : `Real extension-host process exited ${code.exitCode} (${code.signal || 'no signal'}).`);
    const receipt = JSON.parse(await fs.readFile(receiptPath, 'utf8'));
    await fs.rm(temporary, { recursive: true, force: true });
    receipt.hostExitCode = code.exitCode;
    receipt.isolatedProfileRemoved = true;
    receipt.defaultProfileUsed = false;
    completed = true;
    await fs.mkdir(path.resolve(__dirname, '../dist'), { recursive: true });
    await fs.writeFile(path.resolve(__dirname, '../dist/host-smoke.json'), `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o600 });
    console.log(JSON.stringify(receipt));
  } catch (error) {
    // Keep diagnostics private and bounded; never echo raw host logs.
    await fs.writeFile(path.join(temporary, 'host.log'), log, { mode: 0o600 });
    error.message += ` Private isolated-host diagnostics: ${temporary}`;
    throw error;
  } finally {
    if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    // The test-host process exits itself after run(). Only this owned profile is removed.
    // Failed profiles remain private for diagnosis; no normal user profile is touched.
    if (completed) await fs.rm(temporary, { recursive: true, force: true });
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
