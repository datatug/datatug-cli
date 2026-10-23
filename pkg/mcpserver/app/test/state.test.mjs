import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { Explorer, readPayload, cellText, cellDisplay, orderOf, pageOf, copyText, validateParameters } from '../state.mjs';
import { sortedOrder, nextSort, toCsv, toJson } from '../vendor/rows.js';
const fixture = JSON.parse(readFileSync(new URL('./typed-result.json', import.meta.url)));
const result = (handle = 'a'.repeat(64)) => { const r = structuredClone(fixture); r._meta.datatug.handle = handle; return r; };
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { resolve, reject, promise }; };
test('actual core tagged DTO preserves digits and distinguishes null, empty and false', () => {
  const p = readPayload(fixture);
  assert.equal(p.rows[0][0], '9007199254740993'); assert.equal(p.rows[0][1], '9007199254740992.2');
  assert.equal(p.rows[0][2], ''); assert.equal(p.rows[1][2], null); assert.equal(p.rows[0][3], 'false');
  assert.equal(p.rows[0][4], '1e-100'); assert.equal(p.values.enabled, 'false');
  assert.deepEqual(orderOf(p, { col: 1, desc: false }), [1, 2, 0]);
  assert.deepEqual(orderOf(p, { col: 0, desc: true }), [0, 2, 1]);
  assert.throws(() => cellText({ type: 'integer', value: 9007199254740992 }));
  assert.throws(() => cellText({ type: 'decimal', value: 1.1 }));
});
test('no public, legacy, malformed or error-row fallback', () => {
  for (const r of [{ structuredContent: fixture._meta.datatug }, { content: fixture._meta.datatug.result }, { ...fixture, isError: true }, { _meta: { datatug: { ...fixture._meta.datatug, version: 2 } } }]) assert.throws(() => readPayload(r));
  const r = result(); r._meta.datatug.result.recordset.rows[0].pop(); assert.throws(() => readPayload(r));
  const lossy = result(); lossy._meta.datatug.result.recordset.rows[0][0].value = 9007199254740992; assert.throws(() => readPayload(lossy));
});
test('vendor bytes match reviewed exact upstream and parity vectors include exponents/scales/ties/null', () => {
  const manifest = JSON.parse(readFileSync(new URL('../vendor/upstream.json', import.meta.url)));
  assert.equal(createHash('sha256').update(readFileSync(new URL('../vendor/rows.js', import.meta.url))).digest('hex'), manifest.files[0].sha256);
  const values = ['9007199254740992.2', '9007199254740992.1', null, '', 'false', '9007199254740992.10', '1e1000000', '-1e1000000', '0.00000000000000000001', '1e-20', '1.00', '1'];
  assert.deepEqual(sortedOrder(values.map(v => [v]), 0, true, false), [7, 8, 9, 10, 11, 1, 5, 0, 6, 3, 4, 2]);
  assert.deepEqual(sortedOrder([['1'], ['1.0'], [null]], 0, true, true), [0, 1, 2]);
  assert.deepEqual(nextSort(nextSort(undefined, 0), 0), { col: 0, desc: true });
  assert.equal(nextSort({ col: 0, desc: true }, 0), undefined);
});
test('page and record indices share stable sorted order and captured bounds', () => {
  const order = Array.from({ length: 61 }, (_, i) => 60 - i);
  assert.deepEqual(pageOf(order, 0).indices, order.slice(0, 25));
  assert.deepEqual(pageOf(order, 99), { page: 2, pages: 3, indices: order.slice(50) });
  assert.deepEqual(pageOf([], 5), { page: 0, pages: 1, indices: [] });
});
test('copy uses the same exact upstream CSV/JSON transformations', () => {
  const p = readPayload(fixture); const order = orderOf(p, { col: 1, desc: false });
  const names = p.columns.map(c => c.name), types = p.columns.map(c => c.type), rows = order.map(i => p.rows[i]);
  const json = copyText(p, order); assert.equal(json, toJson(names, types, rows));
  assert.match(json, /9007199254740993/); assert.match(json, /9007199254740992\.10/); assert.match(json, /"enabled": false/); assert.match(json, /"label": null/); assert.match(json, /"label": ""/);
  assert.equal(copyText(p, order, 'csv'), toCsv(names, rows)); assert.match(copyText(p, order, 'csv'), /"line one\nline two, ""quoted"""/);
});
test('parameter form validates exact numeric text, dates and boolean false without coercion', () => {
  const defs = [{ id: 'id', type: 'integer', required: true }, { id: 'n', type: 'decimal', required: true }, { id: 'enabled', type: 'boolean', required: true }, { id: 'day', type: 'date', required: false }];
  assert.deepEqual(validateParameters(defs, { id: '9007199254740993', n: '9007199254740992.10', enabled: 'false', day: '2024-02-29' }), {});
  const errors = validateParameters(defs, { id: '01', n: '1e2', enabled: '', day: '2025-02-29' }); assert.equal(Object.keys(errors).length, 4);
  assert.equal(Object.keys(validateParameters([{ id: 'ids', type: 'integer', multiValue: true }], { ids: '1' })).length, 1);
});
test('first notification automatically renders once; late host notification cannot overwrite rerun', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result()); assert.equal(m.current.handle, 'a'.repeat(64));
  const d = deferred(); let args;
  const pending = m.request((...v) => { args = v; return d.promise; }, true, { CustomerId: '9007199254740993', enabled: 'false' });
  m.notify(result('b'.repeat(64))); assert.equal(m.current.handle, 'a'.repeat(64)); assert.equal(m.previous, true);
  d.resolve(result('c'.repeat(64))); await pending;
  assert.deepEqual(args[0], { name: 'datatug_apps_rerun', arguments: { handle: 'a'.repeat(64), parameters: { CustomerId: '9007199254740993', enabled: 'false' } } });
  assert.equal(args[1].timeout, 30000); assert.equal(args[1].maxTotalTimeout, 30000); assert.equal(args[1].resetTimeoutOnProgress, false); assert.equal(m.current.handle, 'c'.repeat(64)); assert.equal(m.previous, false);
  m.notify(result('d'.repeat(64))); assert.equal(m.current.handle, 'c'.repeat(64));
});
test('one concurrent UI call; failed rerun retains previous rows with honest status', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result()); const d = deferred(); let calls = 0;
  const pending = m.request(() => { ++calls; return d.promise; }, true);
  await m.request(() => { ++calls; }, true); assert.equal(calls, 1);
  d.resolve({ isError: true, content: [{ type: 'text', text: 'private arbitrary transport detail' }] }); await pending;
  assert.equal(m.current.rows.length, 3); assert.equal(m.previous, true); assert.equal(m.busy, false);
  assert.match(m.status, /unavailable or expired/); assert.doesNotMatch(m.status, /arbitrary transport/);
});
test('disconnect/teardown abort and fence late responses', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result()); const d = deferred(); let signal;
  const pending = m.request((_, options) => { signal = options.signal; return d.promise; }, true);
  m.close(); assert.equal(signal.aborted, true); d.resolve(result('b'.repeat(64))); await pending;
  assert.equal(m.current.handle, 'a'.repeat(64)); assert.match(m.status, /Disconnected/);
});
test('ten-result local history and fresh rerun capabilities', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result());
  for (let i = 1; i <= 12; i++) await m.request(() => result(i.toString(16).padStart(64, '0')), true);
  assert.equal(m.history.length, 10); assert.equal(m.index, 9); m.select(0); assert.equal(m.index, 0);
  const old = m.current.handle; await m.request(() => result(old), true); assert.equal(m.current.handle, old); assert.equal(m.previous, true);
});
test('timeout state remains honest and result retry sends handle alone', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result()); let request;
  await m.request(args => { request = args; throw Object.assign(Error('detail'), { code: -32001 }); }, false);
  assert.deepEqual(request, { name: 'datatug_apps_result', arguments: { handle: 'a'.repeat(64) } }); assert.match(m.status, /timed out/);
});

