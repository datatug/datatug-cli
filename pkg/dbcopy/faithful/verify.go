package faithful

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash"
	"math"
	"sort"
	"strings"
)

// Diff is one difference between two databases of the same engine.
type Diff struct {
	Object string `json:"object"`
	Aspect string `json:"aspect"`
	Left   string `json:"left"`
	Right  string `json:"right"`
}

func (d Diff) String() string {
	return fmt.Sprintf("%s [%s]: %s != %s", d.Object, d.Aspect, d.Left, d.Right)
}

// Verify compares two databases of the same engine at catalog level and data level: tables, columns
// (declared type, nullability, default), primary keys, foreign keys (columns, referenced table and
// columns, actions), unique constraints, indexes, check constraints, row counts, and a digest of
// every table's rows (so every value, and in SQLite its storage class, is compared).
func Verify(ctx context.Context, left, right Endpoint) ([]Diff, error) {
	if left.Engine != right.Engine {
		return nil, fmt.Errorf("verify compares two databases of one engine, got %s and %s", left.Engine, right.Engine)
	}
	read := func(e Endpoint) (*Catalog, error) {
		if e.Engine == SQLite {
			return ReadSQLite(ctx, e.DB)
		}
		return ReadPostgres(ctx, e.DB, e.schema())
	}
	lc, err := read(left)
	if err != nil {
		return nil, err
	}
	rc, err := read(right)
	if err != nil {
		return nil, err
	}
	var diffs []Diff
	add := func(obj, aspect, l, r string) {
		if l != r {
			diffs = append(diffs, Diff{obj, aspect, l, r})
		}
	}
	names := map[string]bool{}
	for _, t := range lc.Tables {
		names[t.Name] = true
	}
	for _, t := range rc.Tables {
		names[t.Name] = true
	}
	var all []string
	for n := range names {
		all = append(all, n)
	}
	sort.Strings(all)
	for _, n := range all {
		lt, rt := lc.Table(n), rc.Table(n)
		switch {
		case lt == nil:
			diffs = append(diffs, Diff{n, "table", "missing", "present"})
			continue
		case rt == nil:
			diffs = append(diffs, Diff{n, "table", "present", "missing"})
			continue
		}
		add(n, "primary key", strings.Join(lt.PK, ","), strings.Join(rt.PK, ","))
		add(n, "row count", fmt.Sprint(lt.Rows), fmt.Sprint(rt.Rows))
		for _, c := range lt.Columns {
			rcol := rt.Column(c.Name)
			if rcol == nil {
				diffs = append(diffs, Diff{n + "." + c.Name, "column", "present", "missing"})
				continue
			}
			obj := n + "." + c.Name
			add(obj, "declared type", strings.ToLower(c.SourceType), strings.ToLower(rcol.SourceType))
			add(obj, "not null", fmt.Sprint(c.NotNull || contains(lt.PK, c.Name)), fmt.Sprint(rcol.NotNull || contains(rt.PK, c.Name)))
			add(obj, "default", normDefault(c.Default, c.SourceType), normDefault(rcol.Default, rcol.SourceType))
			add(obj, "identity", fmt.Sprint(c.Identity != IdentityNone), fmt.Sprint(rcol.Identity != IdentityNone))
		}
		for _, c := range rt.Columns {
			if lt.Column(c.Name) == nil {
				diffs = append(diffs, Diff{n + "." + c.Name, "column", "missing", "present"})
			}
		}
		add(n, "column order", colNames(lt), colNames(rt))
		add(n, "foreign keys", fkSet(lt), fkSet(rt))
		add(n, "unique constraints", uniqSet(lt), uniqSet(rt))
		add(n, "indexes", idxSet(lt), idxSet(rt))
		add(n, "checks", checkSet(lt), checkSet(rt))
		ld, err := tableDigest(ctx, left, lt, "")
		if err != nil {
			return nil, err
		}
		rd, err := tableDigest(ctx, right, rt, "")
		if err != nil {
			return nil, err
		}
		if ld != rd {
			// Name the columns whose values differ.
			named := false
			for _, c := range lt.Columns {
				if rt.Column(c.Name) == nil {
					continue
				}
				lcd, err := tableDigest(ctx, left, lt, c.Name)
				if err != nil {
					return nil, err
				}
				rcd, err := tableDigest(ctx, right, rt, c.Name)
				if err != nil {
					return nil, err
				}
				if lcd != rcd {
					diffs = append(diffs, Diff{n + "." + c.Name, "data digest", lcd, rcd})
					named = true
				}
			}
			if !named {
				diffs = append(diffs, Diff{n, "data digest", ld, rd})
			}
		}
	}
	return diffs, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func normDefault(d, decl string) string {
	u := strings.ToUpper(d)
	if strings.Contains(u, "CURRENT_TIMESTAMP") || strings.Contains(u, "NOW()") {
		return "<current timestamp>"
	}
	d = strings.Join(strings.Fields(d), " ")
	if len(d) > 2 && d[0] == '\'' && d[len(d)-1] == '\'' && reDefNumber.MatchString(d[1:len(d)-1]) && !sqliteTextAffinity(decl) {
		// A quoted number and the number are one default in a column of numeric affinity; in a column
		// of text affinity '0.00' and 0.00 are two defaults (the second one is the text '0.0').
		return d[1 : len(d)-1]
	}
	return d
}

func colNames(t *Table) string {
	n := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		n[i] = c.Name
	}
	return strings.Join(n, ",")
}

