import { build } from 'esbuild';
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve, dirname, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
const base = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const root = resolve(base, '../../..');
const check = process.argv.includes('--check');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const upstream = JSON.parse(readFileSync(resolve(base, 'vendor/upstream.json')));
for (const file of upstream.files) {
  if (hash(readFileSync(resolve(base, 'vendor', file.destination))) !== file.sha256) throw Error('Vendor hash mismatch; refresh from reviewed upstream');
}
const lock = JSON.parse(readFileSync(resolve(base, 'package-lock.json')));
const sdk = lock.packages['node_modules/@modelcontextprotocol/ext-apps'];
if (sdk.version !== '2.0.3' || sdk.integrity !== 'sha512-Tn+4+cyhO4f4cQSDxNyLiIfEE+qlTTuIbWBJB8JTinEXOhDYgCT4wDTQplxD9MBYZH5pCYGC59GaxiwIT4bu8w==') throw Error('Unreviewed official SDK');
const compiled = await build({ absWorkingDir: base, entryPoints: ['main.mjs'], bundle: true, write: false, format: 'esm', platform: 'browser', target: 'es2022', minify: true, legalComments: 'inline', metafile: true, charset: 'utf8', treeShaking: true });
if (compiled.outputFiles.length !== 1) throw Error('Expected one offline script');
const packages = [...new Set(Object.keys(compiled.metafile.inputs).filter(path => path.includes('node_modules/')).map(path => {
  const tail = path.split('node_modules/').at(-1).split('/'); return tail[0].startsWith('@') ? tail.slice(0, 2).join('/') : tail[0];
}))].sort();
const licenseFile = name => {
  for (const file of ['LICENSE', 'LICENSE.md', 'LICENSE.txt']) {
    try { return readFileSync(resolve(base, 'node_modules', name, file), 'utf8'); } catch {}
  }
  throw Error(`Missing license for ${name}`);
};
let notices = '# Embedded MCP App dependency notices\n\nThese exact files ship in the offline resource. Published MCP package license files include the project relicensing transition notice and both applicable license texts; they are reproduced in full below.\n\n';
for (const name of packages) {
  const pkg = JSON.parse(readFileSync(resolve(base, 'node_modules', name, 'package.json')));
  const expected = lock.packages[`node_modules/${name}`];
  if (!expected || pkg.version !== expected.version) throw Error(`Installed dependency differs from lock: ${name}`);
  notices += `## ${name}@${pkg.version}\n\n${licenseFile(name).replace(/[ \t]+$/gm, '').trim()}\n\n`;
}
notices += `## datatug/ai-plugin ${upstream.commit} lib/rows.js\n\n${readFileSync(resolve(base, 'vendor/LICENSE'), 'utf8').trim()}\n`;
const script = compiled.outputFiles[0].text.replace(/<\/script/gi, '<\\/script');
const style = readFileSync(resolve(base, 'style.css'), 'utf8');
const template = readFileSync(resolve(base, 'template.html'), 'utf8');
const html = `<!--\n${notices}\n-->\n` + template.replace('/* STYLE */', style).replace('/* SCRIPT */', script);
if (Buffer.byteLength(html) > 1500000) throw Error('Embedded app exceeds 1.5 MB bound');
if (html.includes(base) || html.includes('sourceMappingURL=')) throw Error('Nonportable build output');
const manifest = {
  format: 'datatug.mcp-app.build.v1', sdk: { name: '@modelcontextprotocol/ext-apps', version: sdk.version, integrity: sdk.integrity, gitHead: '82221c0c8ce7661efa6771c9d461511b1650495f' },
  lockSHA256: hash(readFileSync(resolve(base, 'package-lock.json'))), upstream,
  bundledPackages: packages.map(name => ({ name, version: lock.packages[`node_modules/${name}`].version, integrity: lock.packages[`node_modules/${name}`].integrity })),
  inputs: [...Object.keys(compiled.metafile.inputs).filter(path => !path.includes('node_modules/')), 'style.css', 'template.html', 'package.json', 'scripts/build.mjs', 'vendor/LICENSE', 'vendor/upstream.json'].sort().map(path => ({ path, sha256: hash(readFileSync(resolve(base, path))) })),
  htmlSHA256: hash(html), htmlBytes: Buffer.byteLength(html),
};
function emit(path, bytes) {
  const target = resolve(base, path);
  if (check) { if (readFileSync(target, 'utf8') !== bytes) throw Error(`Generated file is stale: ${relative(base, target)}`); }
  else writeFileSync(target, bytes);
}
emit('index.html', html); emit('build-manifest.json', JSON.stringify(manifest, null, 2) + '\n'); emit('THIRD_PARTY_NOTICES.md', notices);
const archive = resolve(root, 'THIRD_PARTY_NOTICES.md');
const marker = '\n<!-- BEGIN GENERATED MCP APP NOTICES -->\n';
const prior = readFileSync(archive, 'utf8').split(marker)[0].trimEnd();
const archiveNotices = prior + marker + notices + '<!-- END GENERATED MCP APP NOTICES -->\n';
if (check) { if (readFileSync(archive, 'utf8') !== archiveNotices) throw Error('Archive notices stale'); }
else writeFileSync(archive, archiveNotices);
console.log(JSON.stringify({ check, htmlBytes: manifest.htmlBytes, htmlSHA256: manifest.htmlSHA256, bundledPackages: packages }));
