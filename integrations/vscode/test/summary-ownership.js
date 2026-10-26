'use strict';
// Disposable host resources only; never packaged or used by production code.
const fs = require('node:fs/promises');
async function ownDirectory(owned, folder) {
  await fs.mkdir(folder, { mode: 0o700 }); // Existing paths are never adopted.
  owned.add(folder);
}
async function ownFile(owned, file, content) {
  const handle = await fs.open(file, 'wx', 0o600);
  owned.add(file);
  try { await handle.writeFile(content); } finally { await handle.close(); }
}
function joinChild(child) {
  // Subscribe at spawn time, including spawn errors: close follows error.
  return new Promise(resolve => child.once('close', (code, signal) => resolve({ code, signal })));
}
async function cleanup(child, closed, owned, graceMs = 2000) {
  if (child && closed) {
    let timer;
    if (child.exitCode === null && child.signalCode === null) {
      child.kill('SIGTERM');
      timer = setTimeout(() => { if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL'); }, graceMs);
    }
    try { await closed; } finally { clearTimeout(timer); }
  }
  // Attempt every owned path even if an earlier removal fails.
  const results = await Promise.allSettled([...owned].map(item => fs.rm(item, { recursive: true, force: true })));
  const failure = results.find(result => result.status === 'rejected');
  if (failure) throw failure.reason;
}
module.exports = { ownDirectory, ownFile, joinChild, cleanup };
