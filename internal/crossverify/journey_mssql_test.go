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
	_ "github.com/microsoft/go-mssqldb" // the SQL Server driver the verifier reads through
)

// journeyMSSQLDSNEnv is the DSN of the database the SQL Server journey (scripts/check-journey-mssql.sh) loaded
// the script into; DATATUG_JOURNEY_SQLITE is the SQLite file that was exported.
const journeyMSSQLDSNEnv = "DATATUG_JOURNEY_MSSQL_DSN"

// TestJourneyMSSQL verifies a database that sqlcmd loaded from the script of a SQLite file. It skips when the
// journey is not running: a test that dials a server is never part of the hermetic run.
func TestJourneyMSSQL(t *testing.T) {
	lite, dsn := os.Getenv(journeySQLiteEnv), os.Getenv(journeyMSSQLDSNEnv)
	if lite == "" || dsn == "" {
		t.Skipf("%s and %s are not set: this test needs the SQL Server journey (scripts/check-journey-mssql.sh)", journeySQLiteEnv, journeyMSSQLDSNEnv)
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
	dst, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	plan, err := faithful.PlanScript(ctx, "mssql", src)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Verify(ctx, Input{Plan: plan, Source: src, Target: dst, Engine: EngineMSSQL, SourceSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diffs {
		t.Errorf("DIFF %s", d)
	}
	t.Logf("%s: %d tables, %d rows, %d values compared, %d warnings, %d difference(s)", filepath.Base(abs), res.Tables, res.Rows, res.Values, len(plan.Warnings), len(res.Diffs))
}
