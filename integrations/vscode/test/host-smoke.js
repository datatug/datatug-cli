'use strict';

// Loaded by VS Code's official --extensionTestsPath mechanism, not node:test.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const vscode = require('vscode');

async function run() {
  assert.equal(vscode.workspace.workspaceFolders?.length || 0, 0, 'The isolated smoke must open no workspace.');
  const extension = vscode.extensions.getExtension('datatug.datatug-everywhere');
  assert.ok(extension, 'VS Code must discover the candidate extension.');
  const api = await extension.activate();
  assert.equal(extension.isActive, true);
  const names = ['selectProject', 'selectEnvironment', 'refresh', 'runQuery', 'runCheck', 'addProfile', 'deleteProfile', 'openHosted'].map(name => `datatug.${name}`);
  const registered = await vscode.commands.getCommands(true);
  for (const name of names) assert.ok(registered.includes(name), `Missing command ${name}`);
  assert.ok(api.tree, 'The real host must receive the native tree provider.');
  assert.deepEqual(await api.tree.getChildren(), []);
  await vscode.commands.executeCommand('workbench.view.explorer');
  await vscode.commands.executeCommand('datatug.projects.focus');
  const receipt = {
    schemaVersion: 'datatug.vscode.host-smoke/v1',
    vscodeVersion: vscode.version, appName: vscode.env.appName,
    nodeVersion: process.version, platform: process.platform, arch: process.arch,
    extensionID: extension.id, active: extension.isActive,
    noProject: true, registeredCommands: names.length, emptyTree: true,
    nativeViewFocus: true, workspaceTrusted: vscode.workspace.isTrusted,
    remoteName: vscode.env.remoteName || null,
    cliExecuted: false, workspaceTrustBypassed: false
  };
  assert.ok(process.env.DATATUG_VSCODE_HOST_RECEIPT, 'A private receipt path is required.');
  await fs.writeFile(process.env.DATATUG_VSCODE_HOST_RECEIPT, `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o600 });
}

module.exports = { run };
