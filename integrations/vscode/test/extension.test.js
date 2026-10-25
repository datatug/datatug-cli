'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { createExtension } = require('../src/extension');

function fake(options = {}) {
  const commands = new Map(), messages = [], documents = [], secrets = new Map(), state = new Map(), calls = [];
  let workspaceChanged;
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
      onDidChangeWorkspaceFolders: handler => { workspaceChanged = handler; return { dispose() {} }; }
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
  return { vscode, context, extension, commands, messages, documents, secrets, state, calls, workspaceChanged: () => workspaceChanged() };
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

function awaitBarrier() {
  let enter, release;
  const reached = new Promise(resolve => { enter = resolve; });
  const pending = new Promise(resolve => { release = resolve; });
  return { reached, release, wait: async () => { enter(); return pending; } };
}
async function workspaceFixture(t) {
  const fs = require('node:fs/promises'), os = require('node:os'), path = require('node:path');
  const temporary = await fs.realpath(await fs.mkdtemp(path.join(os.tmpdir(), 'datatug-workspace-race-')));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const project = path.join(temporary, 'original', 'project');
  await fs.mkdir(project, { recursive: true });
  await fs.writeFile(path.join(project, 'datatug-project.json'), '{"id":"project"}');
  for (const name of ['remaining', 'replacement']) await fs.mkdir(path.join(temporary, name));
  const folder = name => ({ uri: { scheme: 'file', fsPath: path.join(temporary, name) } });
  const f = fake({ inputs: ['space', 'PRIVATE_TOKEN'], dependencies: { projectPath: require('../src/boundary').projectPath } });
  f.vscode.workspace.workspaceFolders = [folder('original'), folder('remaining')];
  f.vscode.window.showOpenDialog = async () => [{ scheme: 'file', fsPath: project }];
  await summarySetup(f);
  return { f, folder, project };
}
function assertNoInvalidatedRun(f) {
  assert.equal(f.vscode.workspace.isTrusted, true, 'A trusted remaining workspace must not authorize the old selection.');
  assert.ok(f.vscode.workspace.workspaceFolders.length > 0);
  assert.equal(f.calls.filter(c => c[0] === 'run').length, 0, 'No child can receive database credentials or the hosted token.');
  assert.ok(f.messages.includes('Workspace folders changed. Select the project and environment again before running DataTug.'));
  assert.ok(f.messages.every(message => !message.includes('PRIVATE_TOKEN')));
}
test('workspace removal/replacement during summary consent or SecretStorage await invalidates the pending run', async t => {
  for (const stage of ['summary-consent', 'hosted-token']) for (const change of ['remove', 'replace']) {
    await t.test(`${stage}: ${change} selected root while another trusted root remains`, async t => {
      const { f, folder } = await workspaceFixture(t), gate = awaitBarrier();
      let hostedReads = 0;
      if (stage === 'summary-consent') f.vscode.window.showWarningMessage = async (_message, _options, action) => {
        if (action === 'Run and publish summary') await gate.wait();
        return action;
      };
      f.context.secrets.get = async key => {
        assert.equal(key, 'hosted.summaryToken'); hostedReads++;
        if (stage === 'hosted-token') await gate.wait();
        return 'PRIVATE_TOKEN';
      };
      const pending = f.commands.get('datatug.runCheckWithSummary')('pass');
      await gate.reached;
      f.vscode.workspace.workspaceFolders = change === 'remove' ? [folder('remaining')] : [folder('replacement'), folder('remaining')];
      f.workspaceChanged(); gate.release(); await pending;
      assertNoInvalidatedRun(f);
      assert.equal(hostedReads, stage === 'summary-consent' ? 0 : 1);
      assert.deepEqual(await f.extension.tree.getChildren(), []);
    });
  }
});
test('workspace epoch invalidates pending query/default Check consent even when the original root returns', async t => {
  for (const command of ['runQuery', 'runCheck']) await t.test(command, async t => {
    const { f, folder } = await workspaceFixture(t), gate = awaitBarrier();
    f.vscode.window.showWarningMessage = async (_message, _options, action) => { await gate.wait(); return action; };
    const pending = f.commands.get(`datatug.${command}`)(command === 'runQuery' ? 'items' : 'pass');
    await gate.reached;
    f.vscode.workspace.workspaceFolders = [folder('remaining')]; f.workspaceChanged();
    f.vscode.workspace.workspaceFolders = [folder('original'), folder('remaining')]; f.workspaceChanged();
    gate.release(); await pending;
    assertNoInvalidatedRun(f);
  });
});
test('every execution revalidates its captured project after async setup and guards delayed progress before spawn', async t => {
  for (const command of ['runQuery', 'runCheck', 'runCheckWithSummary']) await t.test(command, async t => {
    const { f, folder, project } = await workspaceFixture(t), gate = awaitBarrier(), stages = [];
    const realPath = require('../src/boundary').projectPath;
    // Dependencies are fixed at activation, so use a second instance to observe
    // the real filesystem boundary after executable/evidence setup completes.
    const g = fake({ inputs: ['space', 'PRIVATE_TOKEN'], dependencies: {
      projectPath: async (...args) => { stages.push('project'); return realPath(...args); },
      executable: async () => { stages.push('executable'); return '/trusted/datatug'; },
      evidenceDirectory: async () => { stages.push('evidence'); return '/private/storage/check-abc'; }
    } });
    g.vscode.workspace.workspaceFolders = [...f.vscode.workspace.workspaceFolders];
    g.vscode.window.showOpenDialog = async () => [{ scheme: 'file', fsPath: project }];
    await summarySetup(g); stages.length = 0;
    g.vscode.window.withProgress = async (_options, action) => { await gate.wait(); return action({}, { isCancellationRequested: false, onCancellationRequested: () => ({ dispose() {} }) }); };
    const pending = g.commands.get(`datatug.${command}`)(command === 'runQuery' ? 'items' : 'pass');
    await gate.reached;
    assert.equal(stages.at(-1), 'project');
    assert.ok(stages.lastIndexOf('project') > stages.lastIndexOf(command === 'runQuery' ? 'executable' : 'evidence'));
    g.vscode.workspace.workspaceFolders = [folder('remaining')]; g.workspaceChanged();
    gate.release(); await pending;
    assertNoInvalidatedRun(g);
  });
});
test('workspace changes still cancel an already registered running child', async () => {
  const gate = awaitBarrier(); let executionToken;
  const f = fake({ dependencies: { runCLI: async (_file, _args, options) => { executionToken = options.token; await gate.wait(); return checkResult(); } } });
  await f.commands.get('datatug.selectProject')();
  const pending = f.commands.get('datatug.runCheck')('pass');
  await gate.reached;
  f.vscode.workspace.workspaceFolders = [{ uri: { scheme: 'file', fsPath: '/remaining' } }]; f.workspaceChanged();
  assert.equal(executionToken.isCancellationRequested, true);
  gate.release(); await pending;
});
