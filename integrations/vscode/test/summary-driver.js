'use strict';
// Disposable companion driver. The candidate bootstrap alone receives its native context.
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const guard = require('./guard');
const vscode = require('vscode');
async function run() {
  const root = process.env.DATATUG_VSCODE_SUMMARY_ROOT;
  const plan = JSON.parse(await fs.readFile(path.join(root, 'plan.json'), 'utf8'));
  const launch = JSON.parse(await fs.readFile(path.join(root, 'launch.json'), 'utf8'));
  const checkpoint = async stage => fs.writeFile(path.join(root, 'checkpoint.json'), JSON.stringify({ stage, launch: launch.phase }), { mode: 0o600 });
  const tokens = JSON.parse(await fs.readFile(path.join(root, 'tokens.json'), 'utf8'));
  const extension = vscode.extensions.getExtension('datatug.datatug-everywhere');
  assert.ok(extension && !extension.isActive, 'Candidate must not activate before the guarded driver handshake.');
  const api = await extension.activate();
  const handshake = JSON.parse(await fs.readFile(path.join(root, `activated-${launch.nonce}.json`), 'utf8'));
  assert.ok(handshake.actualContext && handshake.candidateContextID === 'datatug.datatug-everywhere' && handshake.storageHash === api.acceptance.storageHash);
  await checkpoint('explicit-workspace-trust');
  if (!vscode.workspace.isTrusted) await new Promise(resolve => { const sub = vscode.workspace.onDidGrantWorkspaceTrust(() => { sub.dispose(); resolve(); }); });
  assert.equal(vscode.workspace.workspaceFolders?.length, 1);
  assert.equal(await fs.realpath(vscode.workspace.workspaceFolders[0].uri.fsPath), plan.project);
  await vscode.commands.executeCommand('workbench.view.explorer');
  await vscode.commands.executeCommand('datatug.projects.focus');
  await checkpoint('select-project-and-demo');
  await vscode.commands.executeCommand('datatug.selectProject');
  const items = await api.tree.getChildren();
  assert.ok(items.some(item => item.label === 'Environment: demo'));
  const checks = await api.tree.getChildren(items.find(item => item.kind === 'checks'));
  assert.ok(checks.some(item => item.description === 'pass') && checks.some(item => item.description === 'fail'));
  const store = async alias => {
    await checkpoint(`password-prompt-${alias}`);
    // The operator types the isolated fixture token in the genuine native
    // password prompt. The harness never reads, writes or clears the clipboard.
    await guard.storeNativeToken(vscode, api.acceptance, plan.tokenHashes[alias]);
  };
  if (launch.phase === 'store') {
    await checkpoint('bind-summary-space');
    await vscode.commands.executeCommand('datatug.setSummarySpace');
    await store('original');
    await fs.writeFile(path.join(root, 'stored.json'), JSON.stringify({ nonce: launch.nonce, storageHash: handshake.storageHash, nativeSecretChange: true }), { mode: 0o600 });
    await checkpoint('stored-restart-required');
    await vscode.commands.executeCommand('workbench.action.quit');
    return;
  }
  const stored = JSON.parse(await fs.readFile(path.join(root, 'stored.json'), 'utf8'));
  assert.equal(handshake.storageHash, stored.storageHash, 'Restart must use same candidate namespace/private profile.');
  assert.equal(api.acceptance.secretChanges, 0, 'No token re-entry is allowed before first restarted run.');
  const control = async name => {
    const response = await guard.fixtureFetch(plan.api, '/v0/', `/test/${name}`, { method: 'POST', headers: { 'X-Summary-Test': '1' } });
    assert.equal(response.status, 200); return response.json();
  };
  await control('reset-events');
  const receipts = [];
  const execute = async (phase, check, expectedStatus, expectedHTTP) => {
    api.acceptance.phase = phase;
    const previousDocuments = new Set(vscode.workspace.textDocuments), previousLaunchCount = api.acceptance.launches.length;
    const before = (await control('events')).Events?.length || 0;
    await checkpoint(`run-${phase}-real-consents`);
    await vscode.commands.executeCommand(phase === 'local' ? 'datatug.runCheck' : 'datatug.runCheckWithSummary', check);
    assert.ok(!api.acceptance.blocked, 'Guarded real process launch was refused.');
    const document = vscode.window.activeTextEditor?.document;
    const actual = api.acceptance.launches.at(-1);
    guard.requireFreshResult(document, previousDocuments, actual, phase, api.acceptance.launches.length, previousLaunchCount);
    const data = JSON.parse(document.getText());
    assert.equal(data.status, expectedStatus);
    assert.equal(data.exitCode, expectedStatus === 'fail' ? 1 : 0);
    assert.ok(!document.getText().includes('synthetic-private-row-sentinel'));
    const events = await control('events'); assert.equal(events.Truncated, false);
    const added = (events.Events || []).slice(before);
    if (expectedHTTP) { assert.equal(added.length, 1); assert.equal(added[0].status, expectedHTTP); }
    else assert.equal(added.length, 0);
    receipts.push({ ...actual, HTTPStatus: expectedHTTP || null, nativeJSON: true });
    await fs.writeFile(path.join(root, 'ui-receipts.json'), JSON.stringify(receipts, null, 2), { mode: 0o600 });
  };
  await execute('pass', 'pass', 'pass', 200);
  const oldURI = receipts[0].uri; assert.ok(oldURI?.startsWith('https://datatug.app/'));
  await execute('fail', 'fail', 'fail', 200);
  await store('foreign');
  await execute('foreign', 'pass', 'pass', 403);
  await store('original');
  await control('revoke');
  await execute('revoked', 'pass', 'pass', 403);
  const encoded = new URL(oldURI).hash.slice('#check-summary='.length);
  const reference = JSON.parse(Buffer.from(encoded, 'base64url').toString());
  const old = await guard.fixtureFetch(plan.api, '/v0/', '/v0/datatug/shipping/check-summary', { headers: { Authorization: `Bearer ${tokens.original}` } }, { spaceID: plan.scope.spaceID, project: plan.scope.projectID, environment: 'demo', summaryId: reference.summaryId });
  assert.equal(old.status, 403); assert.equal(old.headers.get('cache-control'), 'no-store'); await old.body.cancel();
  await control('restore');
  const passPath = path.join(plan.project, 'checks/pass.json');
  const original = await fs.readFile(passPath);
  const changed = JSON.parse(original); changed.version = 2;
  await fs.writeFile(passPath, JSON.stringify(changed), { mode: 0o600 });
  try { await execute('version', 'pass', 'pass', 403); }
  finally { await fs.writeFile(passPath, original, { mode: 0o600 }); }
  await checkpoint('delete-native-secret');
  await vscode.commands.executeCommand('datatug.deleteHostedToken');
  const before = api.acceptance.launches.length, eventsBefore = (await control('events')).Events.length, uiBefore = api.acceptance.uiEvents.length;
  api.acceptance.phase = 'missing';
  await checkpoint('missing-token-upload-consents');
  await vscode.commands.executeCommand('datatug.runCheckWithSummary', 'pass');
  guard.requireMissingTokenOutcome(api.acceptance.uiEvents.slice(uiBefore));
  assert.equal(api.acceptance.launches.length, before);
  assert.equal((await control('events')).Events.length, eventsBefore);
  await execute('local', 'pass', 'pass', null);
  await fs.writeFile(path.join(root, 'ui-complete.json'), JSON.stringify({ nonce: launch.nonce, realCandidateContext: true, restartPersisted: true, storageHash: handshake.storageHash, nativeTree: true, nativeConsents: true, secretDeleted: true, missingTokenConsentsAndError: true, missingTokenUploadCLILaunches: 0, localCheckAfterDeletion: true, revokedOldSummaryHTTP: 403, productionOpened: false }), { mode: 0o600 });
  await checkpoint('complete');
  await vscode.commands.executeCommand('workbench.action.quit');
}
exports.activate = () => run().catch(async () => {
  const root = process.env.DATATUG_VSCODE_SUMMARY_ROOT;
  await fs.writeFile(path.join(root, 'driver-error.json'), JSON.stringify({ error: 'Native summary acceptance did not complete; inspect private UI checkpoint.' }), { mode: 0o600 });
});
