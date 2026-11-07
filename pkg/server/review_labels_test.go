package server

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/meaninglabelsfile"
	"github.com/datatug/datatug-core/pkg/meaninglabels"
	_ "modernc.org/sqlite"
)

// reviewSuggestedSQLiteColumns records the owner's "none" decision for every
// column of source (the SQLite file at dbPath) that a read-time suggestion
// marks sensitive, as an owner who reviewed them does.
func reviewSuggestedSQLiteColumns(t *testing.T, projectDir, source, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	columns := map[string][]meaninglabels.Column{}
	rows, err := db.Query(`SELECT m.name, p.name FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		columns[table] = append(columns[table], meaninglabels.Column{Name: column})
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := meaninglabelsfile.Store{Dir: projectDir}
	doc, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := doc
	for table, cols := range columns {
		for _, l := range meaninglabels.Effective(doc, source, table, cols) {
			if !l.Protected() {
				continue
			}
			reviewed, _, err = meaninglabels.Apply(reviewed, meaninglabels.Change{Action: meaninglabels.ActionSet,
				Target: meaninglabels.Target{Source: source, Table: table, Column: l.Column}, Class: meaninglabels.ClassNone, Reason: "test: reviewed"},
				meaninglabels.Actor{ID: "user:test"}, time.Unix(0, 0))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := store.Save(ctx, reviewed, doc.Revision); err != nil {
		t.Fatal(err)
	}
}
