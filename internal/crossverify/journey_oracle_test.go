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
	_ "github.com/sijms/go-ora/v2" // the Oracle driver the verifier reads through (pure Go: no client library)
)

// journeyOracleDSNEnv is the DSN of the schema the Oracle journey (scripts/check-journey-oracle.sh) loaded the script
// into; DATATUG_JOURNEY_SQLITE is the SQLite file that was exported.
const journeyOracleDSNEnv = "DATATUG_JOURNEY_ORACLE_DSN"

// TestJourneyOracle verifies a schema that SQL*Plus loaded from the script of a SQLite file. It skips when the journey is
// not running: a test that dials a server is never part of the hermetic run.
func TestJourneyOracle(t *testing.T) {
	lite, dsn := os.Getenv(journeySQLiteEnv), os.Getenv(journeyOracleDSNEnv)
	if lite == "" || dsn == "" {
		t.Skipf("%s and %s are not set: this test needs the Oracle journey (scripts/check-journey-oracle.sh)", journeySQLiteEnv, journeyOracleDSNEnv)
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
	dst, err := sql.Open("oracle", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	plan, err := faithful.PlanScript(ctx, "oracle", src)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := dst.QueryRowContext(ctx, `SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA') FROM DUAL`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	res, err := Verify(ctx, Input{Plan: plan, Source: src, Target: dst, Schema: schema, Engine: EngineOracle, SourceSHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Diffs {
		t.Errorf("DIFF %s", d)
	}
	t.Logf("%s: %d tables, %d rows, %d values compared, %d warnings, %d difference(s)", filepath.Base(abs), res.Tables, res.Rows, res.Values, len(plan.Warnings), len(res.Diffs))
}
