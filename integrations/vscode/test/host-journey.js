'use strict';

// Real VS Code companion-extension driver. All trust, selection, credential
// choice, local-owner consent and result opening are actual UI actions by CUA.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const vscode = require('vscode');

async function run() {
  const control = process.env.DATATUG_VSCODE_JOURNEY_CONTROL;
  assert.ok(control);
  const receipt = {
    schemaVersion: 'datatug.vscode.host-journey/v1',
    vscodeVersion: vscode.version, nodeVersion: process.version,
    platform: process.platform, arch: process.arch,
    syntheticOnly: true, workspaceTrustBypassed: false,
    commandsDrivenThroughRealAPI: true, uiConsentAutomocked: false, stages: []
  };
  const checkpoint = async name => {
    receipt.stages.push(name);
    await fs.writeFile(path.join(control, 'checkpoint.json'), JSON.stringify({ checkpoint: name, receipt }, null, 2), { mode: 0o600 });
  };
  const gate = async name => {
    for (;;) {
      try { await fs.access(path.join(control, `${name}.continue`)); return; }
      catch (error) { if (error.code !== 'ENOENT') throw error; }
      await new Promise(resolve => setTimeout(resolve, 250));
    }
  };
  await checkpoint('waiting-for-explicit-workspace-trust');
  if (!vscode.workspace.isTrusted) await new Promise(resolve => {
    const subscription = vscode.workspace.onDidGrantWorkspaceTrust(() => { subscription.dispose(); resolve(); });
  });
  assert.equal(vscode.workspace.isTrusted, true);
  assert.equal(vscode.workspace.workspaceFolders.length, 1);
  const extension = vscode.extensions.getExtension('datatug.datatug-everywhere');
  assert.ok(extension);
  const api = await extension.activate();
  assert.equal(extension.isActive, true);
  await vscode.commands.executeCommand('workbench.view.explorer');
  await vscode.commands.executeCommand('datatug.projects.focus');
  await checkpoint('select-project-environment-ui');
  await vscode.commands.executeCommand('datatug.selectProject');
  const roots = await api.tree.getChildren();
  assert.ok(roots.some(item => item.label === 'Environment: local'));
  const queries = await api.tree.getChildren(roots.find(item => item.kind === 'queries'));
  const checks = await api.tree.getChildren(roots.find(item => item.kind === 'checks'));
  assert.ok(queries.some(item => item.description === 'items'));
  assert.ok(checks.some(item => item.description === 'pass'));
  assert.ok(checks.some(item => item.description === 'fail'));
  receipt.projectEnvironmentSelected = true;
  receipt.savedQueryAndCheckDefinitionsBrowsed = true;
  await checkpoint('project-environment-selected');
  await gate('project');

  for (const [kind, id, expected] of [['query', 'items', 'completed'], ['check', 'pass', 'pass'], ['check', 'fail', 'fail']]) {
    await checkpoint(`${kind}-${id}-awaiting-real-ui-consent`);
    await vscode.commands.executeCommand(kind === 'query' ? 'datatug.runQuery' : 'datatug.runCheck', id);
    const document = vscode.window.activeTextEditor?.document;
    assert.ok(document?.isUntitled, 'The real UI must explicitly open native untitled JSON results.');
    assert.equal(document.languageId, 'json');
    const data = JSON.parse(document.getText());
    if (kind === 'query') {
      assert.equal(data[0].ID, 1);
      assert.equal(data[0].Name, 'synthetic-private-row-sentinel');
      receipt.savedQuery = { status: expected, nativeJSONEditor: true, syntheticRows: data.length };
    } else {
      assert.equal(data.schemaVersion, 'datatug.checks.run/v1');
      assert.equal(data.status, expected);
      assert.equal(data.exitCode, expected === 'pass' ? 0 : 1);
      assert.equal(data.checks.length, 1);
      assert.ok(!document.getText().includes('synthetic-private-row-sentinel'));
      receipt[id === 'pass' ? 'checkPass' : 'checkFail'] = { status: data.status, exitCode: data.exitCode, nativeJSONEditor: true, evidenceRowsExcluded: true };
    }
    await checkpoint(`${kind}-${id}-native-json-open`);
    await gate(`${kind}-${id}`);
    // Leave the real result tab open: closing an editor can finish its focus
    // transition after the next QuickPick opens and inadvertently cancel it.
  }
  receipt.separateOwnerConsentActions = 3;
  receipt.credentialsRequired = false;
  receipt.hostedUploads = false;
  await checkpoint('trusted-project-journey-complete');
  await fs.writeFile(path.join(control, 'receipt.json'), `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o600 });
}

module.exports = { run };
