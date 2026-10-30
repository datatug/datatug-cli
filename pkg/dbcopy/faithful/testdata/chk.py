"""Independent checker: catalog + per-column/per-table checksums for SQLite files and PG schemas.
usage: chk.py A B   where each is sqlite:<path> or pg:<url>[#schema]
Prints diffs; exit 1 if any.
"""
import sys, sqlite3, hashlib, re, json, collections

def h(items):
    m = hashlib.sha256()
    for it in sorted(items):
        m.update(it.encode('utf-8', 'surrogatepass') if isinstance(it, str) else it)
        m.update(b'\x00\x01')
    return m.hexdigest()[:16]

def rs(typ, v):
    if v is None:
        return 'NULL'
    if isinstance(v, bytes):
        return typ + ':x' + v.hex()
    if isinstance(v, float):
        return typ + ':' + repr(v)
    return typ + ':' + repr(v)

SEM = '--semantic' in sys.argv
def sqlite_snap(path):
    c = sqlite3.connect('file:' + path + '?mode=ro', uri=True)
    c.text_factory = bytes
    out = {}
    names = [r[0].decode() for r in c.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' ORDER BY name")]
    for t in names:
        q = '"' + t.replace('"', '""') + '"'
        sqltext = c.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", (t,)).fetchone()[0].decode()
        cols0 = [(r[1].decode(), (r[2] or b'').decode(), r[3], (r[4] or b'').decode() if r[4] is not None else None, r[5], r[6]) for r in c.execute(f"PRAGMA table_xinfo({q})")]
        cols = cols0
        if SEM:
            cols = [(n_, d_, 1 if (pk_ and d_.upper() == 'INTEGER' and sum(1 for z in cols0 if z[4]) == 1) else nn_, None if (df_ or '').upper() == 'NULL' else df_, pk_, hid_) for (n_, d_, nn_, df_, pk_, hid_) in cols0]
        fkrows = c.execute(f"PRAGMA foreign_key_list({q})").fetchall()
        fks = collections.defaultdict(list)
        for r in fkrows:
            fks[r[0]].append(r)
        fkl = []
        for k, rr in fks.items():
            rr.sort(key=lambda r: r[1])
            fkl.append(json.dumps([[x[3].decode() for x in rr], rr[0][2].decode(), [(x[4] or b'').decode() for x in rr], rr[0][5].decode(), rr[0][6].decode()]))
        deferrable = len(re.findall(r'(?i)\bDEFERRABLE\b', sqltext)) - len(re.findall(r'(?i)\bNOT\s+DEFERRABLE\b', sqltext))
        idx = []
        for r in c.execute(f"PRAGMA index_list({q})").fetchall():
            name, uniq, origin, partial = r[1].decode(), r[2], r[3].decode(), r[4]
            xi = c.execute('PRAGMA index_xinfo("' + name.replace('"','""') + '")').fetchall()
            keycols = [((x[1] is not None and x[1] >= 0 and x[2] is not None) and x[2].decode() or '<expr>') + (' DESC' if x[3] else '') + (' COLLATE ' + x[4].decode() if x[4] and x[4] != b'BINARY' else '') for x in xi if x[5]]
            isql = c.execute("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", (name,)).fetchone()
            where = ''
            if isql and isql[0]:
                m = re.search(r'(?is)\bWHERE\b(.*)$', isql[0].decode())
                where = m.group(1).strip() if m else ''
            idx.append(json.dumps([name if origin == 'c' else '<' + origin + '>', uniq, origin, partial, keycols, where]))
        checks = len(re.findall(r'(?i)\bCHECK\s*\(', sqltext))
        autoinc = bool(re.search(r'(?i)AUTOINCREMENT', sqltext))
        n = c.execute(f"SELECT count(*) FROM {q}").fetchone()[0]
        colnames = [x[0] for x in cols if x[5] != 1]
        sel = ', '.join(f'typeof("{x.replace(chr(34), chr(34)*2)}"), "{x.replace(chr(34), chr(34)*2)}"' for x in colnames)
        colvals = [[] for _ in colnames]
        rowsr = []
        for r in c.execute(f"SELECT {sel} FROM {q}"):
            rendered = [rs(r[2*i].decode(), r[2*i+1]) for i in range(len(colnames))]
            for i, v in enumerate(rendered):
                colvals[i].append(v)
            rowsr.append('\x1f'.join(rendered))
        seq = c.execute("SELECT seq FROM sqlite_sequence WHERE name=?", (t,)).fetchone() if c.execute("SELECT 1 FROM sqlite_master WHERE name='sqlite_sequence'").fetchone() else None
        out[t] = {
            'columns': cols, 'fks': sorted(fkl), 'deferrable': deferrable, 'indexes': sorted(idx), 'checks': checks,
            'autoinc': autoinc, 'rows': n, 'seq': seq[0] if seq else None,
            'colsum': {colnames[i]: h(colvals[i]) for i in range(len(colnames))}, 'rowsum': h(rowsr),
        }
    return out

def pg_snap(url, schema='public'):
    import psycopg
    c = psycopg.connect(url)
    c.execute("SET DateStyle=ISO; SET IntervalStyle=postgres; SET extra_float_digits=3; SET TimeZone=UTC")
    out = {}
    tabs = [r[0] for r in c.execute("SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=%s AND c.relkind IN ('r','p') ORDER BY 1", (schema,))]
    for t in tabs:
        q = '"' + schema.replace('"', '""') + '"."' + t.replace('"', '""') + '"'
        oid = c.execute("SELECT %s::regclass::oid", (q,)).fetchone()[0]
        cols = c.execute("""SELECT a.attname, format_type(a.atttypid,a.atttypmod), a.attnotnull, pg_get_expr(d.adbin,d.adrelid), a.attidentity, a.attgenerated, t.typname
          FROM pg_attribute a JOIN pg_type t ON t.oid=a.atttypid LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
          WHERE a.attrelid=%s AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum""", (oid,)).fetchall()
        cons = []
        for r in c.execute("""SELECT con.contype, con.conname,
             (SELECT array_agg(a.attname ORDER BY k.ord) FROM unnest(con.conkey) WITH ORDINALITY k(n,ord) JOIN pg_attribute a ON a.attrelid=con.conrelid AND a.attnum=k.n),
             con.confrelid::regclass::text,
             (SELECT array_agg(a.attname ORDER BY k.ord) FROM unnest(con.confkey) WITH ORDINALITY k(n,ord) JOIN pg_attribute a ON a.attrelid=con.confrelid AND a.attnum=k.n),
             con.confupdtype, con.confdeltype, con.condeferrable, con.condeferred, con.convalidated, pg_get_constraintdef(con.oid)
             FROM pg_constraint con WHERE con.conrelid=%s ORDER BY 1,2""", (oid,)):
            r = list(r)
            if r[0] == 'c':
                r[2] = sorted(r[2] or [])
            cons.append(json.dumps(r, default=str))
        idx = [r[0] for r in c.execute("""SELECT pg_get_indexdef(i.indexrelid) FROM pg_index i
              WHERE i.indrelid=%s AND NOT EXISTS (SELECT 1 FROM pg_constraint x WHERE x.conindid=i.indexrelid AND x.contype IN ('p','u','x')) ORDER BY 1""", (oid,))]
        n = c.execute(f"SELECT count(*) FROM {q}").fetchone()[0]
        colnames = [x[0] for x in cols]
        typnames = [x[6] for x in cols]
        sel = ', '.join('"' + x.replace('"', '""') + '"::text' for x in colnames)
        colvals = [[] for _ in colnames]
        rowsr = []
        with c.cursor(name='s') as cur:
            cur.itersize = 5000
            cur.execute(f"SELECT {sel} FROM {q}")
            for r in cur:
                rendered = [rs(typnames[i], r[i]) for i in range(len(colnames))]
                for i, v in enumerate(rendered):
                    colvals[i].append(v)
                rowsr.append('\x1f'.join(rendered))
        c.commit()
        out[t] = {'columns': cols, 'constraints': sorted(cons), 'indexes': idx, 'rows': n,
                  'colsum': {colnames[i]: h(colvals[i]) for i in range(len(colnames))}, 'rowsum': h(rowsr)}
    return out

def snap(spec):
    kind, _, rest = spec.partition(':')
    if kind == 'sqlite':
        return sqlite_snap(rest)
    url, _, schema = rest.partition('#')
    return pg_snap(url, schema or 'public')

def main():
    a, b = snap(sys.argv[1]), snap(sys.argv[2])
    ignore_idx_names = '--ignore-index-names' in sys.argv
    diffs = 0
    for t in sorted(set(a) | set(b)):
        if t not in a or t not in b:
            print(f'TABLE {t}: only in {"A" if t in a else "B"}'); diffs += 1; continue
        for k in a[t]:
            va, vb = a[t][k], b[t][k]
            if k == 'colsum':
                for col in sorted(set(va) | set(vb)):
                    if va.get(col) != vb.get(col):
                        print(f'{t}.{col}: column checksum differs'); diffs += 1
                continue
            if va != vb:
                if k == 'rowsum' and a[t]['colsum'] == b[t]['colsum']:
                    print(f'{t}: row checksum differs but every column matches (row pairing)'); diffs += 1; continue
                if k == 'rowsum':
                    continue
                print(f'{t}.{k}:\n   A={va}\n   B={vb}'); diffs += 1
    tot = sum(x['rows'] for x in a.values())
    print(f'tables A={len(a)} B={len(b)} rowsA={tot} diffs={diffs}')
    sys.exit(1 if diffs else 0)

if __name__ == '__main__':
    main()
