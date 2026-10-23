// Dev-only real browser driver. Every failure prints only its named phase, never private payloads.
import { chromium } from 'playwright';
import { diagnosticCollector } from './browser-diagnostics.mjs';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
const host = process.env.DATATUG_MCP_APPS_HOST;
const binary = process.env.DATATUG_MCP_APPS_BINARY;
const output = process.env.DATATUG_MCP_APPS_ARTIFACTS;
const label = process.env.DATATUG_MCP_APPS_LABEL ?? 'source';
if (!/^[a-z0-9-]{1,48}$/.test(label)) throw Error('Invalid artifact label');
if (!host || !binary) throw Error('Browser driver requires owned Go fixture');
if (output) await mkdir(output, { recursive: true });
const privateRow = 'ROW_ONLY_CELL_a9246ecae531', privateBinding = 'UI_ONLY_BINDING_827193af62bc';
let phase = 'launch', browser, context, page, inner, outer;
const diagnostics = diagnosticCollector([privateRow, privateBinding]), steps = [];
let consolePrivateLeak = false, darkErrorContrast, finalBrokerReport;
const check = (condition, name) => { if (!condition) throw Error(name); };
async function step(name, action) { phase = name; await action(); steps.push({ name, passed: true }); }
async function open(mode, theme = 'light') {
  await page.evaluate(({ mode, theme }) => window.acceptance.open(mode, theme), { mode, theme });
  outer = page.frames().find(f => /\/sandbox\.html$/.test(f.url()));
  check(outer, 'official outer frame absent');
  inner = outer.childFrames()[0]; check(inner, 'official inner frame absent');
  await inner.locator('#title').waitFor();
  await inner.waitForFunction(() => document.querySelector('#content table, #content dl') !== null);
}
async function action(value) {
  const select = inner.locator('#actions');
  // A real trusted click provides user activation before the menu action.
  await select.click(); await select.selectOption(value);
}
async function rock(expected) {
  await inner.waitForFunction(expected => { const names = [...document.querySelectorAll('thead button')].map(e => e.textContent.replace(/[ ↑↓]/g, '')); const genre = names.indexOf('GenreName') + 1, tracks = names.indexOf('TracksPurchased') + 1; return [...document.querySelectorAll('tbody tr')].some(r => r.cells[genre]?.textContent === '"Rock"' && r.cells[tracks]?.textContent === String(expected)); }, expected);
}
async function clipboard() { return page.evaluate(() => navigator.clipboard.readText()); }
async function screenshot(name) { if (output) await page.screenshot({ path: resolve(output, `mcp-apps-${label}-${name}.png`), fullPage: true }); }
try {
  browser = await chromium.launch({ headless: true, executablePath: process.env.DATATUG_MCP_APPS_CHROMIUM });
  context = await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: 'reduce' });
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await context.route('**/*', route => {
    const u = new URL(route.request().url());
    return ['127.0.0.1', 'localhost'].includes(u.hostname) ? route.continue() : route.abort();
  });
  page = await context.newPage();
  page.on('console', message => diagnostics.console(message));
  page.on('pageerror', () => diagnostics.pageError());
  await page.goto(host); await page.waitForFunction(() => window.acceptanceReady === true);
  await step('actual CLI automatic Chinook result', async () => { await open('real'); await rock(14); });
  await step('keyboard parameter rerun changes real SQL result', async () => {
    await action('parameters'); await inner.locator('#param-0').fill('2'); await inner.locator('#param-0').press('Enter');
    await rock(17); check(await inner.locator('#previous').isHidden(), 'new result marked previous');
    await page.evaluate(() => window.acceptance.late()); await rock(17);
  });
  await step('actual empty SQL result is explicit and recoverable', async () => {
    await inner.locator('#param-0').fill('999999'); await inner.locator('#run').click();
    await inner.getByText('No rows in this captured result.',{exact:true}).waitFor();
    check((await inner.locator('#status').innerText()).includes('0 captured rows'),'empty result count missing');
    check(await inner.locator('#previous').isHidden(),'empty result mislabeled previous');
    await inner.locator('#param-0').fill('2'); await inner.locator('#run').click(); await rock(17);
  });
  await step('five views and accessible keyboard controls', async () => {
    const view = inner.locator('#view');
    for (const name of ['Record', 'Columns', 'Sources', 'Related', 'Table']) {
      await view.focus(); await view.selectOption(name);
      const text = await inner.locator('#content').innerText();
      check(text !== '', 'empty view');
      if (name === 'Columns') check(['GenreName','TotalSpent','TracksPurchased'].every(column=>text.includes(column)),'column metadata missing');
      if (name === 'Sources') check(text.includes('chinook-local') && text.includes('opaque-privileged'),'authorized provenance missing');
      if (name === 'Related') check(text.includes('Related metadata is unavailable'),'Related inferred metadata');
    }
    check(await inner.locator('th[scope=col]').count() > 1 && await inner.locator('th[scope=row]').count() > 0, 'table lacks semantic headings');
    await inner.locator('thead button').first().focus(); await inner.locator('thead button').first().press('Enter');
    check(await inner.locator('thead th[aria-sort=ascending]').count() === 1, 'keyboard sort absent');
  });
  await step('320px light single toolbar and wide dark host theme', async () => {
    await page.setViewportSize({ width: 320, height: 900 });
    const geometry = await inner.locator('header').evaluate(h => {
      const boxes = [...h.children].map(e => e.getBoundingClientRect()); const hb = h.getBoundingClientRect();
      return { oneLine: boxes.every(b => b.top >= hb.top && b.bottom <= hb.bottom), fits: h.scrollWidth <= h.clientWidth, named: h.querySelector('h1')?.getAttribute('aria-label')?.length > 0 };
    }); check(geometry.oneLine && geometry.fits && geometry.named, 'narrow toolbar geometry');
    await screenshot('320-light-real-chinook');
    await page.setViewportSize({ width: 1280, height: 900 }); await page.evaluate(() => window.acceptance.theme('dark'));
    await inner.waitForFunction(() => document.documentElement.dataset.theme === 'dark');
    await screenshot('1280-dark-real-chinook');
    check(await inner.evaluate(() => matchMedia('(prefers-reduced-motion: reduce)').matches), 'reduced motion absent');
  });
  await step('dark required-parameter validation has readable error and keyboard focus', async () => {
    if (await inner.locator('#editor').isHidden()) await action('parameters');
    await inner.locator('#param-0').fill(''); await inner.locator('#run').click();
    check(await inner.locator('#error-0').innerText() === 'Required', 'missing required validation');
    const contrast = await inner.locator('#error-0').evaluate(error => {
      const channels = color => color.match(/[\d.]+/g).slice(0,3).map(Number).map(v => {v/=255;return v<=0.04045?v/12.92:((v+0.055)/1.055)**2.4});
      const luminance = color => channels(color).reduce((sum,v,i)=>sum+v*[0.2126,0.7152,0.0722][i],0);
      const a=luminance(getComputedStyle(error).color), b=luminance(getComputedStyle(document.documentElement).backgroundColor);
      return (Math.max(a,b)+0.05)/(Math.min(a,b)+0.05);
    }); darkErrorContrast = contrast; check(contrast>=4.5, 'dark error contrast below AA');
    check(await inner.locator('#param-0').evaluate(e => document.activeElement === e && e.getAttribute('aria-invalid') === 'true'), 'validation focus inaccessible');
    await screenshot('1280-dark-required-parameter');
    await inner.locator('#param-0').fill('2'); await inner.locator('#run').click();
    await inner.waitForFunction(() => document.querySelector('#content').getAttribute('aria-busy') === 'false'); await rock(17);
  });
  await step('real reruns bound selectable history to ten results', async () => {
    for (let i=0;i<9;i++) {
      await inner.locator('#param-0').fill(i%2 ? '2' : '1'); await inner.locator('#run').click(); await rock(i%2 ? 17 : 14);
    }
    await action('history'); check(await inner.locator('#history option').count() === 10,'history did not retain ten');
    await inner.locator('#history').selectOption('0'); check((await inner.locator('#status').innerText()).includes('Retained result'), 'history selection not labeled');
    await inner.locator('#history').selectOption('9');
  });
  await step('real private cell and UI-only parameter absent from model transcript', async () => {
    await open('sentinel');
    await inner.getByText(JSON.stringify(privateRow), { exact: true }).waitFor();
    await action('parameters');
    await inner.locator('input[name=UiNote]').fill(privateBinding);
    await inner.locator('#run').click();
    await inner.getByText(JSON.stringify(privateBinding), { exact: true }).waitFor();
    const report = await page.evaluate(() => window.acceptance.report());
    check(!report.modelProjectionPrivateLeak && !report.stderrPrivateLeak, 'private sentinel diagnostic/projection leak');
    check(report.capturedContinuations >= 4, 'continuation evidence missing');
  });
  await step('optional integer override and clearing applies authoritative saved default', async () => {
    const factor=inner.locator('input[name=OptionalFactor]');
    check(await factor.inputValue()==='5','applied optional default absent from editor');
    check((await inner.locator('#editor').innerText()).includes('blank uses saved default or remains unset'),'optional blank behavior unlabeled');
    await factor.fill('7'); await inner.locator('#run').click();
    await inner.waitForFunction(() => {const names=[...document.querySelectorAll('thead button')].map(e=>e.textContent);return document.querySelector('tbody tr')?.cells[names.indexOf('DefaultedValue')+1]?.textContent==='7';});
    await factor.fill(''); await inner.locator('#run').click();
    await inner.waitForFunction(() => {const names=[...document.querySelectorAll('thead button')].map(e=>e.textContent);return document.querySelector('tbody tr')?.cells[names.indexOf('DefaultedValue')+1]?.textContent==='5';});
    check(await factor.inputValue()==='5' && await inner.locator('#previous').isHidden(),'cleared optional scalar did not apply fresh default');
  });
  await step('real unavailable response retains explicitly previous rows', async () => {
    await page.evaluate(() => window.acceptance.control('revoke'));
    await inner.locator('#run').click();
    await inner.waitForFunction(() => document.querySelector('#status').textContent.includes('unavailable or expired'));
    check(await inner.locator('#previous').isVisible(), 'failed retry lost previous label');
    await inner.getByText(JSON.stringify(privateBinding), { exact: true }).waitFor();
    await page.evaluate(() => window.acceptance.control('restore'));
  });
  await step('official SDK UI timeout keeps previous data and allows retry', async () => {
    await open('sentinel'); const cancellationsBefore = (await page.evaluate(() => window.acceptance.report())).cancelledCalls;
    await page.evaluate(() => window.acceptance.control('delay-on'));
    await action('retry'); await inner.waitForFunction(() => document.querySelector('#content').getAttribute('aria-busy') === 'true');
    check(await inner.locator('#run').isDisabled() && await inner.locator('#actions').isDisabled(), 'concurrent UI execution enabled');
    await inner.waitForFunction(() => document.querySelector('#status').textContent.includes('timed out'), undefined, {timeout:40000});
    check(await inner.locator('#previous').isVisible() && !await inner.locator('#retry').isDisabled(),'timeout lacked previous/retry state');
    await page.waitForFunction(async before => { const report = await window.acceptance.report(); return report.activeCalls === 0 && report.cancelledCalls > before; }, cancellationsBefore, {timeout:3000});
    await page.evaluate(() => window.acceptance.control('delay-off'));
    await inner.locator('#retry').click();
    await inner.waitForFunction(() => document.querySelector('#content').getAttribute('aria-busy') === 'false' && document.querySelector('#previous').hidden);
  });
  await step('typed fixture exact sort record and JSON CSV copy', async () => {
    await open('typed');
    await inner.getByRole('button', { name: 'Sort by amount', exact: true }).click();
    const values = await inner.locator('tbody tr').evaluateAll(rows => rows.map(r => r.cells[2].textContent));
    check(values.join('|') === '9007199254740992.1|9007199254740992.10|9007199254740992.2', 'exact decimal ordering');
    await inner.getByRole('button', { name: 'Sort by amount', exact: true }).click();
    check(await inner.locator('tbody tr').first().locator('td').nth(1).innerText() === '9007199254740992.2', 'descending decimal ordering');
    await inner.getByRole('button', { name: 'Sort by amount', exact: true }).click();
    await inner.getByRole('button', { name: 'Sort by amount', exact: true }).click();
    check(await inner.locator('#actions option[value=copy-record]').isDisabled(), 'Table exposed invisible record copy');
    await action('copy-json'); await inner.waitForFunction(() => document.querySelector('#status').textContent.includes('Copied locally'));
    const json = await clipboard(); check(json.includes('9007199254740993') && json.includes('9007199254740992.10'), 'JSON rounded exact digits');
    await action('copy-csv');
    await page.waitForFunction(async () => (await navigator.clipboard.readText()).startsWith('id,amount,label,enabled,ratio,day,instant\n'));
    const csv = await clipboard(); check(csv.includes('9007199254740992.10') && csv.includes('line one\nline two, ""quoted""'), 'CSV lost exact digits or RFC4180 quoting');
    await inner.getByRole('button', { name: 'Open record 1', exact: true }).click();
    check((await inner.locator('#content').innerText()).includes('9007199254740992.1'), 'Record did not follow sorted order');
    await action('copy-record'); check((await clipboard()).includes('9007199254740992.1'), 'displayed record copy differs');
    await screenshot('typed-fixture-record');
  });
  await step('captured local paging and displayed record target', async () => {
    await open('paged');
    check(await inner.locator('tbody tr').count() === 25,'first captured page size');
    await inner.getByRole('button',{name:'Next pages',exact:true}).click();
    check(await inner.locator('tbody tr').count() === 25,'second captured page size');
    check(await inner.locator('#actions option[value=copy-record]').isDisabled(),'page2 exposed invisible record copy');
    await inner.getByRole('button',{name:'Open record 26',exact:true}).click();
    await action('copy-record'); const record = JSON.parse(await clipboard());check(record.length===1 && record[0].id===26,'record copy did not target displayed row26');
    await inner.locator('#view').selectOption('Table');
    await inner.getByRole('button',{name:'Next pages',exact:true}).click();check(await inner.locator('tbody tr').count()===11,'last captured page size');
    check((await inner.locator('#content .caption').innerText()).includes('database total unknown'),'truncation count fabricated');
  });
  await step('mixed cell JSON types and actual-type display', async () => {
    await open('mixed');
    check((await inner.locator('tbody').innerText()).includes('false (boolean)'), 'boolean type not visible');
    check((await inner.locator('tbody').innerText()).includes('"false"'), 'string type not visible');
    await action('copy-json'); await inner.waitForFunction(() => document.querySelector('#status').textContent.includes('Copied locally'));
    const raw = await clipboard(), rows = JSON.parse(raw);
    check(rows[1].value === '10' && rows[2].value === false && rows[3].value === 'false', 'mixed JSON types changed');
    check(raw.includes('9007199254740993') && raw.includes('9007199254740992.10'), 'mixed JSON precision changed');
  });
  await step('native clipboard denial exposes selected manual fallback', async () => {
    const session = await context.newCDPSession(page);
    const target = await session.send('Target.getTargetInfo');
    await session.send('Browser.setPermission', { permission: { name: 'clipboard-write' }, setting: 'denied', origin: new URL(host).origin, embeddedOrigin: new URL(outer.url()).origin, browserContextId: target.targetInfo.browserContextId });
    await action('copy-json');
    await inner.getByRole('textbox', { name: 'Text to copy manually' }).waitFor();
    const fallback = await inner.getByRole('textbox', { name: 'Text to copy manually' }).evaluate(e => ({ selected: e.selectionStart === 0 && e.selectionEnd === e.value.length, focused: document.activeElement === e, value: e.value }));
    check(fallback.focused && fallback.selected && fallback.value.includes('9007199254740993'), 'manual fallback inaccessible or rounded');
    await screenshot('clipboard-denied-fixture');
    await session.detach();
  });
  await step('official sandbox native source isolation and CSP network denial', async () => {
    const hostOrigin = new URL(host).origin, sandboxOrigin = new URL(outer.url()).origin;
    check(hostOrigin !== sandboxOrigin, 'host sandbox origins same');
    check(await inner.evaluate(() => { try { void top.document; return false; } catch { return true; } }), 'inner can access top');
    const before = await inner.locator('#content').innerText();
    const callsBefore = (await page.evaluate(() => window.acceptance.report())).totalCalls;
    await page.evaluate(() => window.acceptance.siblingForgery());
    await page.evaluate(() => { const f = document.querySelector('#sandbox'); f.contentWindow.postMessage({ jsonrpc: '2.0', method: 'ui/notifications/tool-result', params: { isError: true, content: [] } }, 'https://wrong-origin.invalid'); });
    // Explicit synthetic branch test of exact official parent-origin guard; not a native-origin spoof claim.
    await outer.evaluate(() => window.dispatchEvent(new MessageEvent('message', { source: parent, origin: 'https://wrong-origin.invalid', data: { jsonrpc: '2.0', id: 94381, method: 'tools/call', params: { name: 'datatug_apps_result', arguments: { handle: 'f'.repeat(64) } } } })));
    check(await inner.locator('#content').innerText() === before, 'forged traffic changed result');
    const network = await inner.evaluate(async probe => {
      const violations = []; const listener = e => violations.push(e.effectiveDirective); document.addEventListener('securitypolicyviolation', listener);
      const nested = document.createElement('iframe'); nested.src = probe + '/probe'; document.body.append(nested);
      const attempts = await Promise.all([fetch('https://external.invalid/blocked').then(() => false, () => true), fetch(probe + '/probe').then(() => false, () => true)]);
      await new Promise(yes => setTimeout(yes, 50)); document.removeEventListener('securitypolicyviolation', listener);
      nested.remove(); return { attempts, violations };
    }, host);
    check(network.attempts.every(Boolean) && network.violations.includes('connect-src') && network.violations.includes('frame-src'), 'CSP did not reject network');
    const report = await page.evaluate(() => window.acceptance.report()); check(report.probeRequests === 0, 'App reached host network'); check(report.totalCalls === callsBefore,'forged source/origin traffic reached broker');
  });
  await step('official teardown aborts pending request and child closes independently', async () => {
    await open('sentinel'); const cancellationsBefore = (await page.evaluate(() => window.acceptance.report())).cancelledCalls;
    await page.evaluate(() => window.acceptance.control('delay-on'));
    await action('retry'); await inner.waitForFunction(() => document.querySelector('#content').getAttribute('aria-busy') === 'true');
    await page.evaluate(() => window.acceptance.teardown());
    await inner.waitForFunction(() => document.querySelector('#status').textContent.includes('Disconnected from host'));
    check(await inner.locator('#previous').isVisible(),'mounted teardown lacked previous indicator');
    await page.waitForFunction(async before => { const report = await window.acceptance.report(); return report.activeCalls === 0 && report.cancelledCalls > before; }, cancellationsBefore, {timeout:3000});
    await page.evaluate(() => window.acceptance.control('delay-off'));
    await page.evaluate(() => window.acceptance.control('close-child'));
    const report = await page.evaluate(() => window.acceptance.report());
    check(report.childClosedCleanly && !report.modelProjectionPrivateLeak && !report.stderrPrivateLeak, 'child lifecycle/privacy evidence failed'); finalBrokerReport = report;
  });
  const diagnosticSummary = await diagnostics.drain();
  consolePrivateLeak = diagnosticSummary.privateLeak;
  check(!consolePrivateLeak && !diagnosticSummary.scanFailed, 'browser diagnostics privacy failed');
  const appDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const manifest = JSON.parse(await readFile(resolve(appDir, 'build-manifest.json')));
  const fixture = JSON.parse(await readFile(resolve(appDir,'test/browser-fixture.json')));
  const config = await page.evaluate(() => fetch('/config').then(r=>r.json()));
  check(config.actualAssetSHA256===manifest.htmlSHA256,'served embedded asset hash mismatch');
  const receipt = { artifactLabel: label, binaryVersion: config.binaryVersion, fixture, runtime: 'headless Chromium via pinned Playwright; local test host', browserVersion: browser.version(), darkErrorContrast, broker: finalBrokerReport, binarySHA256: createHash('sha256').update(await readFile(binary)).digest('hex'), appHTMLSHA256: config.actualAssetSHA256, sdk: manifest.sdk, cliChildPATHContainsNode: false, consolePrivateLeak, modelProjectionPrivateLeak: false, stderrPrivateLeak: false, consoleCallsRemoved: manifest.consoleCallsRemoved, clipboardPositive: true, clipboardDeniedFallback: true, brokerCancellationObservedBeforeBarrierRelease: true, nativeSiblingSourceRejected: true, syntheticOriginGuardRejected: true, cspNetworkRejected: true, actualChinookRockCounts: [14, 17], typedPrecisionScenario: 'separate core-valid renderer fixture', steps };
  if (output) await writeFile(resolve(output, `mcp-apps-${label}-browser-receipt.json`), JSON.stringify(receipt, null, 2) + '\n');
  process.stdout.write(JSON.stringify(receipt) + '\n');
 } catch {
  const summary = await diagnostics.drain();
  process.stderr.write(JSON.stringify({ phase, failure: 'BROWSER_ACCEPTANCE_FAILED', diagnostics: summary }) + '\n');
  process.exitCode = 1;
} finally { await context?.close(); await browser?.close(); }
