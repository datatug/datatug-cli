'use strict';

const path = require('node:path');
const { randomUUID } = require('node:crypto');
const boundary = require('./boundary');

function createExtension(vscode, context, dependencies = {}) {
  const api = { ...boundary, ...dependencies };
  let selected, environment, listing, busy = false;
  const running = new Set();
  const changed = new vscode.EventEmitter();
  const roots = () => (vscode.workspace.workspaceFolders || []).filter(f => f.uri.scheme === 'file').map(f => f.uri.fsPath);
  function trust() {
    if (!vscode.workspace.isTrusted) throw new api.SafeError('Trust this workspace before reading project files or running DataTug.');
    if (!roots().length) throw new api.SafeError('Open a local or remote filesystem workspace folder first.');
  }
  const tree = {
    onDidChangeTreeData: changed.event,
    getTreeItem: item => item,
    getChildren: async item => {
      if (!vscode.workspace.isTrusted || !selected || !listing) return [];
      if (!item) return [
        { label: `Environment: ${environment || 'select explicitly'}`, command: { command: 'datatug.selectEnvironment', title: 'Select environment' } },
        { label: 'Saved queries', kind: 'queries', collapsibleState: vscode.TreeItemCollapsibleState.Collapsed },
        { label: 'Checks', kind: 'checks', collapsibleState: vscode.TreeItemCollapsibleState.Collapsed }
      ];
      return (listing[item.kind] || []).map(entry => ({ label: entry.title, description: api.label(entry.id), command: { command: item.kind === 'queries' ? 'datatug.runQuery' : 'datatug.runCheck', title: 'Run selection', arguments: [entry.id] } }));
    }
  };
  async function refresh() {
    trust();
    if (!selected) return;
    selected = await api.projectPath(selected, roots());
    listing = await api.catalog(selected);
    if (!listing.environments.includes(environment)) environment = undefined;
    changed.fire();
  }
  async function selectProject() {
    trust();
    const choice = await vscode.window.showOpenDialog({ canSelectFolders: true, canSelectFiles: false, canSelectMany: false, openLabel: 'Select DataTug project', defaultUri: vscode.workspace.workspaceFolders[0].uri });
    if (!choice?.length) return;
    trust();
    if (choice[0].scheme !== 'file') throw new api.SafeError('Select a project on this extension host filesystem.');
    const candidate = await api.projectPath(choice[0].fsPath, roots());
    const next = await api.catalog(candidate);
    selected = candidate; listing = next; environment = undefined; changed.fire();
    await selectEnvironment();
  }
  async function selectEnvironment() {
    trust();
    if (!selected) return selectProject();
    await refresh();
    const value = await vscode.window.showQuickPick(listing.environments, { title: 'Select the execution environment', placeHolder: 'An explicit selection is required, including for a single environment' });
    if (value) { environment = value; changed.fire(); }
  }
  function profiles() {
    return context.globalState.get('profiles', []).filter(p => typeof p?.id === 'string' && /^[a-f0-9-]{36}$/.test(p.id) && typeof p.name === 'string' && api.secretVariable(p.variable));
  }
  async function addProfile() {
    trust();
    const name = await vscode.window.showInputBox({ title: 'Connection profile name', prompt: 'A local label only; do not include credentials', validateInput: v => /^[A-Za-z0-9 _-]{1,60}$/.test(v) ? undefined : 'Use 1–60 letters, numbers, spaces, underscores or hyphens.' });
    if (!name) return;
    const variable = await vscode.window.showInputBox({ title: 'CLI credential environment variable', prompt: 'Match the existing connection descriptor dsnEnv (for example SHOP_PG_URL), or use PGPASSWORD', validateInput: v => api.secretVariable(v) ? undefined : 'Use PGPASSWORD or an uppercase connection variable ending _URL. Runtime/control variables are refused.' });
    if (!variable) return;
    const value = await vscode.window.showInputBox({ title: 'Store connection credential in VS Code SecretStorage', prompt: 'This value is sent to the execution host CLI environment only when you explicitly select this profile.', password: true, ignoreFocusOut: true });
    if (!value) return;
    trust();
    const profile = { id: randomUUID(), name, variable };
    await context.secrets.store(`connection.${profile.id}`, value);
    try { await context.globalState.update('profiles', [...profiles(), profile]); }
    catch (error) { await context.secrets.delete(`connection.${profile.id}`); throw error; }
    await vscode.window.showInformationMessage('Connection profile stored in VS Code SecretStorage for explicit use on this execution host.');
  }
  async function deleteProfile() {
    trust();
    const profile = await vscode.window.showQuickPick(profiles().map(p => ({ label: p.name, profile: p })), { title: 'Delete a connection profile and its secret' });
    if (!profile) return;
    await context.secrets.delete(`connection.${profile.profile.id}`);
    await context.globalState.update('profiles', profiles().filter(p => p.id !== profile.profile.id));
  }
  async function chooseSecret() {
    const options = [{ label: 'Use existing host configuration', description: 'No extension-stored secret supplied' }, ...profiles().map(profile => ({ label: profile.name, description: profile.variable, profile }))];
    const chosen = await vscode.window.showQuickPick(options, { title: 'Select connection credentials for this execution' });
    if (!chosen) return { cancelled: true };
    if (!chosen.profile) return {};
    const value = await context.secrets.get(`connection.${chosen.profile.id}`);
    if (!value) throw new api.SafeError('Connection profile secret is missing. Store a new profile on this extension host.');
    return { secret: { variable: chosen.profile.variable, value } };
  }
  async function execute(kind, requestedID) {
    trust();
    if (busy) throw new api.SafeError('Another DataTug execution is active. Cancel or finish it first.');
    busy = true;
    try {
      if (!selected) await selectProject();
      if (!selected) return;
      await refresh();
      if (!environment) await selectEnvironment();
      if (!environment) return;
      const entries = listing[kind === 'query' ? 'queries' : 'checks'];
      const chosen = requestedID ? entries.find(q => q.id === requestedID) : (await vscode.window.showQuickPick(entries.map(entry => ({ label: entry.title, description: entry.id, entry })), { title: `Select ${kind === 'query' ? 'saved query' : 'Check'}` }))?.entry;
      if (!chosen) return;
      if (kind === 'check' && chosen.hasInputs) throw new api.SafeError('This preview supports Checks without input declarations only.');
      const runProject = selected, runEnvironment = environment;
      const secret = await chooseSecret();
      if (secret.cancelled) return;
      const admitted = await vscode.window.showWarningMessage(
        `Run the selected ${kind} in environment ${api.label(runEnvironment)}? You authorize local-owner execution if access policies are absent. Workspace trust alone does not authorize this. Existing policies are preserved and enforced; secured Check profiles are refused. Customer data stays on the execution host.`,
        { modal: true }, 'Run with local-owner consent');
      if (admitted !== 'Run with local-owner consent') return;
      trust();
      await api.projectPath(runProject, roots());
      const file = await api.executable(vscode.workspace.getConfiguration('datatug'), roots());
      const evidence = kind === 'check' ? await api.evidenceDirectory(context.globalStorageUri.fsPath, roots()) : undefined;
      const args = api.argumentsFor(kind, runProject, runEnvironment, chosen.id, evidence);
      const parsed = await vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: `DataTug ${kind}`, cancellable: true }, async (_progress, token) => {
        const cancellation = new vscode.CancellationTokenSource();
        running.add(cancellation);
        const link = token.onCancellationRequested(() => cancellation.cancel());
        if (token.isCancellationRequested) cancellation.cancel();
        try { return api.parseResult(kind, await api.runCLI(file, args, { cwd: runProject, token: cancellation.token, secret: secret.secret })); }
        finally { link.dispose(); running.delete(cancellation); cancellation.dispose(); }
      });
      // No raw subprocess text, project titles, argv, rows or errors enter notifications/logs.
      const open = await vscode.window.showInformationMessage(`DataTug ${kind}: ${parsed.status}. Results may contain private data.`, 'Open JSON results');
      if (open === 'Open JSON results') {
        const document = await vscode.workspace.openTextDocument({ language: 'json', content: parsed.content });
        await vscode.window.showTextDocument(document, { preview: true });
      }
    } finally { busy = false; }
  }
  async function openHosted() {
    const { url, reason } = api.hostedLink();
    if (await vscode.window.showInformationMessage(reason, 'Open hosted home') === 'Open hosted home') await vscode.env.openExternal(vscode.Uri.parse(url));
  }
  const commands = {
    selectProject, selectEnvironment, refresh,
    runQuery: id => execute('query', id), runCheck: id => execute('check', id),
    addProfile, deleteProfile, openHosted
  };
  for (const [name, action] of Object.entries(commands)) context.subscriptions.push(vscode.commands.registerCommand(`datatug.${name}`, async (...args) => {
    try { return await action(...args); }
    catch (error) { await vscode.window.showErrorMessage(error instanceof api.SafeError ? error.message : 'DataTug could not complete this operation. Verify the project files, CLI installation and host permissions. Raw errors are withheld to protect credentials.'); }
  }));
  context.subscriptions.push(changed, vscode.window.registerTreeDataProvider('datatug.projects', tree), { dispose() { for (const cancellation of running) cancellation.cancel(); } });
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => {
    for (const cancellation of running) cancellation.cancel();
    selected = undefined; listing = undefined; environment = undefined; changed.fire();
  }));
  return { tree };
}

function activate(context) { return createExtension(require('vscode'), context); }
module.exports = { activate, createExtension };
