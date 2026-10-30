// Maintained fail-on-skip entrypoint. The supplied CLI is never rebuilt or replaced here.
import { build } from 'esbuild';
import { chromium } from 'playwright';
import { mkdtemp, readFile, writeFile, rm, stat } from 'node:fs/promises';
import { resolve, dirname, isAbsolute } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';
import { createHash } from 'node:crypto';
import { spawn, execFileSync } from 'node:child_process';
const app = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const root = resolve(app, '../../..');
for (const name of ['DATATUG_MCP_TEST_BINARY', 'DATATUG_DEMO_PROJECT_DIR']) {
  const value = process.env[name];
  if (!value || !isAbsolute(value)) throw Error(`${name} must name an existing absolute path; acceptance never skips`);
  await stat(value);
}
const fixture = JSON.parse(await readFile(resolve(app,'test/browser-fixture.json')));
const fixtureHead = execFileSync('git',['-C',process.env.DATATUG_DEMO_PROJECT_DIR,'rev-parse','HEAD'],{encoding:'utf8'}).trim();
if (fixtureHead !== fixture.commit) throw Error('Demo fixture must use the exact audited commit in test/browser-fixture.json');
const fixtureDirty = execFileSync('git',['-C',process.env.DATATUG_DEMO_PROJECT_DIR,'status','--porcelain','--','.'],{encoding:'utf8'}).trim();
if (fixtureDirty) throw Error('Demo project input must be clean; only the owned private copy is modified');
if (createHash('sha256').update(await readFile(resolve(root,fixture.databasePath))).digest('hex') !== fixture.databaseSHA256) throw Error('Committed Chinook database differs from fixture manifest');
const sandbox = resolve(app, 'test/official-sandbox');
const manifest = JSON.parse(await readFile(resolve(sandbox, 'upstream.json')));
for (const file of manifest.files) {
  const hash = createHash('sha256').update(await readFile(resolve(sandbox, file.destination))).digest('hex');
  if (hash !== file.sha256) throw Error('Pinned reference sandbox differs from upstream');
}
const output = await mkdtemp(resolve(tmpdir(), 'datatug-mcp-apps-host-'));
try {
  await build({ absWorkingDir: app, entryPoints: ['test/browser-host.mjs'], outfile: resolve(output, 'host.js'), bundle: true, platform: 'browser', format: 'esm', target: 'es2022', minify: true, drop: ['console'] });
  const compiled = await build({ absWorkingDir: app, entryPoints: ['test/official-sandbox/sandbox.ts'], bundle: true, write: false, platform: 'browser', format: 'esm', target: 'es2022', minify: true, drop: ['console'] });
  const html = (await readFile(resolve(sandbox, 'sandbox.html'), 'utf8')).replace('<script type="module" src="/src/sandbox.ts"></script>', () => `<script type="module">${compiled.outputFiles[0].text.replace(/<\/script/gi, '<\\/script')}</script>`);
  await writeFile(resolve(output, 'sandbox.html'), html);
  const args = ['test', './pkg/server', '-run', '^TestMCPAppsBrowserHost$', '-count=1', '-timeout=240s', '-v'];
  const child = spawn('go', args, { cwd: root, stdio: 'inherit', env: { ...process.env, DATATUG_MCP_APPS_BROWSER: '1', DATATUG_MCP_APPS_ASSETS: output, DATATUG_MCP_APPS_NODE: process.execPath, DATATUG_MCP_APPS_CHROMIUM: chromium.executablePath(), DATATUG_TELEMETRY: '0', DO_NOT_TRACK: '1' } });
  const timer = setTimeout(() => child.kill('SIGTERM'), 250000);
  const code = await new Promise((yes, no) => { child.once('error', no); child.once('exit', (code, signal) => yes(code ?? (signal ? 1 : 0))); });
  clearTimeout(timer); process.exitCode = code;
} finally { await rm(output, { recursive: true, force: true }); }
