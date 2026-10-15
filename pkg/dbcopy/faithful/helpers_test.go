package faithful

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // the PostgreSQL driver
)

// pgAdminEnv names the connection URL of a local PostgreSQL server the integration tests may create
// and drop databases on. It must point at 127.0.0.1 / localhost: the tests refuse any other host.
const pgAdminEnv = "DATATUG_TEST_POSTGRES_URL"

func randHex(t testing.TB) string {
	t.Helper()
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// newPGDatabase creates an empty database on the local test server and returns an endpoint on it.
func newPGDatabase(t testing.TB) Endpoint {
	t.Helper()
	admin := os.Getenv(pgAdminEnv)
	if admin == "" {
		t.Skipf("%s is not set: this test needs a local PostgreSQL server", pgAdminEnv)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	if h := u.Hostname(); h != "127.0.0.1" && h != "localhost" && h != "::1" {
		t.Fatalf("%s must point at the local machine, not %q", pgAdminEnv, h)
	}
	ctx := context.Background()
	adminDB, err := sql.Open("pgx", admin)
	if err != nil {
		t.Fatal(err)
	}
	name := "dt_faithful_" + randHex(t)
	if _, err := adminDB.ExecContext(ctx, `CREATE DATABASE `+quoteIdent(name)); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = adminDB.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(name)+` WITH (FORCE)`)
		_ = adminDB.Close()
	})
	return Endpoint{Engine: Postgres, DB: db, Display: "postgres://127.0.0.1/" + name, Schema: "public"}
}

// newSQLiteFile opens (creating it) the SQLite file at path.
func openSQLite(t testing.TB, path string) Endpoint {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return Endpoint{Engine: SQLite, DB: db, Display: path}
}

func newSQLite(t testing.TB) Endpoint {
	return openSQLite(t, filepath.Join(t.TempDir(), "t.sqlite"))
}

func execAll(t testing.TB, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
	}
}

func hasWarning(r *Report, kind, objectPart string) bool {
	for _, w := range r.Warnings {
		if w.Kind == kind && strings.Contains(w.Object, objectPart) {
			return true
		}
	}
	return false
}

// simpleProtocol opens a second handle on e that sends multi-statement scripts.
func simpleProtocol(t testing.TB, e Endpoint) *sql.DB {
	t.Helper()
	u, err := url.Parse(os.Getenv(pgAdminEnv))
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := e.DB.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	q := u.Query()
	q.Set("default_query_exec_mode", "simple_protocol")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqlNull() sql.NullString { return sql.NullString{} }
