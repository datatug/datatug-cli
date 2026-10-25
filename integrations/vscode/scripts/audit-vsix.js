'use strict';
const { execFileSync } = require('node:child_process');
const { createHash } = require('node:crypto');
const fs = require('node:fs');
const target = process.argv[2] || 'dist/datatug-everywhere-0.1.0.vsix';
const entries = execFileSync('unzip', ['-Z1', target], { encoding: 'utf8' }).trim().split('\n');
const allowed = new Set(['[Content_Types].xml', 'extension.vsixmanifest', 'extension/package.json', 'extension/readme.md', 'extension/SECURITY.md', 'extension/changelog.md', 'extension/LICENSE.txt', 'extension/src/extension.js', 'extension/src/boundary.js']);
for (const entry of entries) {
  if (!allowed.has(entry)) throw new Error(`Unexpected VSIX path: ${entry}`);
  const data = execFileSync('unzip', ['-p', target, entry.replaceAll('[', '[[]')], { maxBuffer: 2 * 1024 * 1024 });
  if (/ag[-_]?grid|ag[-_]?charts|@ag-grid|@ag-charts|cdn\.[^\s]+\.(js|css)/i.test(data.toString('utf8'))) throw new Error(`Restricted asset reference: ${entry}`);
}
for (const entry of allowed) if (!entries.includes(entry)) throw new Error(`Missing package file: ${entry}`);
const manifest = JSON.parse(execFileSync('unzip', ['-p', target, 'extension/package.json'], { encoding: 'utf8' }));
if (manifest.dependencies) throw new Error('Unexpected runtime dependencies');
console.log(JSON.stringify({ files: entries.length, runtimeDependencies: 0, restrictedAssets: 0, sha256: createHash('sha256').update(fs.readFileSync(target)).digest('hex') }));
