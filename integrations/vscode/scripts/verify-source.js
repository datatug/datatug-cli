'use strict';
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
for (const dir of ['src', 'scripts', 'test']) for (const name of fs.readdirSync(dir).filter(n => n.endsWith('.js'))) {
  const result = spawnSync(process.execPath, ['--check', path.join(dir, name)], { stdio: 'inherit' });
  if (result.status !== 0) process.exit(result.status || 1);
}
const manifest = require('../package.json');
if (manifest.dependencies || manifest.extensionKind.join() !== 'workspace' || manifest.contributes.configuration.properties['datatug.cliPath'].scope !== 'machine') throw new Error('Unexpected runtime dependencies/host configuration');
console.log('Source syntax and manifest boundary verified; no runtime dependencies.');
