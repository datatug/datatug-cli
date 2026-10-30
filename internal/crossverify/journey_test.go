package crossverify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	_ "github.com/go-sql-driver/mysql" // the MySQL driver the verifier reads through
	_ "modernc.org/sqlite"
)

// Environment of the MySQL journey (scripts/check-journey-mysql.sh sets it): the SQLite file that was
// exported, and the DSN of the database the script was loaded into.
const (
	journeySQLiteEnv = "DATATUG_JOURNEY_SQLITE"
	journeyDSNEnv    = "DATATUG_JOURNEY_MYSQL_DSN"
)

// TestJourneyMySQL verifies a database that the mysql client loaded from the script of a SQLite file. It
// skips when the journey is not running: a test that dials a server is never part of the hermetic run.
func TestJourneyMySQL(t *testing.T) {
	lite, dsn := os.Getenv(journeySQLiteEnv), os.Getenv(journeyDSNEnv)
	if lite == "" || dsn == "" {
		t.Skipf("%s and %s are not set: this test needs the MySQL journey (scripts/check-journey-mysql.sh)", journeySQLiteEnv, journeyDSNEnv)
	}
	ctx := context.Background()
	abs, err := filepath.Abs(lite)
	if err != nil {
		t.Fatal(err)
	}
	src, err := sql.Open("sqlite", "file:"+(&url.URL{Path: filepath.ToSlash(abs)}).EscapedPath()+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	plan, err := faithful.PlanScript(ctx, "mysql", src)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Verify(ctx, Input{Plan: plan, Source: src, Target: dst, SourceSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diffs {
		t.Errorf("DIFF %s", d)
	}
	t.Logf("%s: %d tables, %d rows, %d values compared, %d warnings, %d difference(s)", filepath.Base(abs), res.Tables, res.Rows, res.Values, len(plan.Warnings), len(res.Diffs))
}
