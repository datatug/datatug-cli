'use strict';
// Test harness only. Never imported by packaged extension source.
const fs = require('node:fs/promises');
const path = require('node:path');
const { createHash } = require('node:crypto');
const digest = value => createHash('sha256').update(value).digest('hex');
const inside = (root, target) => target.startsWith(root + path.sep);
const phases = Object.freeze({
  pass: { check: 'pass', token: 'original', publish: true, status: 'pass' },
  fail: { check: 'fail', token: 'original', publish: true, status: 'fail' },
  foreign: { check: 'pass', token: 'foreign', publish: true, status: 'pass' },
  revoked: { check: 'pass', token: 'original', publish: true, status: 'pass' },
  version: { check: 'pass', token: 'original', publish: true, status: 'pass' },
  local: { check: 'pass', publish: false, status: 'pass' }
});
function requireSafe(ok) { if (!ok) throw new Error('Acceptance boundary refused'); }
async function fileMap(root) {
  const result = {};
  async function visit(relative) {
    for (const entry of (await fs.readdir(path.join(root, relative), { withFileTypes: true })).sort((a,b) => a.name.localeCompare(b.name))) {
      requireSafe(!entry.isSymbolicLink());
      const item = path.join(relative, entry.name); requireSafe(item.split(path.sep).length <= 8 && Object.keys(result).length < 128);
      if (entry.isDirectory()) await visit(item);
      else { requireSafe(entry.isFile() && (await fs.stat(path.join(root, item))).size <= 1024 * 1024); result[item] = digest(await fs.readFile(path.join(root, item))); }
    }
  }
  await visit(''); return result;
}
async function verifyActivation(context, plan, candidate, profile) {
  requireSafe(context?.extension?.id === 'datatug.datatug-everywhere');
  requireSafe(await fs.realpath(context.extension.extensionPath) === candidate && await fs.realpath(candidate) === candidate);
  const storage = path.resolve(context.globalStorageUri.fsPath);
  requireSafe(storage === path.join(profile, 'User', 'globalStorage', 'datatug.datatug-everywhere'));
  requireSafe(await fs.realpath(profile) === profile && context.extension.packageJSON.main === './test-bootstrap.js' && digest(await fs.readFile(path.join(candidate, 'package.json'))) === plan.candidateManifestHash);
  for (const [name, expected] of Object.entries(plan.packagedSources)) requireSafe(digest(await fs.readFile(path.join(candidate, name))) === expected);
  await fs.mkdir(storage, { recursive: true, mode: 0o700 });
  requireSafe(await fs.realpath(storage) === storage);
  return storage;
}
async function validateLaunch(plan, state, file, args, options, storage) {
  const phase = phases[state.phase]; requireSafe(!!phase && state.handshake === true);
  fixtureURL(plan.api, '/v0/');
  requireSafe(file === plan.cli && await fs.realpath(file) === file && digest(await fs.readFile(file)) === plan.cliSHA256);
  requireSafe(await fs.realpath(plan.project) === plan.project && options.cwd === plan.project);
  const actual = await fileMap(plan.project), expected = plan.projectMaps[state.phase === 'version' ? 'version' : 'normal'];
  requireSafe(Object.keys(actual).sort().join('\0') === Object.keys(expected).sort().join('\0'));
  requireSafe(Object.keys(actual).every(key => actual[key] === expected[key]));
  const evidence = args[10];
  requireSafe(typeof evidence === 'string' && await fs.realpath(evidence) === evidence && inside(storage, evidence) && !inside(plan.project, evidence) && (await fs.stat(evidence)).mode % 512 === 0o700);
  const expectedArgs = ['checks', 'run', '--project', plan.project, '--env', 'demo', '--check', phase.check, '--local-owner', '--evidence-dir', evidence, '--timeout', '30s', '--warnings', 'fail', '--format', 'json'];
  if (phase.publish) expectedArgs.push('--publish-summary', '--summary-space', plan.scope.spaceID, '--summary-token-env', 'DATATUG_SUMMARY_TOKEN');
  requireSafe(JSON.stringify(args) === JSON.stringify(expectedArgs));
  requireSafe(Object.keys(options).every(key => ['cwd', 'token', 'secret', 'summaryToken', 'timeoutMs'].includes(key)) && options.secret === undefined);
  if (phase.publish) requireSafe(typeof options.summaryToken === 'string' && digest(options.summaryToken) === plan.tokenHashes[phase.token] && options.timeoutMs === 31000);
  else requireSafe(options.summaryToken === undefined && options.timeoutMs === undefined);
  return { phase, args: phase.publish ? [...args, '--summary-api-url', plan.api] : args };
}
function fixtureURL(value, pathname) {
  // Check the literal authority before URL normalization (127.1, decimal IPs,
  // omitted/default ports and encoded hosts must never become accepted aliases).
  requireSafe(typeof value === 'string' && /^http:\/\/127\.0\.0\.1:([1-9][0-9]{0,4})(?:\/|$)/.test(value));
  const endpoint = new URL(value);
  const literalPort = Number(value.match(/^http:\/\/127\.0\.0\.1:([0-9]+)/)[1]);
  requireSafe(literalPort <= 65535 && endpoint.protocol === 'http:' && endpoint.hostname === '127.0.0.1' && !endpoint.username && !endpoint.password && !endpoint.search && !endpoint.hash && endpoint.pathname === pathname);
  requireSafe(value === `http://127.0.0.1:${literalPort}${pathname}` || (pathname === '/' && value === `http://127.0.0.1:${literalPort}`));
  return endpoint;
}
async function fixtureFetch(base, basePath, pathname, options = {}, query, transport = fetch) {
  const endpoint = fixtureURL(base, basePath);
  requireSafe(['/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword', '/test/reset-events', '/test/events', '/test/revoke', '/test/restore', '/v0/datatug/shipping/check-summary'].includes(pathname));
  const target = new URL(pathname, endpoint);
  if (query) target.search = new URLSearchParams(query).toString();
  return transport(target, { ...options, redirect: 'error' });
}
const missingTokenError = 'Store a current hosted summary token in VS Code SecretStorage before this run.';
function observeNativeUI(vscode, state) {
  state.uiEvents = [];
  // Empty proxy targets also support the frozen API objects used by VS Code;
  // no native property is replaced or mutated.
  const window = new Proxy({}, { get(_target, name) {
    const target = vscode.window;
    const method = target[name];
    if (typeof method !== 'function') return method;
    if (!['showWarningMessage', 'showErrorMessage', 'showInputBox'].includes(name)) return method.bind(target);
    return async (...args) => {
      const phase = state.phase;
      const result = await Reflect.apply(method, target, args); // Forward unchanged to genuine native UI.
      if (name === 'showWarningMessage') {
        for (const [kind, action] of [['owner', 'Run with local-owner consent'], ['upload', 'Run and publish summary']]) {
          if (args[1]?.modal === true && args.slice(2).includes(action)) state.uiEvents.push({ phase, kind, accepted: result === action });
        }
      } else if (name === 'showErrorMessage' && args[0] === missingTokenError) state.uiEvents.push({ phase, kind: 'missing-token-error' });
      else if (name === 'showInputBox' && args[0]?.title === 'Store hosted summary token in SecretStorage') state.uiEvents.push({ phase, kind: 'token-input', hash: typeof result === 'string' ? digest(result) : null });
      return result;
    };
  } });
  return new Proxy({}, { get(_target, name) { return name === 'window' ? window : vscode[name]; } });
}
function requireMissingTokenOutcome(events) {
  requireSafe(events.length === 3 && events[0].kind === 'owner' && events[0].accepted === true && events[1].kind === 'upload' && events[1].accepted === true && events[2].kind === 'missing-token-error' && events.every(event => event.phase === 'missing'));
}
function requireFreshResult(document, previousDocuments, actual, phase, launchCount, previousLaunchCount) {
  requireSafe(launchCount === previousLaunchCount + 1 && actual?.phase === phase && document?.isUntitled && document.languageId === 'json' && !previousDocuments.has(document));
  requireSafe(digest(document.getText()) === actual.contentHash);
}
async function storeNativeToken(vscode, state, expectedHash) {
  const before = state.secretChanges, inputsBefore = state.uiEvents.length;
  await vscode.commands.executeCommand('datatug.storeHostedToken');
  const inputs = state.uiEvents.slice(inputsBefore).filter(event => event.kind === 'token-input');
  requireSafe(inputs.length === 1 && inputs[0].hash === expectedHash && state.secretChanges > before);
}
module.exports = { digest, fileMap, verifyActivation, validateLaunch, requireSafe, phases, fixtureURL, fixtureFetch, observeNativeUI, requireMissingTokenOutcome, requireFreshResult, storeNativeToken, missingTokenError };
