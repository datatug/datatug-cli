'use strict';
const { execFileSync } = require('node:child_process');
const { createHash } = require('node:crypto');
const fs = require('node:fs');
const allowed = new Set(['[Content_Types].xml', 'extension.vsixmanifest', 'extension/package.json', 'extension/readme.md', 'extension/SECURITY.md', 'extension/changelog.md', 'extension/LICENSE.txt', 'extension/src/extension.js', 'extension/src/boundary.js']);

function auditVSIX(target, { execute = execFileSync, fileSystem = fs } = {}) {
  const entries = execute('unzip', ['-Z1', target], { encoding: 'utf8' }).trim().split('\n');
  if (new Set(entries).size !== entries.length) throw new Error('Duplicate VSIX path');
  if (entries.length !== allowed.size) throw new Error('Unexpected VSIX file count');
  for (const entry of entries) if (!allowed.has(entry)) throw new Error(`Unexpected VSIX path: ${entry}`);
  for (const entry of allowed) if (!entries.includes(entry)) throw new Error(`Missing package file: ${entry}`);
  for (const entry of entries) {
    const data = execute('unzip', ['-p', target, entry.replaceAll('[', '[[]')], { maxBuffer: 2 * 1024 * 1024 });
    if (/ag[-_]?grid|ag[-_]?charts|@ag-grid|@ag-charts|cdn\.[^\s]+\.(js|css)/i.test(data.toString('utf8'))) throw new Error(`Restricted asset reference: ${entry}`);
  }
  const manifest = JSON.parse(execute('unzip', ['-p', target, 'extension/package.json'], { encoding: 'utf8' }));
  if (manifest.dependencies) throw new Error('Unexpected runtime dependencies');
  return { files: entries.length, runtimeDependencies: 0, restrictedAssets: 0, sha256: createHash('sha256').update(fileSystem.readFileSync(target)).digest('hex') };
}

if (require.main === module) console.log(JSON.stringify(auditVSIX(process.argv[2] || 'dist/datatug-everywhere-0.1.0.vsix')));
module.exports = { auditVSIX };
