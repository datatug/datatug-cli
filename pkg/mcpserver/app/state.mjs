import { sortedOrder, nextSort, toCsv, toJson } from './vendor/rows.js';
export { nextSort };
export const VIEWS = ['Table', 'Record', 'Columns', 'Sources', 'Related'];
export const PAGE_SIZE = 25;
const types = new Set(['string', 'integer', 'decimal', 'number', 'boolean', 'null', 'date', 'datetime']);
const integer = /^(0|-?[1-9][0-9]*)$/;
const decimal = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/;
const record = v => v !== null && typeof v === 'object' && !Array.isArray(v);
export function cellDisplay(cell) {
  const value = cellText(cell);
  if (value === null) return 'NULL';
  if (cell.type === 'string') return value === '' ? '“” (empty string)' : JSON.stringify(value);
  if (cell.type === 'boolean') return `${value} (boolean)`;
  return value;
}
export function cellText(cell) {
  if (!record(cell) || !types.has(cell.type)) throw Error('Invalid typed value');
  const v = cell.value;
  if (cell.type === 'null') { if (v !== null) throw Error('Invalid null'); return null; }
  if (cell.type === 'boolean') { if (typeof v !== 'boolean') throw Error('Invalid boolean'); return String(v); }
  if (cell.type === 'number') { if (typeof v !== 'number' || !Number.isFinite(v)) throw Error('Invalid number'); return String(v); }
  if (typeof v !== 'string') throw Error('Invalid string-tagged value');
  if (cell.type === 'integer' && !integer.test(v)) throw Error('Invalid integer');
  if (cell.type === 'decimal' && !decimal.test(v)) throw Error('Invalid decimal');
  return v;
}
// Read private DTO only. Public summaries and legacy raw rows are deliberately never consulted.
export function readPayload(notification) {
  const p = notification?._meta?.datatug;
  if (notification?.isError || !record(p) || p.version !== 1 || !/^[a-f0-9]{64}$/.test(p.handle) || typeof p.queryId !== 'string') throw Error('Unavailable');
  const result = p.result;
  const rs = result?.recordset;
  if (!record(result) || !record(rs) || !Array.isArray(rs.columns) || !Array.isArray(rs.rows) || rs.rows.length > 500) throw Error('Unavailable');
  if (!rs.columns.every(c => record(c) && typeof c.name === 'string' && typeof c.type === 'string')) throw Error('Unavailable');
  const rows = rs.rows.map(row => {
    if (!Array.isArray(row) || row.length !== rs.columns.length) throw Error('Unavailable');
    return row.map(cellText);
  });
  const parameters = p.parameters ?? [];
  const values = p.values ?? {};
  if (!Array.isArray(parameters) || !record(values) || !parameters.every(q => record(q) && typeof q.id === 'string' && types.has(q.type) && typeof q.required === 'boolean') || !Object.values(values).every(v => typeof v === 'string')) throw Error('Unavailable');
  if (new Set(parameters.map(q => q.id)).size !== parameters.length) throw Error('Unavailable');
  if (result.limitations != null && !Array.isArray(result.limitations)) throw Error('Unavailable');
  if (result.provenance != null && !record(result.provenance)) throw Error('Unavailable');
  return { ...p, parameters, values, rows, typedRows: rs.rows, columns: rs.columns };
}
export function orderOf(p, sort) {
  return sortedOrder(p.rows, sort?.col ?? -1, !!sort && ['number', 'integer', 'decimal'].includes(p.columns[sort.col]?.type), sort?.desc ?? false);
}
export function pageOf(order, page) {
  const pages = Math.max(1, Math.ceil(order.length / PAGE_SIZE));
  const current = Math.min(Math.max(0, page), pages - 1);
  return { page: current, pages, indices: order.slice(current * PAGE_SIZE, (current + 1) * PAGE_SIZE) };
}
export function copyText(p, indices, format = 'json') {
  const names = p.columns.map(c => c.name);
  const rows = indices.map(i => p.rows[i]);
  if (format === 'csv') return toCsv(names, rows);
  // Column declarations do not constrain SQLite cell types. Format every row with its actual
  // tagged types through the exact upstream helper, then combine its object fragments.
  const objects = indices.map(i => toJson(names, p.typedRows[i].map(cell => cell.type), [p.rows[i]]).slice(2, -3));
  return objects.length ? `[\n${objects.join(',\n')}\n]\n` : '[]\n';
}
export function validateParameters(defs, values) {
  const errors = Object.create(null);
  for (const p of defs) {
    const v = Object.hasOwn(values, p.id) ? values[p.id] : '';
    if (p.multiValue) { errors[p.id] = 'This parameter cannot be edited here.'; continue; }
    if (v === '' && p.required) errors[p.id] = 'Required';
    else if (v !== '' || p.required) {
      if (p.type === 'integer' && !integer.test(v)) errors[p.id] = 'Use an exact integer without leading zeros.';
      if (p.type === 'decimal' && !decimal.test(v)) errors[p.id] = 'Use exact decimal text (no exponent).';
      if (p.type === 'number' && (!/^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/.test(v) || !Number.isFinite(Number(v)))) errors[p.id] = 'Use a finite number.';
      if (p.type === 'boolean' && !['true', 'false'].includes(v)) errors[p.id] = 'Choose true or false.';
      if (p.type === 'date' && (!/^\d{4}-\d{2}-\d{2}$/.test(v) || Number.isNaN(Date.parse(v)) || new Date(v).toISOString().slice(0, 10) !== v)) errors[p.id] = 'Use a valid YYYY-MM-DD date.';
      if (p.type === 'datetime' && (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z$/.test(v) || Number.isNaN(Date.parse(v)))) errors[p.id] = 'Use a UTC date and time ending in Z.';
      if (p.type === 'null' && v !== 'null') errors[p.id] = 'Use null.';
    }
  }
  return { ...errors };
}
export const unavailable = 'Private result unavailable or expired. Run the saved query again.';
// A host notification can initialize this view once; afterwards only matching local request epochs
// may replace accepted results. Old host notifications cannot race newer user reruns into the UI.
export class Explorer {
  constructor(changed = () => {}) {
    this.changed = changed; this.history = []; this.index = -1; this.epoch = 0;
    this.status = 'Connecting to host…'; this.busy = false; this.previous = false;
    this.connected = false; this.closed = false; this.initialized = false;
  }
  get current() { return this.history[this.index]; }
  accept(p) {
    const existing = this.history.findIndex(item => item.handle === p.handle);
    if (existing >= 0) { this.history[existing] = p; this.index = existing; }
    else { this.history.push(p); this.history = this.history.slice(-10); this.index = this.history.length - 1; }
    this.previous = false; this.status = `${p.rows.length} captured rows${p.result.truncated ? '; result truncated' : ''}.`;
  }
  notify(result) {
    if (this.closed || this.initialized || this.busy) return;
    try { this.accept(readPayload(result)); this.initialized = true; } catch { this.status = unavailable; }
    this.changed();
  }
  select(index) {
    if (this.busy || !this.history[index]) return;
    this.index = index; this.status = 'Retained result; authorization is checked on retry or rerun.'; this.changed();
  }
  async request(call, rerun, parameters = {}) {
    if (this.closed || this.busy || !this.connected || !this.current) return;
    const handle = this.current.handle;
    const epoch = ++this.epoch;
    this.controller = new AbortController();
    this.busy = true; this.previous = true; this.status = rerun ? 'Running saved query… Previous result shown.' : 'Checking retained result… Previous result shown.'; this.changed();
    try {
      const result = await call({ name: rerun ? 'datatug_apps_rerun' : 'datatug_apps_result', arguments: rerun ? { handle, parameters } : { handle } }, { signal: this.controller.signal, timeout: 30000 });
      if (this.closed || epoch !== this.epoch) return;
      const p = readPayload(result);
      if (rerun && p.handle === handle) throw Error('Unavailable');
      this.accept(p); this.initialized = true;
    } catch (e) {
      if (this.closed || epoch !== this.epoch) return;
      this.status = e?.code === -32001 || e?.name === 'TimeoutError' ? 'Request timed out. Previous result shown; retry is available.' : unavailable + ' Previous result shown.';
    } finally {
      if (!this.closed && epoch === this.epoch) { this.busy = false; this.controller = undefined; this.changed(); }
    }
  }
  disconnect() {
    this.connected = false; this.busy = false; this.previous = !!this.current; ++this.epoch;
    this.controller?.abort(); this.status = 'Disconnected from host. Previous result shown if available.'; this.changed();
  }
  close() { this.closed = true; this.disconnect(); }
}
