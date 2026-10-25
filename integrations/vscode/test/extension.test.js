'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createExtension } = require('../src/extension');

function fake(options = {}) {
  const commands = new Map(), messages = [], documents = [], secrets = new Map(), state = new Map(), calls = [];
  class EventEmitter { constructor() { this.event = () => ({ dispose() {} }); } fire() {} dispose() {} }
  class CancellationTokenSource { constructor() { this.token = { isCancellationRequested: false, onCancellationRequested: () => ({ dispose() {} }) }; } cancel() { this.token.isCancellationRequested = true; } dispose() {} }
  const vscode = {
    EventEmitter, CancellationTokenSource,
    TreeItemCollapsibleState: { Collapsed: 1 }, ProgressLocation: { Notification: 15 },
    Uri: { parse: url => url }, env: { openExternal: async url => calls.push(['url', url]) },
    workspace: {
      isTrusted: options.trusted ?? true,
      workspaceFolders: [{ uri: { scheme: 'file', fsPath: '/workspace' } }],
      getConfiguration: () => ({ inspect: () => ({ globalValue: '/trusted/datatug' }) }),
      openTextDocument: async document => { documents.push(document); return document; },
      onDidChangeWorkspaceFolders: () => ({ dispose() {} })
    },
    commands: { registerCommand: (name, handler) => { commands.set(name, handler); return { dispose() {} }; } },
    window: {
      registerTreeDataProvider: () => ({ dispose() {} }),
      showOpenDialog: async () => [{ scheme: 'file', fsPath: '/workspace/project' }],
      showQuickPick: async items => options.pick ? options.pick(items) : items[0],
      showInputBox: async () => options.inputs?.shift(),
      showWarningMessage: async (message, _modal, action) => { messages.push(message); return options.consent === false ? undefined : action; },
      showInformationMessage: async message => { messages.push(message); return options.open ? 'Open JSON results' : undefined; },
      showErrorMessage: async message => messages.push(message),
      showTextDocument: async document => calls.push(['document', document]),
      withProgress: async (_options, action) => action({}, new CancellationTokenSource().token)
    }
  };
  const context = {
    subscriptions: [], globalStorageUri: { fsPath: '/private/storage' },
    globalState: { get: (key, fallback) => state.get(key) ?? fallback, update: async (key, value) => state.set(key, value) },
    secrets: { store: async (key, value) => secrets.set(key, value), get: async key => secrets.get(key), delete: async key => secrets.delete(key) }
  };
  const dependencies = {
    projectPath: async selected => selected,
    readJSON: async () => ({ id: 'project' }),
    catalog: async () => ({ environments: ['local'], queries: [{ id: 'items', title: 'Items' }], checks: [{ id: 'pass', title: 'Pass' }, { id: 'input', title: 'Input', hasInputs: true }] }),
    executable: async () => '/trusted/datatug',
    evidenceDirectory: async () => '/private/storage/check-abc',
    runCLI: async (file, args, opts) => { calls.push(['run', file, args, opts]); return { code: 0, stdout: '[{"private":"<script>"}]' }; },
    ...options.dependencies
  };
  const extension = createExtension(vscode, context, dependencies);
  return { vscode, context, extension, commands, messages, documents, secrets, state, calls };
}
test('activation registers native commands; empty/trust-denied workspace never reads or spawns', async () => {
  let reads = 0;
  const f = fake({ trusted: false, dependencies: { catalog: async () => { reads++; } } });
  assert.equal(f.commands.size, 12);
  assert.deepEqual(await f.extension.tree.getChildren(), []);
  await f.commands.get('datatug.selectProject')(); await f.commands.get('datatug.runQuery')(); await f.commands.get('datatug.addProfile')();
  assert.equal(reads, 0); assert.equal(f.calls.length, 0); assert.equal(f.secrets.size, 0);
  assert.ok(f.messages.every(message => message.includes('Trust')));
});
test('no-project cancellation does not run; owner consent remains separate from workspace trust', async () => {
  const empty = fake(); empty.vscode.window.showOpenDialog = async () => undefined;
  await empty.commands.get('datatug.runQuery')(); assert.equal(empty.calls.length, 0);
  const f = fake({ consent: false });
  await f.commands.get('datatug.selectProject')(); await f.commands.get('datatug.runQuery')();
  assert.equal(f.calls.length, 0); assert.ok(f.messages.some(message => message.includes('Workspace trust alone')));
});
test('query JSON opens only on explicit request; plain native JSON document contains inert text', async () => {
  for (const open of [false, true]) {
    const f = fake({ open });
    await f.commands.get('datatug.selectProject')(); await f.commands.get('datatug.runQuery')('items');
    const run = f.calls.find(call => call[0] === 'run');
    assert.deepEqual(run[2].slice(0, 2), ['query', 'run']);
    assert.equal(run[2][run[2].indexOf('--env') + 1], 'local');
    assert.equal(f.documents.length, open ? 1 : 0);
    if (open) { assert.equal(f.documents[0].language, 'json'); assert.equal(JSON.parse(f.documents[0].content)[0].private, '<script>'); }
    assert.ok(f.messages.every(message => !message.includes('<script>')));
  }
});
test('Check input declarations are refused before owner/source access; no bare checks TUI', async () => {
  const f = fake(); await f.commands.get('datatug.selectProject')();
  await f.commands.get('datatug.runCheck')('input'); assert.equal(f.calls.length, 0);
  await f.commands.get('datatug.runCheck')('pass');
  const args = f.calls.find(call => call[0] === 'run')[2];
  assert.deepEqual(args.slice(0, 2), ['checks', 'run']);
  assert.ok(args.includes('--local-owner')); assert.ok(args.includes('/private/storage/check-abc'));
});
test('connection profiles store values only in SecretStorage and inject only after explicit selection', async () => {
  const f = fake({ inputs: ['Demo', 'SHOP_PG_URL', 'postgres://PRIVATE'], pick: items => items.find(item => item.profile) || items[0] });
  await f.commands.get('datatug.addProfile')();
  assert.equal(f.secrets.size, 1); assert.ok(!JSON.stringify([...f.state]).includes('PRIVATE'));
  await f.commands.get('datatug.selectProject')(); await f.commands.get('datatug.runQuery')('items');
  const run = f.calls.find(call => call[0] === 'run');
  assert.equal(run[3].secret.value, 'postgres://PRIVATE'); assert.ok(!JSON.stringify(run[2]).includes('PRIVATE'));
  assert.ok(f.messages.every(message => !message.includes('PRIVATE')));
  await f.commands.get('datatug.deleteProfile')(); assert.equal(f.secrets.size, 0); assert.deepEqual(f.state.get('profiles'), []);
});
test('unhandled driver errors are replaced by constant safe text', async () => {
  const f = fake({ dependencies: { runCLI: async () => { throw new Error('password PRIVATE'); } } });
  await f.commands.get('datatug.selectProject')(); await f.commands.get('datatug.runQuery')('items');
  assert.ok(f.messages.every(message => !message.includes('PRIVATE'))); assert.equal(f.documents.length, 0);
});
test('workspace trust is rechecked after prompts and before execution', async () => {
  const f = fake(); await f.commands.get('datatug.selectProject')();
  f.vscode.window.showWarningMessage = async () => { f.vscode.workspace.isTrusted = false; return 'Run with local-owner consent'; };
  await f.commands.get('datatug.runQuery')('items'); assert.equal(f.calls.length, 0);
});
function checkResult(status = 'pass', extra = {}) {
  const code = status === 'pass' ? 0 : 1;
  return { code, stdout: JSON.stringify({ schemaVersion: 'datatug.checks.run/v1', status, exitCode: code, checks: [], ...extra }) };
}
function summaryLink() {
  return 'https://datatug.app/space/space/store/firestore/project/project/overview#check-summary=' + Buffer.from(JSON.stringify({ v: 1, environment: 'local', summaryId: 'A'.repeat(43) })).toString('base64url');
}
async function summarySetup(f) {
  await f.commands.get('datatug.setSummarySpace')();
  await f.commands.get('datatug.storeHostedToken')();
  await f.commands.get('datatug.selectProject')();
}
test('summary binding is separate from SecretStorage token; default Check never reads hosted auth', async () => {
  const f = fake({ inputs: ['space', 'PRIVATE_TOKEN'], dependencies: { runCLI: async (...args) => { f.calls.push(['run', ...args]); return checkResult(); } } });
  await summarySetup(f);
  assert.equal(f.state.get('summarySpace'), 'space');
  assert.equal(f.secrets.get('hosted.summaryToken'), 'PRIVATE_TOKEN');
  assert.ok(!JSON.stringify([...f.state]).includes('PRIVATE_TOKEN'));
  f.context.secrets.get = async () => { throw new Error('default Check must not load hosted auth'); };
  await f.commands.get('datatug.runCheck')('pass');
  const run = f.calls.find(c => c[0] === 'run');
  assert.ok(run); assert.ok(!run[2].includes('--publish-summary')); assert.equal(run[3].summaryToken, undefined);
  assert.ok(f.messages.every(m => !m.includes('PRIVATE_TOKEN')));
});
test('summary command requires separate consent; token only selected child env and explicit summary action', async () => {
  const f = fake({ inputs: ['space', 'PRIVATE_TOKEN'], dependencies: { runCLI: async (...args) => { f.calls.push(['run', ...args]); return checkResult('pass', { deepLinkReason: 'hosted_summary_published', deepLink: summaryLink() }); } } });
  await summarySetup(f);
  f.vscode.window.showInformationMessage = async (message, ...actions) => { f.messages.push(message); return actions.includes('Open summary in DataTug.app') ? 'Open summary in DataTug.app' : undefined; };
  await f.commands.get('datatug.runCheckWithSummary')('pass');
  const run = f.calls.find(c => c[0] === 'run');
  assert.deepEqual(run[2].slice(-5), ['--publish-summary', '--summary-space', 'space', '--summary-token-env', 'DATATUG_SUMMARY_TOKEN']);
  assert.equal(run[3].summaryToken, 'PRIVATE_TOKEN'); assert.equal(run[3].timeoutMs, 31000);
  assert.ok(!JSON.stringify(run[2]).includes('PRIVATE_TOKEN')); assert.ok(!run[2].includes('--summary-api-url'));
  assert.ok(f.messages.some(m => m.includes('seven days') && m.includes('SQL, rows, inputs, error text')));
  assert.deepEqual(f.calls.filter(c => c[0] === 'url'), [['url', summaryLink()]]);
  assert.ok(f.messages.every(m => !m.includes('PRIVATE_TOKEN')));
});
test('trust and owner cancellation precede summary consent and token lookup', async () => {
  for (const scenario of ['untrusted', 'owner-cancel', 'summary-cancel', 'trust-revoked']) {
    const f = fake({ inputs: ['space', 'PRIVATE_TOKEN'] }); await summarySetup(f);
    let reads = 0, summaryPrompts = 0;
    f.context.secrets.get = async () => { reads++; return 'PRIVATE_TOKEN'; };
    if (scenario === 'untrusted') f.vscode.workspace.isTrusted = false;
    f.vscode.window.showWarningMessage = async (_m, _o, action) => {
      if (action === 'Run and publish summary') { summaryPrompts++; if (scenario === 'summary-cancel') return undefined; }
      if (scenario === 'owner-cancel') return undefined;
      if (scenario === 'trust-revoked') f.vscode.workspace.isTrusted = false;
      return action;
    };
    await f.commands.get('datatug.runCheckWithSummary')('pass');
    assert.equal(f.calls.filter(c => c[0] === 'run').length, 0, scenario); assert.equal(reads, 0, scenario);
    if (scenario !== 'summary-cancel') assert.equal(summaryPrompts, 0, scenario);
  }
});
test('upload denial preserves fail/native JSON with bounded reason; hosted token deletion works', async () => {
  const f = fake({ open: true, inputs: ['space', 'PRIVATE_TOKEN'], dependencies: { runCLI: async () => checkResult('fail', { deepLinkReason: 'summary_publish_denied', deepLink: 'https://evil/PRIVATE_TOKEN', error: 'PRIVATE_TOKEN' }) } });
  await summarySetup(f); await f.commands.get('datatug.runCheckWithSummary')('pass');
  assert.equal(JSON.parse(f.documents[0].content).status, 'fail');
  assert.ok(f.messages.some(m => m.includes('fail') && m.includes('denied') && m.includes('preserved')));
  assert.equal(f.calls.filter(c => c[0] === 'url').length, 0); assert.ok(!JSON.stringify(f.documents).includes('PRIVATE_TOKEN'));
  await f.commands.get('datatug.deleteHostedToken')();
  assert.equal(f.secrets.size, 0); assert.equal(f.state.get('summarySpace'), 'space');
});
test('published summary never opens without explicit action; command manifest matches registration', async () => {
  const f = fake({ inputs: ['space', 'PRIVATE_TOKEN'], dependencies: { runCLI: async () => checkResult('pass', { deepLinkReason: 'hosted_summary_published', deepLink: summaryLink() }) } });
  assert.deepEqual([...f.commands.keys()].sort(), require('../package.json').contributes.commands.map(c => c.command).sort());
  await summarySetup(f); await f.commands.get('datatug.runCheckWithSummary')('pass');
  assert.equal(f.calls.filter(c => c[0] === 'url').length, 0); assert.equal(f.documents.length, 0);
});