test('authorization retry refreshes a retained result without inventing a history entry', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result());
  await m.request(() => result(), false);
  assert.equal(m.history.length, 1); assert.equal(m.index, 0); assert.equal(m.previous, false);
});

test('mixed core-valid cell types survive JSON export and remain visually distinct', () => {
  const p = readPayload(JSON.parse(readFileSync(new URL('./mixed-result.json', import.meta.url))));
  const text = copyText(p, p.rows.map((_, i) => i));
  const decoded = JSON.parse(text);
  assert.equal(decoded[0].value, 2);
  assert.equal(decoded[1].value, '10');
  assert.equal(decoded[2].value, false);
  assert.equal(decoded[3].value, 'false');
  assert.match(text, /"value": 9007199254740993/);
  assert.match(text, /"value": 9007199254740992\.10/);
  assert.equal(decoded[6].value, null);
  assert.notEqual(cellDisplay(p.typedRows[2][0]), cellDisplay(p.typedRows[3][0]));
  assert.notEqual(cellDisplay(p.typedRows[0][0]), cellDisplay({type:'string', value:'2'}));
  assert.equal(cellDisplay(p.typedRows[4][0]), '9007199254740993');
  assert.equal(cellDisplay(p.typedRows[5][0]), '9007199254740992.10');
});

test('official SDK2 REQUEST_TIMEOUT code produces honest timeout state without error data', async () => {
  const m = new Explorer(); m.connected = true; m.notify(result());
  await m.request(() => { throw Object.assign(Error('private transport detail'), {code:'REQUEST_TIMEOUT'}); }, false);
  assert.match(m.status,/timed out/); assert.doesNotMatch(m.status,/private transport detail/); assert.equal(m.previous,true);
});
