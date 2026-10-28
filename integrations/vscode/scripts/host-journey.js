'use strict';

const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { spawn } = require('node:child_process');

async function filesDigest(folder) {
  const digest = createHash('sha256');
  async function visit(relative) {
    for (const entry of (await fs.readdir(path.join(folder, relative), { withFileTypes: true })).sort((a, b) => a.name.localeCompare(b.name))) {
      const item = path.join(relative, entry.name);
      digest.update(item);
      if (entry.isDirectory()) await visit(item);
      else digest.update(await fs.readFile(path.join(folder, item)));
    }
  }
  await visit(''); return digest.digest('hex');
}

// Fixed companion source: no repository or environment value becomes JavaScript.
// The copied journey uses only Node builtins and the real VS Code module; it has
// no __dirname or relative-file dependency that would change after this copy.
const COMPANION_DRIVER = `'use strict';
const vscode = require('vscode');
const fs = require('node:fs/promises');
const path = require('node:path');
const { run } = require('./journey.js');
exports.activate = () => {
  run().then(() => vscode.commands.executeCommand('workbench.action.quit')).catch(async error => {
    await fs.writeFile(path.join(process.env.DATATUG_VSCODE_JOURNEY_CONTROL, 'driver-error.txt'), String(error.stack || error), { mode: 0o600 });
  });
};
`;

async function stageJourneyDriver(extensions) {
  const driver = path.join(extensions, 'localtest.datatug-acceptance-driver');
  await fs.mkdir(driver);
  await fs.writeFile(path.join(driver, 'package.json'), JSON.stringify({
    name: 'datatug-acceptance-driver', publisher: 'localtest', version: '0.0.0',
    engines: { vscode: '^1.95.0' }, main: './driver.js',
    activationEvents: ['onStartupFinished'],
    capabilities: { untrustedWorkspaces: { supported: true } }
  }));
  await fs.writeFile(path.join(driver, 'driver.js'), COMPANION_DRIVER);
  await fs.copyFile(path.resolve(__dirname, '../test/host-journey.js'), path.join(driver, 'journey.js'));
  return driver;
}

async function main() {
  const binary = '/Applications/Visual Studio Code - Insiders.app/Contents/MacOS/Code - Insiders';
  const cli = process.env.DATATUG_TEST_CLI;
  if (!cli || !path.isAbsolute(cli)) throw new Error('Set DATATUG_TEST_CLI to the explicit reviewed candidate binary.');
  await fs.access(binary); await fs.access(cli);
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-vscode-journey-'));
  await fs.chmod(temporary, 0o700);
  const profile = path.join(temporary, 'profile'), extensions = path.join(temporary, 'extensions'), project = path.join(temporary, 'checks-demo'), control = path.join(temporary, 'control');
  const artifacts = path.resolve(__dirname, '../dist/trusted-project-acceptance');
  await fs.mkdir(artifacts, { recursive: true, mode: 0o700 });
  await fs.mkdir(path.join(profile, 'User'), { recursive: true });
  await fs.mkdir(extensions); await fs.mkdir(control, { mode: 0o700 });
  // VS Code's --extensionTestsPath intentionally rejects modal dialogs.
  // A disposable companion extension drives the real API without test-mode
  // overrides. CUA still performs every trust and owner-consent action.
  await stageJourneyDriver(extensions);
  await fs.cp(path.resolve(__dirname, '../../../testdata/checks-demo'), project, { recursive: true });
  const before = await filesDigest(project);
  await fs.writeFile(path.join(profile, 'User', 'settings.json'), JSON.stringify({
    'datatug.cliPath': cli, 'telemetry.telemetryLevel': 'off', 'update.mode': 'none',
    'extensions.autoUpdate': false, 'extensions.autoCheckUpdates': false,
    'workbench.enableExperiments': false, 'workbench.startupEditor': 'none',
    'window.restoreWindows': 'none'
  }));
  const environment = { ...process.env, DATATUG_VSCODE_JOURNEY_CONTROL: control };
  delete environment.ELECTRON_RUN_AS_NODE; delete environment.NODE_OPTIONS;
  const args = ['--new-window', '--skip-welcome', '--skip-release-notes', `--user-data-dir=${profile}`, `--extensions-dir=${extensions}`, `--extensionDevelopmentPath=${path.resolve(__dirname, '..')}`, project];
  await fs.writeFile(path.join(artifacts, 'owner.json'), JSON.stringify({ owner: 'datatug-everywhere-vscode-acceptance-r2', temporary, control, project, profile, hardTimeoutSeconds: 600 }, null, 2), { mode: 0o600 });
  console.log(JSON.stringify({ owner: 'datatug-everywhere-vscode-acceptance-r2', control, project, hardTimeoutSeconds: 600 }));
  let log = '', child, forceTimer, timedOut = false;
  try {
    const outcome = await new Promise((resolve, reject) => {
      child = spawn(binary, args, { env: environment, stdio: ['ignore', 'pipe', 'pipe'], shell: false });
      const capture = data => { if (log.length < 65536) log += data.toString('utf8').slice(0, 65536 - log.length); };
      child.stdout.on('data', capture); child.stderr.on('data', capture);
      const timer = setTimeout(() => { timedOut = true; child.kill('SIGTERM'); forceTimer = setTimeout(() => child.kill('SIGKILL'), 2000); }, 600000);
      child.once('error', error => { clearTimeout(timer); reject(error); });
      child.once('close', (code, signal) => { clearTimeout(timer); clearTimeout(forceTimer); resolve({ code, signal }); });
    });
    if (timedOut || outcome.code !== 0) throw new Error(`Owned acceptance host did not complete: ${timedOut ? 'timeout' : `${outcome.code}/${outcome.signal}`}`);
    const receipt = JSON.parse(await fs.readFile(path.join(control, 'receipt.json'), 'utf8'));
    receipt.sourceProjectUnchanged = before === await filesDigest(project);
    if (!receipt.sourceProjectUnchanged) throw new Error('Synthetic workspace changed during read-only acceptance.');
    const storage = path.join(profile, 'User/globalStorage/datatug.datatug-everywhere');
    const evidence = (await fs.readdir(storage)).filter(name => name.startsWith('check-'));
    receipt.privateEvidenceDirectories = evidence.length;
    receipt.privateEvidenceMode0700 = (await Promise.all(evidence.map(name => fs.stat(path.join(storage, name))))).every(stat => (stat.mode & 0o777) === 0o700);
    receipt.cliSHA256 = createHash('sha256').update(await fs.readFile(cli)).digest('hex');
    receipt.hostExitCode = outcome.code; receipt.defaultProfileUsed = false;
    await fs.rm(temporary, { recursive: true, force: true });
    receipt.ownedProfileProjectAndEvidenceRemoved = true;
    await fs.writeFile(path.join(artifacts, 'receipt.json'), `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o600 });
    console.log(JSON.stringify(receipt));
  } catch (error) {
    await fs.writeFile(path.join(artifacts, 'host-failure.log'), log, { mode: 0o600 });
    throw error;
  } finally {
    if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    await fs.rm(temporary, { recursive: true, force: true });
  }
}

if (require.main === module) main().catch(() => { console.error('DATATUG_VSCODE_JOURNEY_FAILED'); process.exitCode = 1; });
module.exports = { stageJourneyDriver };
