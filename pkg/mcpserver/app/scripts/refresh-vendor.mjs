// Offline, exact-commit refresh. No network, checkout mutation, or independent vendor patches.
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
const base = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const manifest = JSON.parse(readFileSync(resolve(base, 'vendor/upstream.json')));
const source = process.argv[2];
if (!source) throw Error('Usage: node scripts/refresh-vendor.mjs /path/to/ai-plugin-clone');
const license = execFileSync('git', ['-C', source, 'show', `${manifest.commit}:LICENSE`]);
for (const entry of manifest.files) {
  const bytes = execFileSync('git', ['-C', source, 'show', `${manifest.commit}:${entry.path}`]);
  if (createHash('sha256').update(bytes).digest('hex') !== entry.sha256) throw Error('Upstream content does not match reviewed hash');
  writeFileSync(resolve(base, 'vendor', entry.destination), bytes);
}
writeFileSync(resolve(base, 'vendor/LICENSE'), license);
console.log(`Refreshed exact ${manifest.repository}@${manifest.commit}`);
