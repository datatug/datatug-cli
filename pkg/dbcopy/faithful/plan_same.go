package faithful

import "strings"

// PlanSQLiteToSQLite plans a copy between two SQLite databases: the tables are created from the very
// statements the source stores (so declared types, defaults, collations, checks, foreign keys and
// storage options are exactly the source's) and the data is copied value for value.
func PlanSQLiteToSQLite(cat *Catalog) *Plan {
	p := &Plan{From: SQLite, To: SQLite}
	for _, n := range cat.Notes {
		// Views, triggers and indexes of a SQLite source are carried as they are stored.
		if n.Kind == WarnViewNotCarried || n.Kind == WarnTriggerNotCarried || n.Kind == WarnIndexNotCarried ||
			n.Kind == WarnCollationNotCarried || n.Kind == WarnGeneratedColumn || n.Kind == WarnStorageOption || n.Kind == WarnFKNotValidated {
			continue
		}
		p.Warnings = append(p.Warnings, n)
	}
	for _, t := range cat.Tables {
		tt := &TargetTable{Src: t, Name: t.Name, PKName: t.PKName, PK: t.PK, Uniques: t.Uniques, FKs: t.FKs, Indexes: t.Indexes, Checks: t.Checks}
		for _, c := range t.Columns {
			if c.Generated != "" {
				continue // the target computes it
			}
			tc := &TargetColumn{Src: c, Name: c.Name, Type: c.SourceType, NotNull: c.NotNull, readExpr: sqliteReadExpr(c), enc: encPass}
			if c.Identity == IdentityAutoincrement {
				tc.Identity = IdentityAutoincrement
			}
			tt.Columns = append(tt.Columns, tc)
		}
		p.Tables = append(p.Tables, tt)
	}
	return p
}

// sameEngineStatements are the stored statements of a SQLite source: tables first, then what hangs
// on them, in the order the source created them.
func sameEngineStatements(cat *Catalog, p *Plan) (create []createStmt, post []postStmt) {
	kept := map[string]bool{}
	for _, t := range p.Tables {
		kept[t.Name] = true
		create = append(create, createStmt{t.Name, strings.TrimSpace(t.Src.CreateSQL)})
	}
	for _, o := range cat.Objects {
		if o.Type != "view" && !kept[o.Table] {
			continue
		}
		owner, replace := o.Table, ""
		switch o.Type {
		case "view":
			owner, replace = "", "DROP VIEW IF EXISTS "+quoteIdent(o.Name)
		case "trigger":
			replace = "DROP TRIGGER IF EXISTS " + quoteIdent(o.Name)
		}
		post = append(post, postStmt{table: owner, replace: replace, sql: o.SQL, kind: map[string]string{"index": WarnIndexNotCarried, "view": WarnViewNotCarried, "trigger": WarnTriggerNotCarried}[o.Type], obj: o.Name})
	}
	return
}