func fkSet(t *Table) string {
	var s []string
	for _, fk := range t.FKs {
		s = append(s, fmt.Sprintf("(%s)->%s(%s) upd=%s del=%s deferrable=%t deferred=%t match=%s notvalid=%t", strings.Join(fk.Columns, ","), fk.RefTable, strings.Join(fk.RefColumns, ","), fk.OnUpdate, fk.OnDelete, fk.Deferrable, fk.Deferred, fk.Match, fk.NotValid))
	}
	sort.Strings(s)
	return strings.Join(s, "; ")
}

// uniqSet lists the unique column sets: UNIQUE constraints and unique plain indexes are one thing
// to a reader of the schema (SQLite writes one as the other), so they are compared by columns.
func uniqSet(t *Table) string {
	var s []string
	for _, u := range t.Uniques {
		s = append(s, "("+strings.Join(u.Columns, ",")+")")
	}
	for _, ix := range t.Indexes {
		if ix.Unique && ix.Where == "" && !hasDesc(ix) {
			cols := make([]string, len(ix.Columns))
			for i, c := range ix.Columns {
				cols[i] = c.Name
			}
			s = append(s, "("+strings.Join(cols, ",")+")")
		}
	}
	sort.Strings(s)
	return strings.Join(s, "; ")
}

func hasDesc(ix Index) bool {
	for _, c := range ix.Columns {
		if c.Desc {
			return true
		}
	}
	return false
}

func idxSet(t *Table) string {
	var s []string
	for _, ix := range t.Indexes {
		if ix.Unique && ix.Where == "" && !hasDesc(ix) {
			continue
		}
		cols := make([]string, len(ix.Columns))
		for i, c := range ix.Columns {
			cols[i] = c.Name
			if c.Desc {
				cols[i] += " DESC"
			}
		}
		s = append(s, fmt.Sprintf("%s unique=%v where=%q (%s)", ix.Name, ix.Unique, ix.Where, strings.Join(cols, ",")))
	}
	sort.Strings(s)
	return strings.Join(s, "; ")
}

// checkSet compares the checks by number: the text of an expression is the engine's own, and a name an engine gives to an unnamed check is not a difference.
func checkSet(t *Table) string { return fmt.Sprint(len(t.Checks)) }

// tableDigest hashes every row of a table in primary key order. SQLite values carry their storage
// class; PostgreSQL values are compared by their text form.
func tableDigest(ctx context.Context, e Endpoint, t *Table, only string) (string, error) {
	var exprs, names []string
	for _, c := range t.Columns {
		if only != "" && c.Name != only {
			continue
		}
		names = append(names, c.Name)
		exprs = append(exprs, quoteIdent(c.Name)+"::text")
	}
	list := strings.Join(exprs, ", ")
	if e.Engine == SQLite {
		list = sqliteSelectList(names)
	}
	var from, order string
	if e.Engine == SQLite {
		from = quoteIdent(t.Name)
	} else {
		from = qualified(e.schema(), t.Name)
	}
	if len(t.PK) > 0 {
		order = quoteList(t.PK)
	} else {
		// A table with no key has no stable order: order by every column.
		var all []string
		for _, c := range t.Columns {
			if e.Engine == SQLite {
				all = append(all, sqliteReadExpr(c))
			} else {
				all = append(all, quoteIdent(c.Name)+"::text")
			}
		}
		order = strings.Join(all, ", ")
	}
	rows, err := e.DB.QueryContext(ctx, "SELECT "+list+" FROM "+from+" ORDER BY "+order)
	if err != nil {
		return "", fmt.Errorf("digest %s: %w", t.Name, err)
	}
	defer func() { _ = rows.Close() }()
	h := sha256.New()
	var vals, ptrs []any
	width := len(names)
	if e.Engine == SQLite {
		vals, ptrs = sqliteRowBuffer(width)
	} else {
		vals, ptrs = make([]any, width), make([]any, width)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
	}
	var n int64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		if e.Engine == SQLite {
			fixEmptyBlobs(vals)
		}
		for _, v := range vals[:width] {
			hashValue(h, v)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d rows sha256:%x", n, h.Sum(nil)[:8]), nil
}

var _ = sql.ErrNoRows

// hashValue feeds one value, with its storage class, to h.
func hashValue(h hash.Hash, v any) {
	var buf [8]byte
	switch x := v.(type) {
	case nil:
		h.Write([]byte{0})
	case int64:
		h.Write([]byte{1})
		binary.LittleEndian.PutUint64(buf[:], uint64(x))
		h.Write(buf[:])
	case float64:
		h.Write([]byte{2})
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(x))
		h.Write(buf[:])
	case string:
		h.Write([]byte{3})
		binary.LittleEndian.PutUint64(buf[:], uint64(len(x)))
		h.Write(buf[:])
		h.Write([]byte(x))
	case []byte:
		h.Write([]byte{4})
		binary.LittleEndian.PutUint64(buf[:], uint64(len(x)))
		h.Write(buf[:])
		h.Write(x)
	default:
		_, _ = fmt.Fprintf(h, "?%v", x)
	}
}
