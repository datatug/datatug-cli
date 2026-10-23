import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { transformSync } from 'esbuild';
test('embedded module parses intact after SDK replacement-token strings are inlined', () => {
 const html = readFileSync(new URL('../index.html',import.meta.url),'utf8');
 const start = html.indexOf('<script type="module">') + '<script type="module">'.length;
 const end = html.lastIndexOf('</script>');
 assert.ok(start > 0 && end > start);
 assert.doesNotThrow(() => transformSync(html.slice(start,end),{format:'esm',loader:'js',target:'es2022'}));
 assert.equal(html.slice(start,end).includes('<!doctype html>'),false);
});
