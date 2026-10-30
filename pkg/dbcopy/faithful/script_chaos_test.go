package faithful

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
)

// faultySQLite opens the SQLite file at path through a connection that fails the calls the plan says.
func faultySQLite(t testing.TB, path string, plan *faultPlan) *sql.DB {
	t.Helper()
	db := sql.OpenDB(chaosConnector{open: func(context.Context) (driver.Conn, error) {
		return (&sqlite.Driver{}).Open("file:" + filepath.ToSlash(path) + "?mode=rwc")
	}, plan: plan})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sweepStatements make a small database that goes through every step of the export: keys, an index, a
// check, a default, a foreign key with a parent, one without, a counter and rows.
var sweepStatements = []string{
	`CREATE TABLE p (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, d DATETIME DEFAULT CURRENT_TIMESTAMP, n NUMERIC(6,2) CHECK (n >= 0))`,
	`CREATE TABLE c (id INTEGER PRIMARY KEY, pid INTEGER REFERENCES p(id), gone INTEGER REFERENCES nowhere(id), v BLOB)`,
	`CREATE INDEX c_pid ON c (pid)`,
	`INSERT INTO p (name, d, n) VALUES ('a', '2024-01-01 00:00:00', 1.5), ('b', '2024-01-02 00:00:00', 2)`,
	`INSERT INTO c VALUES (1, 1, NULL, x'00'), (2, 2, NULL, x'')`,
}

// TestExportSurvivesEveryFailure fails each call of the export, in each way a call can fail, and requires an
// error or a script, never a panic or a hang.
func TestExportSurvivesEveryFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sweep.sqlite")
	execAll(t, openSQLite(t, path).DB, sweepStatements...)
	exportWith := func(plan *faultPlan) error {
		db := faultySQLite(t, path, plan)
		_, err := ExportScript(context.Background(), ScriptRequest{Dialect: "mysql", DB: db, Header: testHeader()}, io.Discard)
		return err
	}
	probe := &faultPlan{}
	if err := exportWith(probe); err != nil {
		t.Fatal(err)
	}
	total := probe.total()
	if total < 20 {
		t.Fatalf("only %d calls", total)
	}
	failures := 0
	for mode := faultMode(0); mode < faultModes; mode++ {
		for at := 1; at <= total; at++ {
			if err := exportWith(&faultPlan{failAt: at, mode: mode}); err != nil {
				failures++
			}
		}
	}
	if failures < total {
		t.Errorf("%d of %d injected failures were reported", failures, total*int(faultModes))
	}
	// Planning alone fails the same way.
	for mode := faultMode(0); mode < faultModes; mode++ {
		for at := 1; at <= total; at++ {
			db := faultySQLite(t, path, &faultPlan{failAt: at, mode: mode})
			_, _ = PlanScript(context.Background(), "mysql", db)
		}
	}
}
