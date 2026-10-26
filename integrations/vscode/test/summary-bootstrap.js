'use strict';
// This file is copied only into a disposable candidate; package main never points here.
const fs = require('node:fs/promises');
const path = require('node:path');
const vscode = require('vscode');
const guard = require('./test-guard');
let activated = false;
exports.activate = async context => {
  guard.requireSafe(!activated); activated = true;
  const root = process.env.DATATUG_VSCODE_SUMMARY_ROOT;
  guard.requireSafe(typeof root === 'string' && root.startsWith('/private/tmp/datatug-vscode-summary-acceptance-'));
  const plan = JSON.parse(await fs.readFile(path.join(root, 'plan.json'), 'utf8'));
  const launch = JSON.parse(await fs.readFile(path.join(root, 'launch.json'), 'utf8'));
  guard.requireSafe(launch.nonce === process.env.DATATUG_VSCODE_SUMMARY_NONCE && launch.candidate === context.extension.extensionPath && !vscode.env.remoteName && vscode.version === '1.142.0-insider');
  const storage = await guard.verifyActivation(context, plan, launch.candidate, plan.profile);
  const marker = path.join(root, `activated-${launch.nonce}.json`);
  const state = { handshake: true, phase: '', launches: [], secretChanges: 0, storageHash: guard.digest(storage), candidateContextID: context.extension.id };
  await fs.writeFile(marker, JSON.stringify({ nonce: launch.nonce, candidateContextID: context.extension.id, storageHash: state.storageHash, actualContext: true }), { mode: 0o600, flag: 'wx' });
  context.subscriptions.push(context.secrets.onDidChange(event => { if (event.key === 'hosted.summaryToken') state.secretChanges++; }));
  const boundary = require('./src/boundary');
  const { createExtension } = require('./src/extension');
  const runCLI = async (file, args, options) => {
    try {
      const admitted = await guard.validateLaunch(plan, state, file, args, options, storage);
      const result = await boundary.runCLI(file, admitted.args, options);
      const data = JSON.parse(result.stdout);
      const parsed = boundary.parseResult('check', result, admitted.phase.publish ? plan.scope : undefined);
      guard.requireSafe(parsed.status === admitted.phase.status);
      const uri = data.deepLinkReason === 'hosted_summary_published' ? boundary.safeDeepLink(data.deepLink, plan.scope) : undefined;
      if (data.deepLinkReason === 'hosted_summary_published') guard.requireSafe(!!uri);
      state.launches.push({ phase: state.phase, code: result.code, localStatus: parsed.status, contentHash: guard.digest(parsed.content), reason: data.deepLinkReason, uri: uri || null, tokenHashMatched: admitted.phase.publish, actualCLI: true });
      await fs.writeFile(path.join(root, 'launch-receipts.json'), JSON.stringify(state.launches, null, 2), { mode: 0o600 });
      return result; // Exact real process result, never a simulated outcome.
    } catch {
      state.blocked = true;
      throw new boundary.SafeError('Isolated acceptance launch refused. No production fallback is permitted.');
    }
  };
  const api = createExtension(guard.observeNativeUI(vscode, state), context, { runCLI });
  await vscode.commands.executeCommand('setContext', 'datatug.summaryAcceptanceReady', true);
  return { ...api, acceptance: state };
};
