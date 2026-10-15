package faithful

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ReadSQLite reads the catalog of a SQLite database: its tables with columns, primary keys,
// foreign keys, unique constraints, indexes and check constraints. Views, triggers, virtual tables
// and the like are recorded as notes (warnings) because they cannot be carried across engines.
func ReadSQLite(ctx context.Context, db Queryer) (*Catalog, error) {
	cat := &Catalog{Engine: SQLite, Schema: "main"}

	type master struct{ typ, name, tbl, sql string }
	rows, err := db.QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("read sqlite_master: %w", err)
	}
	var objects []master
	for rows.Next() {
		var m master
		if err := rows.Scan(&m.typ, &m.name, &m.tbl, &m.sql); err != nil {
			_ = rows.Close()
			return nil, err
		}
		objects = append(objects, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close()

	withoutRowid, strict := map[string]bool{}, map[string]bool{}
	if tl, err := db.QueryContext(ctx, `SELECT name, wr, strict FROM pragma_table_list WHERE schema = 'main'`); err == nil {
		for tl.Next() {
			var n string
			var wr, st int
			if tl.Scan(&n, &wr, &st) == nil {
				withoutRowid[n], strict[n] = wr == 1, st == 1
			}
		}
		_ = tl.Close()
	}

	tableSQL := map[string]string{}
	for _, o := range objects {
		if o.typ != "table" && o.sql != "" {
			cat.Objects = append(cat.Objects, RawObject{Type: o.typ, Name: o.name, Table: o.tbl, SQL: o.sql})
		}
		switch o.typ {
		case "table":
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(o.sql)), "CREATE VIRTUAL") {
				cat.Notes = append(cat.Notes, Warning{WarnObjectNotCarried, o.name, "virtual table is not carried (it has no portable definition)"})
				continue
			}
			tableSQL[o.name] = o.sql
			cat.Tables = append(cat.Tables, &Table{Name: o.name})
		case "view":
			cat.Notes = append(cat.Notes, Warning{WarnViewNotCarried, o.name, "views are written in the SQL dialect of their engine and are not translated across engines"})
		case "trigger":
			cat.Notes = append(cat.Notes, Warning{WarnTriggerNotCarried, o.name, "triggers are not carried across engines"})
		}
	}

	for _, t := range cat.Tables {
		t.CreateSQL = tableSQL[t.Name]
		if err := readSQLiteTable(ctx, db, cat, t, tableSQL[t.Name], withoutRowid[t.Name], strict[t.Name]); err != nil {
			return nil, fmt.Errorf("read table %q: %w", t.Name, err)
		}
	}
	// The position of each AUTOINCREMENT counter.
	if sq, err := db.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence`); err == nil {
		for sq.Next() {
			var name string
			var seq int64
			if sq.Scan(&name, &seq) == nil {
				if t := cat.Table(name); t != nil && len(t.PK) == 1 {
					if c := t.Column(t.PK[0]); c != nil {
						c.SequenceStart = seq + 1
					}
				}
			}
		}
		_ = sq.Close()
	}
	// A foreign key that names no column references the primary key of the parent.
	for _, t := range cat.Tables {
		for i := range t.FKs {
			fk := &t.FKs[i]
			if len(fk.RefColumns) == 0 || fk.RefColumns[0] == "" {
				if parent := cat.Table(fk.RefTable); parent != nil {
					fk.RefColumns = append([]string(nil), parent.PK...)
				}
			}
		}
	}
	return cat, nil
}

func readSQLiteTable(ctx context.Context, db Queryer, cat *Catalog, t *Table, createSQL string, withoutRowid, strict bool) error {
	cols, err := db.QueryContext(ctx, `PRAGMA table_xinfo(`+quoteIdent(t.Name)+`)`)
	if err != nil {
		return err
	}
	type pkCol struct {
		name string
		pos  int
	}
	var pks []pkCol
	for cols.Next() {
		var cid, notnull, pk, hidden int
		var name, typ string
		var dflt sql.NullString
		if err := cols.Scan(&cid, &name, &typ, &notnull, &dflt, &pk, &hidden); err != nil {
			_ = cols.Close()
			return err
		}
		if hidden == 1 {
			continue
		}
		c := &Column{Name: name, SourceType: typ, NotNull: notnull != 0, Precision: -1, Scale: -1}
		if dflt.Valid && !strings.EqualFold(strings.TrimSpace(dflt.String), "NULL") {
			c.Default = dflt.String
		}
		if hidden == 2 || hidden == 3 {
			c.Generated = "generated"
			cat.Notes = append(cat.Notes, Warning{WarnGeneratedColumn, t.Name + "." + name,
				"generated column: the expression is not carried, its values are copied as an ordinary column"})
		}
		t.Columns = append(t.Columns, c)
		if pk > 0 {
			pks = append(pks, pkCol{name, pk})
		}
	}
	if err := cols.Err(); err != nil {
		return err
	}
	_ = cols.Close()
	sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
	for _, p := range pks {
		t.PK = append(t.PK, p.name)
	}
	if len(t.PK) == 1 && !withoutRowid {
		if c := t.Column(t.PK[0]); c != nil && strings.EqualFold(strings.TrimSpace(c.SourceType), "INTEGER") {
			c.Identity = IdentityRowid
			if autoincrementWord.MatchString(createSQL) {
				c.Identity = IdentityAutoincrement
			}
		}
	}
	// Name of the primary key constraint, when the statement gives one.
	for _, m := range regexpPKName(createSQL) {
		t.PKName = m
	}

	t.Checks = extractChecks(createSQL)

	if withoutRowid {
		cat.Notes = append(cat.Notes, Warning{WarnStorageOption, t.Name, "WITHOUT ROWID is a SQLite storage option and is not carried"})
	}
	if strict {
		cat.Notes = append(cat.Notes, Warning{WarnStorageOption, t.Name, "STRICT is a SQLite typing option and is not carried (types are carried as declared)"})
	}
	if collateWord.MatchString(createSQL) {
		cat.Notes = append(cat.Notes, Warning{WarnCollationNotCarried, t.Name, "the table definition has COLLATE clauses; collations are not carried"})
	}
	deferrable := fkDeferrable(createSQL)

	// Foreign keys.
	fks, err := db.QueryContext(ctx, `PRAGMA foreign_key_list(`+quoteIdent(t.Name)+`)`)
	if err != nil {
		return err
	}
	byID := map[int]*ForeignKey{}
	var order []int
	for fks.Next() {
		var id, seq int
		var table, from, onUpdate, onDelete, match string
		var to sql.NullString
		if err := fks.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = fks.Close()
			return err
		}
		fk := byID[id]
		if fk == nil {
			fk = &ForeignKey{RefTable: table, OnUpdate: normAction(onUpdate), OnDelete: normAction(onDelete)}
			byID[id] = fk
			order = append(order, id)
		}
		fk.Columns = append(fk.Columns, from)
		fk.RefColumns = append(fk.RefColumns, to.String)
	}
	if err := fks.Err(); err != nil {
		return err
	}
	_ = fks.Close()
	for _, id := range order {
		t.FKs = append(t.FKs, *byID[id])
	}

	nameConstraints(t, createSQL)
	for i := range t.FKs {
		if d, ok := deferrable[strings.Join(t.FKs[i].Columns, "\x00")]; ok {
			t.FKs[i].Deferrable, t.FKs[i].Deferred = true, d
		}
	}
	if deferrableWord.MatchString(notDeferrable.ReplaceAllString(createSQL, "")) && len(deferrable) == 0 {
		cat.Notes = append(cat.Notes, Warning{WarnFKNotValidated, t.Name, "a DEFERRABLE clause was found that could not be matched to a foreign key; foreign keys are created as immediate"})
	}

	// Indexes and unique constraints.
	idx, err := db.QueryContext(ctx, `PRAGMA index_list(`+quoteIdent(t.Name)+`)`)
	if err != nil {
		return err
	}
	type idxRow struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}
	var idxs []idxRow
	for idx.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := idx.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = idx.Close()
			return err
		}
		idxs = append(idxs, idxRow{name, unique != 0, origin, partial != 0})
	}
	if err := idx.Err(); err != nil {
		return err
	}
	_ = idx.Close()
	for _, ix := range idxs {
		if ix.origin == "pk" {
			continue
		}
		xi, err := db.QueryContext(ctx, `PRAGMA index_xinfo(`+quoteIdent(ix.name)+`)`)
		if err != nil {
			return err
		}
		var keyCols []IndexColumn
		expression, nonBinary := false, false
		for xi.Next() {
			var seqno, cid, desc, key int
			var name sql.NullString
			var coll string
			if err := xi.Scan(&seqno, &cid, &name, &desc, &coll, &key); err != nil {
				_ = xi.Close()
				return err
			}
			if key == 0 {
				continue
			}
			if cid == -2 || !name.Valid {
				expression = true
				continue
			}
			if !strings.EqualFold(coll, "BINARY") {
				nonBinary = true
			}
			keyCols = append(keyCols, IndexColumn{Name: name.String, Desc: desc != 0})
		}
		if err := xi.Err(); err != nil {
			return err
		}
		_ = xi.Close()
		obj := t.Name + "." + ix.name
		if expression {
			cat.Notes = append(cat.Notes, Warning{WarnIndexNotCarried, obj, "index on an expression is not carried"})
			continue
		}
		if nonBinary {
			cat.Notes = append(cat.Notes, Warning{WarnCollationNotCarried, obj, "index column collation other than BINARY is not carried"})
		}
		switch ix.origin {
		case "u":
			names := make([]string, len(keyCols))
			for i, k := range keyCols {
				names[i] = k.Name
			}
			t.Uniques = append(t.Uniques, Unique{Columns: names})
		default:
			index := Index{Name: ix.name, Unique: ix.unique, Columns: keyCols}
			if ix.partial {
				var idxSQL string
				_ = db.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='index' AND name=?`, ix.name).Scan(&idxSQL)
				index.Where = topLevelWhere(idxSQL)
			}
			t.Indexes = append(t.Indexes, index)
		}
	}
	nameUniques(t, createSQL)
	sort.SliceStable(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+quoteIdent(t.Name)).Scan(&t.Rows); err != nil {
		return err
	}
	return nil
}

func normAction(a string) string {
	a = strings.ToUpper(strings.TrimSpace(a))
	if a == "" {
		return "NO ACTION"
	}
	return a
}
