'use strict';
const fs = require('node:fs');
const { spawnSync } = require('node:child_process');
fs.mkdirSync('dist', { recursive: true });
const cli = require.resolve('@vscode/vsce/vsce');
const result = spawnSync(process.execPath, [cli, 'package', '--no-dependencies', '--out', 'dist/datatug-everywhere-0.1.0.vsix'], { stdio: 'inherit', env: { ...process.env, VSCE_PAT: '' } });
process.exit(result.status || (result.error ? 1 : 0));
