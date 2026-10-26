'use strict';
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { randomUUID } = require('node:crypto');
const guard = require('../test/summary-guard');
const ownership = require('../test/summary-ownership');
async function main() {
  const root = process.env.DATATUG_VSCODE_SUMMARY_ROOT;
  guard.requireSafe(typeof root === 'string' && root.startsWith('/private/tmp/datatug-vscode-summary-acceptance-') && await fs.realpath(root) === root);
  const ready = JSON.parse(await fs.readFile(path.join(root, 'backend/browser-ready.json'), 'utf8'));
  guard.requireSafe(ready.projectID.startsWith('demo-')); guard.fixtureURL(ready.apiBase, '/v0/'); guard.fixtureURL(ready.authEmulator, '/');
  const cli = process.env.DATATUG_TEST_CLI;
  guard.requireSafe(typeof cli === 'string' && await fs.realpath(cli) === cli && guard.digest(await fs.readFile(cli)) === '9f4be9b7841dd5dd06df99d095ad992bd1d740340401fda10ee611df824c4d0c');
  const editor = '/Applications/Visual Studio Code - Insiders.app/Contents/MacOS/Code - Insiders'; await fs.access(editor);
  const source = path.resolve(__dirname, '..'), candidate = path.join(root, 'candidate'), profile = path.join(root, 'profile'), extensions = path.join(root, 'extensions'), project = path.join(root, 'checks-demo');
  const owned = new Set(); let child, childClosed, timer, forceTimer;
  try {
    for (const folder of [candidate, profile, extensions, project]) await ownership.ownDirectory(owned, folder);
    const packagedSources = {};
    for (const file of ['src/extension.js', 'src/boundary.js', 'README.md', 'SECURITY.md', 'CHANGELOG.md', 'LICENSE']) {
      const data = await fs.readFile(path.join(source, file)); packagedSources[file] = guard.digest(data);
      await fs.mkdir(path.dirname(path.join(candidate, file)), { recursive: true, mode: 0o700 }); await fs.writeFile(path.join(candidate, file), data, { mode: 0o600 });
    }
    const manifest = JSON.parse(await fs.readFile(path.join(source, 'package.json'), 'utf8')), before = JSON.parse(JSON.stringify(manifest));
    manifest.main = './test-bootstrap.js'; manifest.activationEvents = [];
    for (const view of manifest.contributes.views.explorer) view.when = 'datatug.summaryAcceptanceReady';
    await fs.writeFile(path.join(candidate, 'package.json'), JSON.stringify(manifest, null, 2), { mode: 0o600 });
    for (const [file, target] of [['summary-bootstrap.js', 'test-bootstrap.js'], ['summary-guard.js', 'test-guard.js']]) await fs.copyFile(path.resolve(__dirname, '../test', file), path.join(candidate, target));
    await fs.cp(path.resolve(source, '../../testdata/checks-demo'), project, { recursive: true });
    const manifestPath = path.join(project, 'datatug-project.json'), definition = JSON.parse(await fs.readFile(manifestPath)); definition.id = ready.project;
    await fs.writeFile(manifestPath, JSON.stringify(definition), { mode: 0o600 }); await fs.rename(path.join(project, 'environments/local'), path.join(project, 'environments/demo'));
    const envPath = path.join(project, 'environments/demo/environment-summary.json'), env = JSON.parse(await fs.readFile(envPath)); env.id = 'demo';
    await fs.writeFile(envPath, JSON.stringify(env), { mode: 0o600 });
    const normal = await guard.fileMap(project), altered = JSON.parse(await fs.readFile(path.join(project, 'checks/pass.json'))); altered.version = 2;
    const version = { ...normal, [path.join('checks', 'pass.json')]: guard.digest(JSON.stringify(altered)) }, tokens = {};
    for (const [alias, email] of [['original', ready.userEmail], ['foreign', ready.foreignEmail]]) {
      const response = await guard.fixtureFetch(ready.authEmulator, '/', '/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email, password: ready.password, returnSecureToken: true }) }, { key: 'isolated-summary-test' });
      guard.requireSafe(response.status === 200); const data = await response.json();
      guard.requireSafe(typeof data.idToken === 'string' && JSON.parse(Buffer.from(data.idToken.split('.')[1], 'base64url')).aud === ready.projectID); tokens[alias] = data.idToken;
    }
    await ownership.ownFile(owned, path.join(root, 'tokens.json'), JSON.stringify(tokens));
    const plan = { cli, cliSHA256: guard.digest(await fs.readFile(cli)), api: ready.apiBase, project, profile, scope: { spaceID: ready.spaceID, projectID: ready.project, environment: 'demo' }, packagedSources, candidateManifestHash: guard.digest(await fs.readFile(path.join(candidate, 'package.json'))), projectMaps: { normal, version }, tokenHashes: Object.fromEntries(Object.entries(tokens).map(([k,v]) => [k,guard.digest(v)])) };
    await fs.writeFile(path.join(root, 'plan.json'), JSON.stringify(plan, null, 2), { mode: 0o600 }); await fs.mkdir(path.join(profile, 'User'), { mode: 0o700 });
    await fs.writeFile(path.join(profile, 'User/settings.json'), JSON.stringify({ 'datatug.cliPath': cli, 'telemetry.telemetryLevel': 'off', 'update.mode': 'none', 'extensions.autoUpdate': false, 'extensions.autoCheckUpdates': false, 'workbench.enableExperiments': false, 'workbench.startupEditor': 'none', 'window.restoreWindows': 'none' }), { mode: 0o600 });
    const companion = path.join(extensions, 'localtest.datatug-summary-driver'); await fs.mkdir(companion, { mode: 0o700 });
    await fs.writeFile(path.join(companion, 'package.json'), JSON.stringify({ name: 'datatug-summary-driver', publisher: 'localtest', version: '0.0.0', engines: { vscode: '^1.95.0' }, extensionKind: ['workspace'], main: './driver.js', activationEvents: ['onStartupFinished'], capabilities: { untrustedWorkspaces: { supported: true } } }), { mode: 0o600 });
    await fs.copyFile(path.resolve(__dirname, '../test/summary-driver.js'), path.join(companion, 'driver.js'));
    await fs.copyFile(path.resolve(__dirname, '../test/summary-guard.js'), path.join(companion, 'guard.js'));
    const owner = { owner: 'datatug-everywhere-instrumented-vscode-summary-r9', root, profile, extensions, candidate, project, actualCandidateID: 'datatug.datatug-everywhere', manifestDelta: { before, after: manifest }, bootstrapHash: guard.digest(await fs.readFile(path.join(candidate, 'test-bootstrap.js'))), packagedSources, nativeCLIHash: plan.cliSHA256, teardown: 'Quit each exact private-profile child through driver, await close; on failure SIGTERM then SIGKILL exact child and await close. Remove this invocation owned profile/extensions/candidate/project/tokens on every exit. Never access clipboard. Backend/emulators and browser-ready credentials have separate outer ownership.', tokenEntry: 'Operator must type the isolated fixture token into the genuine native password prompt; no clipboard transfer.', limits: 'Instrumented native UI/SecretStorage/CLI/emulator API only; no production routing/signatures/live hosted UI.' };
    await fs.writeFile(path.join(root, 'editor-owner.json'), JSON.stringify(owner, null, 2), { mode: 0o600 });
    for (const phase of ['store', 'exercise']) {
      const nonce = randomUUID(); await fs.writeFile(path.join(root, 'launch.json'), JSON.stringify({ phase, nonce, candidate }), { mode: 0o600 });
      const environment = { ...process.env, DATATUG_VSCODE_SUMMARY_ROOT: root, DATATUG_VSCODE_SUMMARY_NONCE: nonce, DATATUG_POLICIES_DIR: path.join(root, 'policies'), DATATUG_NO_BROWSER_AUTH: '1' };
      for (const key of ['ELECTRON_RUN_AS_NODE', 'NODE_OPTIONS', 'DATATUG_SUMMARY_TOKEN', 'VSCE_PAT']) delete environment[key];
      const args = ['--new-window', '--skip-welcome', '--skip-release-notes', `--user-data-dir=${profile}`, `--extensions-dir=${extensions}`, `--extensionDevelopmentPath=${candidate}`, project];
      await fs.mkdir(environment.DATATUG_POLICIES_DIR, { recursive: true, mode: 0o700 });
      let log = ''; const result = await new Promise((resolve, reject) => {
        child = spawn(editor, args, { env: environment, stdio: ['ignore', 'pipe', 'pipe'], shell: false });
        childClosed = ownership.joinChild(child);
        fs.writeFile(path.join(root, 'editor-process.json'), JSON.stringify({ pid: child.pid, phase, args }), { mode: 0o600 }).catch(reject);
        const capture = data => { if (log.length < 65536) log += data.toString().slice(0, 65536-log.length); }; child.stdout.on('data', capture); child.stderr.on('data', capture);
        timer = setTimeout(() => { child.kill('SIGTERM'); forceTimer = setTimeout(() => child.kill('SIGKILL'), 2000); }, 600000); child.once('error', reject); child.once('close', code => { clearTimeout(timer); clearTimeout(forceTimer); resolve(code); });
      });
      await fs.writeFile(path.join(root, `editor-${phase}.log`), log, { mode: 0o600 });
      guard.requireSafe(result === 0 && !await fs.access(path.join(root, 'driver-error.json')).then(() => true, () => false));
      const completed = JSON.parse(await fs.readFile(path.join(root, phase === 'store' ? 'stored.json' : 'ui-complete.json'), 'utf8'));
      guard.requireSafe(completed.nonce === nonce);
    }
    guard.requireSafe(JSON.stringify(await guard.fileMap(project)) === JSON.stringify(normal));
    for (const [file, hash] of Object.entries(packagedSources)) guard.requireSafe(guard.digest(await fs.readFile(path.join(candidate, file))) === hash);
    await fs.writeFile(path.join(root, 'editor-complete.json'), JSON.stringify({ nativeHostExit: 0, actualCandidateContext: true, sameProfileRestart: true, packagedSourceUnchanged: true, syntheticProjectRestored: true, productionRouting: false }), { mode: 0o600 });
  } finally {
    clearTimeout(timer); clearTimeout(forceTimer);
    await ownership.cleanup(child, childClosed, owned);
    await fs.writeFile(path.join(root, 'editor-cleanup.json'), JSON.stringify({ exactChildJoined: !!childClosed, removedOwnedPaths: [...owned], clipboardAccess: false }), { mode: 0o600 });
  }
}
main().catch(() => { console.error('Instrumented native summary acceptance did not complete. Inspect private evidence and editor-cleanup.json for teardown completion.'); process.exitCode = 1; });
