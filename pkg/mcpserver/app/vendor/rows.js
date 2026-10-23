// The rows of a result card as the person works with them: sorted for the Table view and as the
// text of the clipboard. Pure. A value is only ever handled here; it goes to the screen and to the
// person's clipboard and nowhere else (never a log, a tool result, a prompt or a notice).

const DECIMAL = /^([+-]?)(\d+(?:\.\d*)?|\.\d+)(?:[eE]([+-]?\d+))?$/
const JSON_NUMBER = /^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?$/

// Normalize decimal digits and their position, without expanding exponents or rounding through Number.
function decimal(value) {
  const match = DECIMAL.exec(value.trim())
  if (!match) return null
  const parts = match[2].split('.')
  const digits = parts.join('').replace(/^0+/, '')
  if (!digits) return { sign: 0, digits: '', position: 0n }
  return {
    sign: match[1] === '-' ? -1 : 1,
    digits,
    position: BigInt(digits.length) + BigInt(match[3] || '0') - BigInt((parts[1] || '').length),
  }
}

function compareDecimal(x, y) {
  if (x.sign !== y.sign) return x.sign < y.sign ? -1 : 1
  if (x.sign === 0) return 0
  if (x.position !== y.position) return (x.position < y.position ? -1 : 1) * x.sign
  for (let i = 0; i < Math.max(x.digits.length, y.digits.length); i++) {
    const a = x.digits[i] || '0'
    const b = y.digits[i] || '0'
    if (a !== b) return (a < b ? -1 : 1) * x.sign
  }
  return 0
}

// -1: a before b, 1: after, 0: equal. Decimal strings retain every digit; text follows numbers.
function compareNumeric(a, b) {
  // Retain the existing Number-compatible nondecimal forms (hex, Infinity, etc.). Decimal
  // strings, including values outside Number's finite range, always take the exact path.
  const number = value => {
    const exact = decimal(value)
    if (exact) return { exact }
    const n = Number(value)
    return value.trim() === '' || Number.isNaN(n) ? null
      : Number.isFinite(n) ? { exact: decimal(String(n)) } : { infinity: n }
  }
  const x = number(a)
  const y = number(b)
  if (x && y) {
    if (x.exact && y.exact) return compareDecimal(x.exact, y.exact)
    if (x.infinity === y.infinity) return 0
    if (x.infinity !== undefined) return x.infinity < 0 ? -1 : 1
    return y.infinity < 0 ? 1 : -1
  }
  if (!!x !== !!y) return x ? -1 : 1
  return compareText(a, b)
}
const compareText = (a, b) => a.localeCompare(b, 'en', { sensitivity: 'base' })

// The indices of `rows` in the order of column `col`: ascending, or descending when `desc`; a null
// is last either way; equal values keep their order (stable). Nothing is changed in `rows`.
export function sortedOrder(rows, col, numeric, desc) {
  const order = rows.map((_, i) => i)
  if (col < 0) return order
  const cmp = numeric ? compareNumeric : compareText
  return order.sort((i, j) => {
    const a = rows[i][col]
    const b = rows[j][col]
    if (a === null || a === undefined) return b === null || b === undefined ? i - j : 1
    if (b === null || b === undefined) return -1
    const c = cmp(a, b)
    return c === 0 ? i - j : desc ? -c : c
  })
}

// RFC 4180: a field with a quote, comma or line break is quoted and its quotes doubled. A null is an
// empty field. Values are raw (no formula prefix): the README says so.
const field = v => {
  const s = v === null || v === undefined ? '' : String(v)
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

// `columns` are names; `rows` are arrays of cells in the same order. A header row first.
export function toCsv(columns, rows) {
  return [columns, ...rows].map(r => r.map(field).join(',')).join('\n') + '\n'
}

// An array of objects keyed by column name, one per row. A number-typed cell that is a JSON number
// keeps its exact digits; anything else is a string; a null is null. `types` match `columns`.
export function toJson(columns, types, rows) {
  const cell = (v, t) => {
    if (v === null || v === undefined) return 'null'
    if ((t === 'integer' || t === 'number' || t === 'decimal') && JSON_NUMBER.test(v)) return v
    if (t === 'boolean' && (v === 'true' || v === 'false')) return v
    return JSON.stringify(String(v))
  }
  const objects = rows.map(r => '  {' + columns.map((c, i) => `${JSON.stringify(c)}: ${cell(r[i], types[i])}`).join(', ') + '}')
  return rows.length === 0 ? '[]\n' : `[\n${objects.join(',\n')}\n]\n`
}

// The cycle of a header press: none, ascending, descending, none.
export function nextSort(sort, col) {
  if (sort === undefined || sort === null || sort.col !== col) return { col, desc: false }
  return sort.desc ? undefined : { col, desc: true }
}

// What a result is, in a few words: the tables it reads, or the saved query's id, or "SQL query".
// Names from meta.json only; never a value, a path or a result id.
export function resultTitle(meta) {
  const nm = p => (p.schema === '' || p.schema === 'main' ? p.relation : `${p.schema}.${p.relation}`)
  const sources = [...new Set(meta.participants.map(nm).filter(n => n !== ''))]
  if (meta.savedId) return `Saved query ${meta.savedId}`
  if (sources.length > 0) return sources.length > 3 ? `${sources.slice(0, 3).join(', ')} and ${sources.length - 3} more` : sources.join(', ')
  return meta.unavailable.participants === 'native-sql' ? 'SQL query' : 'Query'
}

// The header line of a card: what it is, where it ran and how much it holds.
export function headerLine(meta, columnCount) {
  const what = resultTitle(meta)
  const db = meta.db.source === null ? '' : meta.db.env ? `${meta.db.source} (${meta.db.env})` : meta.db.source
  const kept = meta.rows.kept
  const rows = `${kept} row${kept === 1 ? '' : 's'}${meta.rows.hasMore ? ` (at least ${kept + 1})` : ''} x ${columnCount} column${columnCount === 1 ? '' : 's'}`
  return [what, db, rows].filter(p => p !== '').join(' · ')
}

// The window of `size` columns the Table view shows out of `total`: page `page` (0-based, clamped), the
// pages there are, and the first and one past the last column index. Any number of columns, one included.
export function columnPage(total, page, size) {
  const pages = Math.max(1, Math.ceil(total / size))
  const p = Math.min(Math.max(0, Math.trunc(Number(page)) || 0), pages - 1)
  const start = p * size
  return { page: p, pages, start, end: Math.min(total, start + size) }
}

// The results the panel can switch between: the newest `max` of `keys` (oldest first) that the person
// has not closed.
export function historyKeys(keys, closed, max) {
  return keys.slice(-max).filter(k => !closed.includes(k))
}

// The shown one: `want` while it is still in `keys`, else the newest; undefined for no results.
export function historyPick(keys, want) {
  return keys.includes(want) ? want : keys.at(-1)
}
