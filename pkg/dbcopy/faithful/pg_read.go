package faithful

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const unitSep = "\x1f"

func splitSep(s sql.NullString) []string {
	if !s.Valid || s.String == "" {
		return nil
	}
	return strings.Split(s.String, unitSep)
}

var pgActions = map[string]string{"a": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

// ReadPostgres reads the catalog of one PostgreSQL schema (the caller says which: Endpoint.Schema defaults to public).
func ReadPostgres(ctx context.Context, db Queryer, schema string) (*Catalog, error) {
	cat := &Catalog{Engine: Postgres, Schema: schema}
	rows, err := db.QueryContext(ctx, `
SELECT c.oid::bigint, c.relname, c.relkind::text, c.relispartition, c.relrowsecurity
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p','v','m','f')
ORDER BY c.relname`, schema)
	if err != nil {
		return nil, fmt.Errorf("read pg_class: %w", err)
	}
	type rel struct {
		oid                 int64
		name, kind          string
		partition, rowsecur bool
	}
	var rels []rel
	for rows.Next() {
		var r rel
		if err := rows.Scan(&r.oid, &r.name, &r.kind, &r.partition, &r.rowsecur); err != nil {
			_ = rows.Close()
			return nil, err
		}
		rels = append(rels, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	oids := map[string]int64{}
	for _, r := range rels {
		switch r.kind {
		case "v":
			cat.Notes = append(cat.Notes, Warning{WarnViewNotCarried, r.name, "views are written in the SQL dialect of their engine and are not translated across engines"})
		case "m":
			cat.Notes = append(cat.Notes, Warning{WarnViewNotCarried, r.name, "materialized view is not carried"})
		case "f":
			cat.Notes = append(cat.Notes, Warning{WarnObjectNotCarried, r.name, "foreign table is not carried"})
		default:
			if r.partition {
				cat.Notes = append(cat.Notes, Warning{WarnStorageOption, r.name, "partition of a partitioned table is not carried as a partition; its rows are carried by the parent table"})
				continue
			}
			if r.kind == "p" {
				cat.Notes = append(cat.Notes, Warning{WarnStorageOption, r.name, "partitioned table is carried as one ordinary table"})
			}
			if r.rowsecur {
				cat.Notes = append(cat.Notes, Warning{WarnObjectNotCarried, r.name, "row level security policies are not carried"})
			}
			cat.Tables = append(cat.Tables, &Table{Name: r.name})
			oids[r.name] = r.oid
		}
	}
	for _, t := range cat.Tables {
		if err := readPGTable(ctx, db, cat, t, oids[t.Name]); err != nil {
			return nil, fmt.Errorf("read table %q: %w", t.Name, err)
		}
	}
	return cat, nil
}

func readPGTable(ctx context.Context, db Queryer, cat *Catalog, t *Table, oid int64) error {
	cols, err := db.QueryContext(ctx, `
SELECT a.attnum, a.attname, format_type(a.atttypid, a.atttypmod), tt.typname, tt.typtype::text, tt.typcategory::text,
       a.attnotnull, COALESCE(pg_get_expr(d.adbin, d.adrelid), ''), a.attidentity::text, a.attgenerated::text,
       COALESCE(col_description(a.attrelid, a.attnum), ''),
       (a.attcollation <> 0 AND a.attcollation <> tt.typcollation)
FROM pg_attribute a
JOIN pg_type tt ON tt.oid = a.atttypid
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`, oid)
	if err != nil {
		return err
	}
	attnums := map[string]int{}
	for cols.Next() {
		var attnum int
		var name, typ, typname, typtype, typcat, identity, generated, comment string
		var notnull, collation bool
		var def string
		if err := cols.Scan(&attnum, &name, &typ, &typname, &typtype, &typcat, &notnull, &def, &identity, &generated, &comment, &collation); err != nil {
			_ = cols.Close()
			return err
		}
		c := &Column{Name: name, SourceType: typ, NotNull: notnull, Default: def, Precision: -1, Scale: -1}
		classifyPGType(c, typ, typname, typtype, typcat)
		attnums[name] = attnum
		if identity != "" {
			c.Identity = IdentityGenerated
		}
		if generated != "" {
			c.Generated = def
			c.Default = ""
			cat.Notes = append(cat.Notes, Warning{WarnGeneratedColumn, t.Name + "." + name,
				"generated column: the expression is not carried, its values are copied as an ordinary column"})
		}
		if collation {
			cat.Notes = append(cat.Notes, Warning{WarnCollationNotCarried, t.Name + "." + name, "column collation is not carried"})
		}
		if strings.HasPrefix(comment, markerPrefix) {
			var m Marker
			if json.Unmarshal([]byte(strings.TrimPrefix(comment, markerPrefix)), &m) == nil {
				c.Marker = &m
			} else {
				c.Comment = comment
			}
		} else {
			c.Comment = comment
		}
		t.Columns = append(t.Columns, c)
	}
	if err := cols.Err(); err != nil {
		return err
	}
	_ = cols.Close()

	// A serial / identity column: the sequence behind it and its position.
	for _, c := range t.Columns {
		if c.Identity == IdentityNone && strings.HasPrefix(c.Default, "nextval(") {
			c.Identity = IdentityGenerated
			c.Default = ""
		}
		if c.Identity == IdentityGenerated {
			if err := readSequence(ctx, db, cat, t, c, oid, attnums[c.Name]); err != nil {
				return err
			}
		}
	}

	// Constraints.
	cons, err := db.QueryContext(ctx, `
SELECT c.conname, c.contype::text,
  (SELECT string_agg(a.attname, E'\037' ORDER BY k.ord) FROM unnest(c.conkey) WITH ORDINALITY k(n, ord) JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.n),
  COALESCE(rn.nspname, ''), COALESCE(rc.relname, ''),
  (SELECT string_agg(a.attname, E'\037' ORDER BY k.ord) FROM unnest(c.confkey) WITH ORDINALITY k(n, ord) JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.n),
  c.confupdtype::text, c.confdeltype::text, c.condeferrable, c.condeferred, c.convalidated, pg_get_constraintdef(c.oid), c.confmatchtype::text
FROM pg_constraint c
LEFT JOIN pg_class rc ON rc.oid = c.confrelid
LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace
WHERE c.conrelid = $1 ORDER BY c.conname`, oid)
	if err != nil {
		return err
	}
	for cons.Next() {
		var name, typ, refSchema, refTable, upd, del, def, match string
		var colsText, refCols sql.NullString
		var deferrable, deferred, validated bool
		if err := cons.Scan(&name, &typ, &colsText, &refSchema, &refTable, &refCols, &upd, &del, &deferrable, &deferred, &validated, &def, &match); err != nil {
			_ = cons.Close()
			return err
		}
		obj := t.Name + "." + name
		switch typ {
		case "p":
			t.PKName = name
			t.PK = splitSep(colsText)
		case "u":
			t.Uniques = append(t.Uniques, Unique{Name: name, Columns: splitSep(colsText)})
		case "f":
			if refSchema != cat.Schema {
				cat.Notes = append(cat.Notes, Warning{WarnFKNotCarried, obj, fmt.Sprintf("foreign key references table %q.%q in another schema; only one schema is copied", refSchema, refTable)})
				continue
			}
			if match == "f" {
				cat.Notes = append(cat.Notes, Warning{WarnFKNotCarried, obj, "MATCH FULL is not carried; the foreign key is carried with the default match"})
			}
			t.FKs = append(t.FKs, ForeignKey{Name: name, Columns: splitSep(colsText), RefTable: refTable, RefColumns: splitSep(refCols),
				OnUpdate: pgActions[upd], OnDelete: pgActions[del], Deferrable: deferrable, Deferred: deferred, NotValid: !validated, Match: map[bool]string{true: "FULL"}[match == "f"]})
		case "c":
			t.Checks = append(t.Checks, Check{Name: name, Expr: pgCheckExpr(def)})
		case "x":
			cat.Notes = append(cat.Notes, Warning{WarnObjectNotCarried, obj, "exclusion constraint is not carried"})
		}
	}
	if err := cons.Err(); err != nil {
		return err
	}
	_ = cons.Close()

	// Indexes that are not behind a constraint.
	idx, err := db.QueryContext(ctx, `
SELECT ic.relname, i.indisunique, am.amname, i.indpred IS NOT NULL, COALESCE(pg_get_expr(i.indpred, i.indrelid), ''),
  i.indnkeyatts, i.indnatts,
  (SELECT string_agg(CASE WHEN k.n = 0 THEN '' ELSE a.attname END, E'\037' ORDER BY k.ord)
     FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(n, ord) LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.n),
  array_to_string(i.indoption::int2[], ','), i.indexprs IS NOT NULL,
  EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid AND c.contype IN ('p','u','x'))
FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid JOIN pg_am am ON am.oid = ic.relam
WHERE i.indrelid = $1 ORDER BY ic.relname`, oid)
	if err != nil {
		return err
	}
	for idx.Next() {
		var name, am, where, options string
		var unique, partial, hasExpr, backs bool
		var nkey, natts int
		var colsText sql.NullString
		if err := idx.Scan(&name, &unique, &am, &partial, &where, &nkey, &natts, &colsText, &options, &hasExpr, &backs); err != nil {
			_ = idx.Close()
			return err
		}
		if backs {
			continue
		}
		obj := t.Name + "." + name
		names := splitSep(colsText)
		switch {
		case am != "btree":
			cat.Notes = append(cat.Notes, Warning{WarnIndexNotCarried, obj, fmt.Sprintf("%s index is not carried (only btree indexes are)", am)})
			continue
		case hasExpr:
			cat.Notes = append(cat.Notes, Warning{WarnIndexNotCarried, obj, "index on an expression is not carried"})
			continue
		case natts > nkey:
			cat.Notes = append(cat.Notes, Warning{WarnIndexNotCarried, obj, "index with INCLUDE columns is not carried"})
			continue
		}
		opts := strings.Split(options, ",")
		var ic []IndexColumn
		for i := 0; i < nkey && i < len(names); i++ {
			flags, _ := strconv.Atoi(opts[i])
			ic = append(ic, IndexColumn{Name: names[i], Desc: flags&1 != 0})
		}
		t.Indexes = append(t.Indexes, Index{Name: name, Unique: unique, Columns: ic, Where: where})
	}
	if err := idx.Err(); err != nil {
		return err
	}
	_ = idx.Close()

	var trg []string
	trows, err := db.QueryContext(ctx, `SELECT tgname FROM pg_trigger WHERE tgrelid = $1 AND NOT tgisinternal`, oid)
	if err != nil {
		return err
	}
	for trows.Next() {
		var n string
		if err := trows.Scan(&n); err != nil {
			_ = trows.Close()
			return err
		}
		trg = append(trg, n)
	}
	_ = trows.Close()
	for _, n := range trg {
		cat.Notes = append(cat.Notes, Warning{WarnTriggerNotCarried, t.Name + "." + n, "triggers are not carried across engines"})
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+quoteIdent(cat.Schema)+`.`+quoteIdent(t.Name)).Scan(&t.Rows); err != nil {
		return err
	}
	return nil
}

var pgCheckPrefix = regexp.MustCompile(`(?is)^CHECK\s*\(`)

// pgCheckExpr extracts the expression of "CHECK ((...)) [NO INHERIT] [NOT VALID]".
func pgCheckExpr(def string) string {
	loc := pgCheckPrefix.FindStringIndex(def)
	if loc == nil {
		return def
	}
	open := loc[1] - 1
	end := matchParen(def, open)
	if end < 0 {
		return def
	}
	return strings.TrimSpace(def[open+1 : end])
}

var (
	pgNumericArgs = regexp.MustCompile(`\((\d+)(?:,\s*(\d+))?\)`)
)

// classifyPGType sets Kind, Width, Length, Precision and Scale of c from the PostgreSQL type text.
func classifyPGType(c *Column, formatted, typname, typtype, typcat string) {
	f := strings.ToLower(formatted)
	args := pgNumericArgs.FindStringSubmatch(f)
	switch {
	case typcat == "A":
		c.Kind = KindOther
	case typtype == "e", typtype == "c", typtype == "r", typtype == "d":
		c.Kind = KindOther
	case f == "smallint":
		c.Kind, c.Width = KindInt, 2
	case f == "integer":
		c.Kind, c.Width = KindInt, 4
	case f == "bigint":
		c.Kind, c.Width = KindInt, 8
	case f == "real":
		c.Kind, c.Width = KindFloat, 4
	case f == "double precision":
		c.Kind, c.Width = KindFloat, 8
	case strings.HasPrefix(f, "numeric"):
		c.Kind = KindDecimal
		if args != nil {
			c.Precision, _ = strconv.Atoi(args[1])
			c.Scale = 0
			if args[2] != "" {
				c.Scale, _ = strconv.Atoi(args[2])
			}
		}
	case f == "boolean":
		c.Kind = KindBool
	case f == "json", f == "jsonb", f == "uuid":
		c.Kind, c.Special = KindOther, f
	case f == "text", f == "name":
		c.Kind = KindText
	case strings.HasPrefix(f, "character varying"):
		c.Kind = KindText
		if args != nil {
			c.Length, _ = strconv.Atoi(args[1])
		}
	case strings.HasPrefix(f, "character("):
		c.Kind, c.Fixed = KindText, true
		if args != nil {
			c.Length, _ = strconv.Atoi(args[1])
		}
	case f == "character":
		c.Kind, c.Fixed, c.Length = KindText, true, 1
	case f == "bytea":
		c.Kind = KindBytes
	case f == "date":
		c.Kind = KindDate
	case strings.HasPrefix(f, "timestamp") && strings.Contains(f, "with time zone"):
		c.Kind = KindTimestampTZ
	case strings.HasPrefix(f, "timestamp"):
		c.Kind = KindTimestamp
	case strings.HasPrefix(f, "time") && !strings.Contains(f, "with time zone"):
		c.Kind = KindTime
	default:
		c.Kind = KindOther
	}
	_ = typname
}

// readSequence finds the sequence behind an identity / serial column and reads the value it gives next.
// A sequence that was never called has not handed out its start value yet (is_called is false), and
// pg_sequences.last_value is NULL for it: the sequence relation itself says where it is.
func readSequence(ctx context.Context, db Queryer, cat *Catalog, t *Table, c *Column, tableOID int64, attnum int) error {
	var seqSchema, seq sql.NullString
	err := db.QueryRowContext(ctx, `
SELECT sn.nspname, s.relname FROM pg_depend d JOIN pg_class s ON s.oid = d.objid AND s.relkind = 'S' JOIN pg_namespace sn ON sn.oid = s.relnamespace
WHERE d.refobjid = $1 AND d.refobjsubid = $2 AND d.deptype IN ('a','i') LIMIT 1`, tableOID, attnum).Scan(&seqSchema, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no sequence behind the column: nothing to carry
	}
	if err != nil {
		return fmt.Errorf("find the sequence of %s.%s: %w", t.Name, c.Name, err)
	}
	qn := quoteIdent(seqSchema.String) + "." + quoteIdent(seq.String)
	var last int64
	var called bool
	if err := db.QueryRowContext(ctx, `SELECT last_value, is_called FROM `+qn).Scan(&last, &called); err != nil {
		return fmt.Errorf("read the sequence %s: %w", qn, err)
	}
	c.SequenceStart = last
	if called {
		c.SequenceStart = last + 1
	}
	var start, step int64
	if err := db.QueryRowContext(ctx, `SELECT seqstart, seqincrement FROM pg_sequence s JOIN pg_class c ON c.oid = s.seqrelid JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2`,
		seqSchema.String, seq.String).Scan(&start, &step); err != nil {
		return fmt.Errorf("read the definition of the sequence %s: %w", qn, err)
	}
	if start != 1 || step != 1 {
		source := last // what the source gives next: one step after the last value, or the start
		if called {
			source = last + step
		}
		cat.Notes = append(cat.Notes, Warning{WarnIdentityNotCarried, t.Name + "." + c.Name,
			fmt.Sprintf("the sequence starts at %d and steps by %d; the next value of the source is %d, the target counts on from %d in steps of 1 (the start and the step are not carried)", start, step, source, c.SequenceStart)})
	}
	return nil
}
