'use strict';
const fs = require('node:fs');
const { spawnSync } = require('node:child_process');

function packageVSIX({ fileSystem = fs, spawn = spawnSync, resolve = require.resolve } = {}) {
  const target = 'dist/datatug-everywhere-0.1.0.vsix';
  fileSystem.mkdirSync('dist', { recursive: true });
  // An earlier package must never stand in for this invocation's output.
  fileSystem.rmSync(target, { force: true });
  try {
    const cli = resolve('@vscode/vsce/vsce');
    const result = spawn(process.execPath, [cli, 'package', '--no-dependencies', '--out', target], { stdio: 'inherit', env: { ...process.env, VSCE_PAT: '' } });
    if (result.status !== 0 || result.error || result.signal) {
      fileSystem.rmSync(target, { force: true });
      return Number.isInteger(result.status) && result.status > 0 ? result.status : 1;
    }
    const output = fileSystem.lstatSync(target);
    if (!output.isFile() || output.size === 0) {
      fileSystem.rmSync(target, { force: true });
      return 1;
    }
    return 0;
  } catch {
    fileSystem.rmSync(target, { force: true });
    return 1;
  }
}

if (require.main === module) process.exit(packageVSIX());
module.exports = { packageVSIX };
