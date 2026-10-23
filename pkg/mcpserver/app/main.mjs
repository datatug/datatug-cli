import { App, applyDocumentTheme } from '@modelcontextprotocol/ext-apps';
import { Explorer, VIEWS, nextSort, orderOf, pageOf, copyText, validateParameters, cellDisplay } from './state.mjs';

const app = new App({ name: 'DataTug query explorer', version: '1.0.0' }, {}, { autoResize: false, strict: true });
const byId = id => document.getElementById(id);
const title = byId('title'), view = byId('view'), actions = byId('actions');
const content = byId('content'), status = byId('status'), editor = byId('editor');
let sort, page = 0, recordIndex = 0, selectedCell, renderedHandle;
let copyFallback, cleanupResize, theme = matchMedia('(prefers-color-scheme: dark)');
const model = new Explorer(render);
const node = (tag, text, attrs = {}) => {
  const element = document.createElement(tag);
  if (text !== undefined) element.textContent = text;
  for (const [k, v] of Object.entries(attrs)) element.setAttribute(k, String(v));
  return element;
};
function button(text, action, disabled = false, label) {
  const b = node('button', text, { type: 'button' }); b.disabled = disabled;
  if (label) b.setAttribute('aria-label', label);
  b.addEventListener('click', action); return b;
}
function kv(parent, label, value) {
  const row = node('div', undefined, { class: 'detail' });
  row.append(node('dt', label), node('dd', value)); parent.append(row);
}
function resetResult() { copyFallback?.remove(); byId('selection').textContent = ''; sort = undefined; page = 0; recordIndex = 0; selectedCell = undefined; }
function render() {
  const p = model.current;
  const nextHandle = p?.handle;
  if (nextHandle !== renderedHandle) { resetResult(); renderedHandle = nextHandle; renderParameters(); }
  title.textContent = p ? `Saved query ${p.queryId}` : 'Query explorer'; title.title = title.textContent;
  title.setAttribute('aria-label', title.textContent);
  status.textContent = model.status;
  byId('previous').hidden = !model.previous;
  byId('previous').textContent = 'Previous result — this is not a new query result.';
  content.setAttribute('aria-busy', String(model.busy));
  actions.disabled = model.busy;
  byId('history').disabled = model.busy;
  byId('run').disabled = model.busy || !model.connected || !p;
  byId('retry').disabled = model.busy || !model.connected || !p;
  view.disabled = !p;
  for (const option of actions.options) {
    if (option.value === 'copy-cell') option.disabled = view.value !== 'Table' || !selectedCell;
    if (option.value === 'copy-record') option.disabled = !p || view.value !== 'Record';
    if (['copy-json', 'copy-csv'].includes(option.value)) option.disabled = !p || !['Table', 'Record'].includes(view.value);
  }
  content.replaceChildren();
  if (!p) { content.append(node('p', model.status)); return; }
  if (view.value === 'Table') table(p);
  if (view.value === 'Record') recordView(p);
  if (view.value === 'Columns') columnsView(p);
  if (view.value === 'Sources') sourcesView(p);
  if (view.value === 'Related') content.append(node('p', 'Related metadata is unavailable for this saved SQL result. No relationships are inferred.'));
}
function pager(parent, pos, total, move, label) {
  const nav = node('nav', undefined, { class: 'pager', 'aria-label': label });
  nav.append(button('‹', () => move(pos - 1), pos === 0, `Previous ${label}`), node('span', `${total ? pos + 1 : 0} of ${total}`), button('›', () => move(pos + 1), pos + 1 >= total, `Next ${label}`)); parent.append(nav);
}
function table(p) {
  const order = orderOf(p, sort), pg = pageOf(order, page); page = pg.page;
  content.append(node('p', `${p.rows.length} captured rows${p.result.truncated ? ' · truncated; database total unknown' : ' · database total not reported'} · ${p.columns.length} columns`, { class: 'caption' }));
  if (!p.rows.length) { content.append(node('p', 'No rows in this captured result.')); return; }
  const wrap = node('div', undefined, { class: 'table-scroll', tabindex: '0', 'aria-label': 'Query rows; scroll horizontally for more columns' });
  const t = node('table'), head = node('tr'), thead = node('thead');
  head.append(node('th', '#', { scope: 'col' }));
  p.columns.forEach((c, col) => {
    const th = node('th', undefined, { scope: 'col', 'aria-sort': sort?.col === col ? sort.desc ? 'descending' : 'ascending' : 'none' });
    const b = button(c.name + (sort?.col === col ? sort.desc ? ' ↓' : ' ↑' : ''), () => {
      sort = nextSort(sort, col); page = 0; recordIndex = 0; selectedCell = undefined; byId('selection').textContent = ''; render();
      content.querySelectorAll('thead button')[col]?.focus();
    }, false, `Sort by ${c.name}`); th.append(b); head.append(th);
  }); thead.append(head); t.append(thead);
  const body = node('tbody');
  pg.indices.forEach((index, position) => {
    const row = node('tr'); const rh = node('th', undefined, { scope: 'row' });
    rh.append(button(String(page * 25 + position + 1), () => { recordIndex = page * 25 + position; view.value = 'Record'; render(); content.querySelector('button')?.focus(); }, false, `Open record ${page * 25 + position + 1}`)); row.append(rh);
    p.rows[index].forEach((v, col) => {
      const shown = cellDisplay(p.typedRows[index][col]);
      const td = node('td'); const b = button(shown, () => { selectedCell = { index, col }; actions.querySelector('[value=copy-cell]').disabled = false; byId('selection').textContent = `Selected cell: row ${page * 25 + position + 1}, ${p.columns[col].name}. Use Actions to copy.`; }, false, `${p.columns[col].name}: ${shown}; ${p.typedRows[index][col].type}`);
      b.className = 'cell' + (v === null || v === '' ? ' muted' : ''); td.append(b); row.append(td);
    }); body.append(row);
  }); t.append(body); wrap.append(t); content.append(wrap);
  pager(content, page, pg.pages, value => { page = value; selectedCell = undefined; byId('selection').textContent = ''; render(); content.querySelector('.pager button:not(:disabled)')?.focus(); }, 'pages');
}
function recordView(p) {
  const order = orderOf(p, sort); recordIndex = Math.max(0, Math.min(recordIndex, order.length - 1));
  if (!order.length) { content.append(node('p', 'No records in this captured result.')); return; }
  pager(content, recordIndex, order.length, value => { recordIndex = value; render(); content.querySelector('.pager button:not(:disabled)')?.focus(); }, 'records');
  const list = node('dl'); p.columns.forEach((c, i) => kv(list, `${c.name} · ${p.typedRows[order[recordIndex]][i].type}`, cellDisplay(p.typedRows[order[recordIndex]][i]))); content.append(list);
}
function columnsView(p) {
  const list = node('dl');
  p.columns.forEach((c, i) => kv(list, `${i + 1}. ${c.name}`, c.type));
  content.append(node('p', 'Declared result columns. Key, nullability and relationship metadata are not supplied.'), list);
}
function sourcesView(p) {
  const list = node('dl'); const provenance = p.result.provenance ?? {};
  for (const [key, label] of Object.entries({ source: 'Source', queryId: 'Saved query', mode: 'Mode', observedAt: 'Observed at', executionProfile: 'Execution profile', collection: 'Collection', snapshotId: 'Snapshot' })) {
    if (typeof provenance[key] === 'string' && provenance[key]) kv(list, label, provenance[key]);
  }
  content.append(node('p', 'Only authorized execution provenance is shown. Source tables and SQL lineage are not supplied for opaque saved SQL.'), list);
  const limitations = p.result.limitations ?? [];
  if (limitations.length) {
    content.append(node('h2', 'Reported limitations'));
    const ul = node('ul'); limitations.forEach(l => { const label = [l.policy, l.rowsFiltered ? 'rows filtered by policy' : '', Array.isArray(l.hiddenColumns) && l.hiddenColumns.length ? `hidden columns: ${l.hiddenColumns.join(', ')}` : ''].filter(v => typeof v === 'string' && v).join(' · '); ul.append(node('li', label || 'A limitation was reported by the execution service.')); }); content.append(ul);
  }
}
function renderParameters() {
  const holder = byId('fields'); holder.replaceChildren();
  const p = model.current;
  if (!p) return;
  if (!p.parameters.length) holder.append(node('p', 'This saved query has no declared parameters.'));
  p.parameters.forEach((param, i) => {
    const label = node('label', `${param.id}${param.required ? ' *' : ''}`); const id = `param-${i}`; label.htmlFor = id;
    const input = param.type === 'boolean' ? node('select') : node('input'); input.id = id; input.name = param.id;
    if (param.type === 'boolean') ['', 'true', 'false'].forEach(v => input.append(node('option', v || 'Choose…', { value: v })));
    else { input.type = 'text'; input.autocomplete = 'off'; input.spellcheck = false; }
    input.value = Object.hasOwn(p.values, param.id) ? p.values[param.id] : ''; input.required = param.required;
    input.disabled = !!param.multiValue;
    input.setAttribute('aria-describedby', `help-${i} error-${i}`);
    holder.append(label, input, node('p', `${param.type}${param.required ? ' · required' : ' · optional'}${input.disabled ? ' · editing unavailable' : ''}`, { id: `help-${i}`, class: 'caption' }), node('p', '', { id: `error-${i}`, class: 'error' }));
  });
}
editor.addEventListener('submit', event => {
  event.preventDefault(); if (model.busy || !model.current) return;
  const values = Object.fromEntries(new FormData(editor));
  for (const param of model.current.parameters) {
    if (!param.required && param.type !== 'string' && values[param.id] === '') delete values[param.id];
  }
  const errors = validateParameters(model.current.parameters, values);
  model.current.parameters.forEach((p, i) => { const input = byId(`param-${i}`); input.setAttribute('aria-invalid', String(!!errors[p.id])); byId(`error-${i}`).textContent = errors[p.id] ?? ''; });
  if (Object.keys(errors).length) { editor.querySelector('[aria-invalid=true]')?.focus(); return; }
  model.request(app.callServerTool.bind(app), true, values);
});
view.replaceChildren(...VIEWS.map(label => node('option', label))); view.addEventListener('change', render);
async function copy(kind) {
  const p = model.current; if (!p) return;
  let text;
  if (kind === 'record' && view.value !== 'Record') return;
  if (kind === 'cell') {
    if (!selectedCell) { status.textContent = 'Select a table cell first.'; return; }
    text = p.rows[selectedCell.index][selectedCell.col] ?? 'NULL';
  } else {
    const order = orderOf(p, sort);
    const indices = kind === 'record' || view.value === 'Record' ? order.length ? [order[recordIndex]] : [] : pageOf(order, page).indices;
    text = copyText(p, indices, kind === 'csv' ? 'csv' : 'json');
  }
  // This runs only in the user's action gesture; denial leaves a visible native selection fallback.
  try { await navigator.clipboard.writeText(text); status.textContent = 'Copied locally to clipboard.'; }
  catch {
    copyFallback?.remove(); copyFallback = node('section', undefined, { 'aria-label': 'Copy manually', class: 'copy-fallback' });
    const textarea = node('textarea'); textarea.readOnly = true; textarea.value = text; textarea.setAttribute('aria-label', 'Text to copy manually');
    copyFallback.append(node('p', 'Clipboard permission was denied. Select and copy this text manually.'), textarea, button('Close', () => { copyFallback.remove(); actions.focus(); }));
    byId('app').append(copyFallback); textarea.focus(); textarea.select(); status.textContent = 'Clipboard denied. Manual copy text is selected.';
  }
}
actions.addEventListener('change', () => {
  const action = actions.value; actions.value = '';
  if (action === 'parameters') { editor.hidden = !editor.hidden; if (!editor.hidden) editor.querySelector('input,select,button')?.focus(); }
  if (action === 'history') {
    byId('history-panel').hidden = !byId('history-panel').hidden;
    const picker = byId('history'); picker.replaceChildren(...model.history.map((p, i) => node('option', `${i + 1}. ${p.queryId} · ${p.rows.length} captured`, { value: i }))); picker.value = model.index; picker.focus();
  }
  if (action.startsWith('copy-')) void copy(action.slice(5));
  if (action === 'retry') model.request(app.callServerTool.bind(app), false);
});
byId('history').addEventListener('change', event => model.select(Number(event.target.value)));
byId('retry').addEventListener('click', () => model.request(app.callServerTool.bind(app), false));
byId('close-parameters').addEventListener('click', () => { editor.hidden = true; actions.focus(); });
function setTheme(ctx) { applyDocumentTheme(ctx?.theme ?? (theme.matches ? 'dark' : 'light')); }
const themeChanged = () => setTheme(app.getHostContext()); theme.addEventListener('change', themeChanged);
function cleanup() { model.close(); cleanupResize?.(); theme.removeEventListener('change', themeChanged); window.removeEventListener('pagehide', teardown); }
function teardown() { cleanup(); void app.close(); }
app.ontoolresult = result => model.notify(result);
app.ontoolcancelled = () => { model.status = 'Query cancelled. Previous result shown if available.'; model.previous = !!model.current; render(); };
app.onhostcontextchanged = ctx => setTheme(ctx);
app.onteardown = async () => { cleanup(); setTimeout(() => { void app.close(); }, 0); return {}; };
app.onclose = () => { cleanupResize?.(); model.disconnect(); };
app.onerror = () => { model.status = 'Host transport error. Retry if the connection is available.'; render(); };
window.addEventListener('pagehide', teardown);
setTheme(); render();
try {
  await app.connect(undefined, { timeout: 10000 });
  if (!model.closed) { model.connected = true; setTheme(app.getHostContext()); cleanupResize = app.setupSizeChangedNotifications(); if (!model.current) model.status = 'Waiting for the saved query result…'; render(); }
} catch { if (!model.closed) model.disconnect(); }
