'use strict';

const path = require('node:path');
const { randomUUID } = require('node:crypto');
const boundary = require('./boundary');

function createExtension(vscode, context, dependencies = {}) {
  const api = { ...boundary, ...dependencies };
  let selected, environment, listing, busy = false, workspaceEpoch = 0;
  const running = new Set();
  const changed = new vscode.EventEmitter();
  const roots = () => (vscode.workspace.workspaceFolders || []).filter(f => f.uri.scheme === 'file').map(f => f.uri.fsPath);
  function trust() {
    if (!vscode.workspace.isTrusted) throw new api.SafeError('Trust this workspace before reading project files or running DataTug.');
    if (!roots().length) throw new api.SafeError('Open a local or remote filesystem workspace folder first.');
  }
  function workspaceUnchanged(epoch) {
    if (epoch !== workspaceEpoch) throw new api.SafeError('Workspace folders changed. Select the project and environment again before running DataTug.');
    trust();
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
    const epoch = workspaceEpoch, current = selected;
    if (!current) return;
    const project = await api.projectPath(current, roots());
    workspaceUnchanged(epoch);
    const next = await api.catalog(project);
    workspaceUnchanged(epoch);
    selected = project; listing = next;
    if (!listing.environments.includes(environment)) environment = undefined;
    changed.fire();
  }
  async function selectProject() {
    trust();
    const epoch = workspaceEpoch;
    const choice = await vscode.window.showOpenDialog({ canSelectFolders: true, canSelectFiles: false, canSelectMany: false, openLabel: 'Select DataTug project', defaultUri: vscode.workspace.workspaceFolders[0].uri });
    workspaceUnchanged(epoch);
    if (!choice?.length) return;
    if (choice[0].scheme !== 'file') throw new api.SafeError('Select a project on this extension host filesystem.');
    const candidate = await api.projectPath(choice[0].fsPath, roots());
    workspaceUnchanged(epoch);
    const next = await api.catalog(candidate);
    workspaceUnchanged(epoch);
    selected = candidate; listing = next; environment = undefined; changed.fire();
    await selectEnvironment();
  }
  async function selectEnvironment() {
    trust();
    const epoch = workspaceEpoch;
    if (!selected) return selectProject();
    await refresh();
    workspaceUnchanged(epoch);
    const value = await vscode.window.showQuickPick(listing.environments, { title: 'Select the execution environment', placeHolder: 'An explicit selection is required, including for a single environment' });
    workspaceUnchanged(epoch);
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
  const hostedTokenKey = 'hosted.summaryToken';
  async function setSummarySpace() {
    trust();
    const value = await vscode.window.showInputBox({ title: 'Bind hosted summary Space', prompt: 'Enter the explicit hosted Space ID. This local binding does not grant hosted access.', validateInput: v => api.summaryIdentifier(v) ? undefined : 'Use 1–128 letters, numbers, underscores or hyphens.' });
    if (!value) return;
    trust();
    if (!api.summaryIdentifier(value)) throw new api.SafeError('Invalid hosted Space ID.');
    await context.globalState.update('summarySpace', value);
  }
  async function storeHostedToken() {
    trust();
    const value = await vscode.window.showInputBox({ title: 'Store hosted summary token in SecretStorage', prompt: 'Use a current hosted bearer token. Hosted sign-in and the current grant remain independent in DataTug.app. The token is supplied only for an explicitly consented summary run.', password: true, ignoreFocusOut: true, validateInput: v => api.summaryToken(v) ? undefined : 'Enter a bearer token without spaces or control characters (maximum 8192 characters).' });
    if (!value) return;
    trust();
    if (!api.summaryToken(value)) throw new api.SafeError('Invalid hosted token.');
    await context.secrets.store(hostedTokenKey, value);
    await vscode.window.showInformationMessage('Hosted token stored in VS Code SecretStorage. Each summary run requires separate consent.');
  }
  async function deleteHostedToken() {
    trust();
    await context.secrets.delete(hostedTokenKey);
    await vscode.window.showInformationMessage('Hosted summary token deleted from VS Code SecretStorage on this execution host.');
  }
  async function execute(kind, requestedID, publishSummary = false) {
    trust();
    if (busy) throw new api.SafeError('Another DataTug execution is active. Cancel or finish it first.');
    busy = true;
    const executionEpoch = workspaceEpoch;
    try {
      if (!selected) await selectProject();
      workspaceUnchanged(executionEpoch);
      if (!selected) return;
      await refresh();
      workspaceUnchanged(executionEpoch);
      if (!environment) await selectEnvironment();
      workspaceUnchanged(executionEpoch);
      if (!environment) return;
      const entries = listing[kind === 'query' ? 'queries' : 'checks'];
      const chosen = requestedID ? entries.find(q => q.id === requestedID) : (await vscode.window.showQuickPick(entries.map(entry => ({ label: entry.title, description: entry.id, entry })), { title: `Select ${kind === 'query' ? 'saved query' : 'Check'}` }))?.entry;
      workspaceUnchanged(executionEpoch);
      if (!chosen) return;
      if (kind === 'check' && chosen.hasInputs) throw new api.SafeError('This preview supports Checks without input declarations only.');
      const runProject = selected, runEnvironment = environment;
      const secret = await chooseSecret();
      workspaceUnchanged(executionEpoch);
      if (secret.cancelled) return;
      const admitted = await vscode.window.showWarningMessage(
        `Run the selected ${kind} in environment ${api.label(runEnvironment)}? You authorize local-owner execution if access policies are absent. Workspace trust alone does not authorize this. Existing policies are preserved and enforced; secured Check profiles are refused. Source data and private evidence stay on the execution host.`,
        { modal: true }, 'Run with local-owner consent');
      workspaceUnchanged(executionEpoch);
      if (admitted !== 'Run with local-owner consent') return;
      await api.projectPath(runProject, roots());
      workspaceUnchanged(executionEpoch);
      let summaryScope, hostedToken;
      if (publishSummary) {
        const spaceID = context.globalState.get('summarySpace');
        const manifest = await api.readJSON(runProject, 'datatug-project.json');
        workspaceUnchanged(executionEpoch);
        if (!api.summaryIdentifier(spaceID) || !api.summaryIdentifier(manifest.id) || !api.summaryIdentifier(runEnvironment) || !api.summaryIdentifier(chosen.id)) throw new api.SafeError('Bind an explicit hosted Space and use compatible project, environment and Check identifiers before publishing a summary.');
        summaryScope = { spaceID, projectID: manifest.id, environment: runEnvironment };
        const consent = await vscode.window.showWarningMessage(
          `Publish a minimal Check summary to Space ${spaceID}, project ${manifest.id}, environment ${runEnvironment} for this run? Uploads project, Space, environment and Check identifiers/version, execution status, outcome, severity, duration and execution time for seven days. SQL, rows, inputs, error text and private evidence are excluded. Hosted sign-in, Space access and the current hosted grant are checked independently in DataTug.app. No automatic retries.`,
          { modal: true }, 'Run and publish summary');
        workspaceUnchanged(executionEpoch);
        if (consent !== 'Run and publish summary') return;
        hostedToken = await context.secrets.get(hostedTokenKey);
        workspaceUnchanged(executionEpoch);
        if (!api.summaryToken(hostedToken)) throw new api.SafeError('Store a current hosted summary token in VS Code SecretStorage before this run.');
      }
      workspaceUnchanged(executionEpoch);
      const file = await api.executable(vscode.workspace.getConfiguration('datatug'), roots());
      workspaceUnchanged(executionEpoch);
      const evidence = kind === 'check' ? await api.evidenceDirectory(context.globalStorageUri.fsPath, roots()) : undefined;
      workspaceUnchanged(executionEpoch);
      // Revalidate the captured project after all asynchronous setup. The epoch
      // is checked again inside the progress callback immediately before spawn.
      await api.projectPath(runProject, roots());
      workspaceUnchanged(executionEpoch);
      const args = api.argumentsFor(kind, runProject, runEnvironment, chosen.id, evidence, summaryScope);
      const parsed = await vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title: `DataTug ${kind}`, cancellable: true }, async (_progress, token) => {
        const cancellation = new vscode.CancellationTokenSource();
        running.add(cancellation);
        const link = token.onCancellationRequested(() => cancellation.cancel());
        if (token.isCancellationRequested) cancellation.cancel();
        try {
          workspaceUnchanged(executionEpoch);
          return api.parseResult(kind, await api.runCLI(file, args, { cwd: runProject, token: cancellation.token, secret: secret.secret, ...(publishSummary ? { summaryToken: hostedToken, timeoutMs: 31000 } : {}) }), summaryScope);
        } finally { link.dispose(); running.delete(cancellation); cancellation.dispose(); }
      });
      // No raw subprocess text, project titles, argv, rows or errors enter notifications/logs.
      const summaryNotice = parsed.summary?.url ? ' Minimal summary published for seven days. Opening it requires independent hosted sign-in and the current hosted grant.' : parsed.summary?.reason ? ` ${parsed.summary.reason} Local Check status is preserved.` : '';
      const actions = ['Open JSON results', ...(parsed.summary?.url ? ['Open summary in DataTug.app'] : [])];
      const open = await vscode.window.showInformationMessage(`DataTug ${kind}: ${parsed.status}. Results may contain private data.${summaryNotice}`, ...actions);
      if (open === 'Open summary in DataTug.app' && parsed.summary?.url) await vscode.env.openExternal(vscode.Uri.parse(parsed.summary.url));
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
    runQuery: id => execute('query', id), runCheck: id => execute('check', id), runCheckWithSummary: id => execute('check', id, true),
    setSummarySpace, storeHostedToken, deleteHostedToken,
    addProfile, deleteProfile, openHosted
  };
  for (const [name, action] of Object.entries(commands)) context.subscriptions.push(vscode.commands.registerCommand(`datatug.${name}`, async (...args) => {
    try { return await action(...args); }
    catch (error) { await vscode.window.showErrorMessage(error instanceof api.SafeError ? error.message : 'DataTug could not complete this operation. Verify the project files, CLI installation and host permissions. Raw errors are withheld to protect credentials.'); }
  }));
  context.subscriptions.push(changed, vscode.window.registerTreeDataProvider('datatug.projects', tree), { dispose() { for (const cancellation of running) cancellation.cancel(); } });
  context.subscriptions.push(vscode.workspace.onDidChangeWorkspaceFolders(() => {
    workspaceEpoch++;
    for (const cancellation of running) cancellation.cancel();
    selected = undefined; listing = undefined; environment = undefined; changed.fire();
  }));
  return { tree };
}

function activate(context) { return createExtension(require('vscode'), context); }
module.exports = { activate, createExtension };
