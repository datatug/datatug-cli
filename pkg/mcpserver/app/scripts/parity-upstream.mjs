// Execute shared browser vectors against both the exact Git-pinned upstream module and vendor.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import assert from 'node:assert/strict';
import * as vendored from '../vendor/rows.js';
const manifest = JSON.parse(readFileSync(new URL('../vendor/upstream.json', import.meta.url)));
const source = process.argv[2];
if (!source) throw Error('Usage: node scripts/parity-upstream.mjs /path/to/ai-plugin-clone');
const bytes = execFileSync('git', ['-C', source, 'show', `${manifest.commit}:lib/rows.js`]);
assert.equal(createHash('sha256').update(bytes).digest('hex'), manifest.files[0].sha256);
const upstream = await import(`data:text/javascript;base64,${bytes.toString('base64')}`);
const fixture = JSON.parse(readFileSync(new URL('../test/typed-result.json', import.meta.url)))._meta.datatug.result.recordset;
const rows = fixture.rows.map(row => row.map(c => c.type === 'null' ? null : String(c.value)));
const numeric = [['9007199254740992.2'], ['9007199254740992.1'], ['9007199254740992.10'], [null], ['1e1000000'], ['-1e1000000'], ['1e-20'], ['0.00000000000000000001'], [''], ['false']];
for (const data of [rows, numeric]) for (const col of [0, 1]) for (const desc of [true, false]) assert.deepEqual(vendored.sortedOrder(data, col, true, desc), upstream.sortedOrder(data, col, true, desc));
const names = fixture.columns.map(c => c.name), types = fixture.columns.map(c => c.type);
for (const method of ['toCsv', 'toJson']) {
  const args = method === 'toCsv' ? [names, rows] : [names, types, rows];
  assert.equal(vendored[method](...args), upstream[method](...args));
}
console.log(`Exact upstream ${manifest.commit} SHA256 ${manifest.files[0].sha256}; tagged DTO + exponent/scales/ties/null/copy parity passed`);
