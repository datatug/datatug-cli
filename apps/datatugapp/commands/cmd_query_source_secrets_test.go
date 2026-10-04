package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

const sourceSecret = "s3cr3t-DT01"

// assertNoSecret fails when any captured output holds the secret.
func assertNoSecret(t *testing.T, outputs ...string) {
	t.Helper()
	for _, output := range outputs {
		if strings.Contains(output, sourceSecret) {
			t.Fatalf("output leaks the password: %q", output)
		}
	}
}

func TestQuery_EnvSourceResolvesTheVariable(t *testing.T) {
	t.Setenv("DT01_QUERY_DB", setupQueryDB(t))
	stdout, stderr, code := runQuery(t, "", "--db", "env:DT01_QUERY_DB", "--from", "products", "--format", "json", "--no-policies")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if objects := decodeObjects(t, stdout); len(objects) != 3 {
		t.Fatalf("products = %v", objects)
	}
}

func TestQuery_EnvSourceErrorsNameTheVariableOnly(t *testing.T) {
	t.Setenv("DT01_QUERY_BAD", "mysql://alice:"+sourceSecret+"@db.example.com/shop")
	for _, tc := range []struct {
		db      string
		wantMsg string
	}{
		{"env:DT01_QUERY_UNSET", "environment variable DT01_QUERY_UNSET is not set"},
		{"env:DT01_QUERY_BAD", "environment variable DT01_QUERY_BAD does not hold a supported source URL"},
		{"env:lower_case", "^[A-Z][A-Z0-9_]*$"},
		{"env:postgres://alice:" + sourceSecret + "@h/db", "^[A-Z][A-Z0-9_]*$"},
	} {
		stdout, stderr, code := runQuery(t, "", "--db", tc.db, "--from", "t", "--no-policies")
		if code != exitCodeUsage {
			t.Errorf("%s: exit %d, want %d: %s", tc.db, code, exitCodeUsage, stderr)
		}
		if !strings.Contains(stderr, tc.wantMsg) {
			t.Errorf("%s: stderr %q lacks %q", tc.db, stderr, tc.wantMsg)
		}
		assertNoSecret(t, stdout, stderr)
	}
}

func TestQuery_PostgresPasswordOnTheCommandLineNeverAppears(t *testing.T) {
	literal := "postgres://alice:" + sourceSecret + "@127.0.0.1:1/shop?sslmode=disable"
	stdout, stderr, code := runQuery(t, "", "--db", literal, "--from", "customers", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	assertNoSecret(t, stdout, stderr)
	if !strings.Contains(stderr, "postgres://alice:xxxxx@127.0.0.1:1/shop") {
		t.Errorf("stderr should show the redacted URL: %q", stderr)
	}

	t.Setenv("DT01_QUERY_PG", literal)
	stdout, stderr, code = runQuery(t, "", "--db", "env:DT01_QUERY_PG", "--from", "customers", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	assertNoSecret(t, stdout, stderr)
	if !strings.Contains(stderr, "open env:DT01_QUERY_PG:") {
		t.Errorf("stderr should name the variable: %q", stderr)
	}
	if strings.Contains(stderr, "alice") || strings.Contains(stderr, "127.0.0.1") {
		t.Errorf("an env source must not echo any part of the variable's value: %q", stderr)
	}
}

func TestQuery_DriverErrorsThatQuoteTheURLAreRedacted(t *testing.T) {
	literal := "postgres://alice:" + sourceSecret + "@127.0.0.1:1/shop"
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return nil, errors.New(`dial "` + literal + `" failed; password=` + sourceSecret)
	}
	stdout, stderr, code := runQuery(t, "", "--db", literal, "--from", "customers", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	assertNoSecret(t, stdout, stderr)
}

func TestQuery_HelpDocumentsTheEnvForm(t *testing.T) {
	if help := dbSchemesHelp(); !strings.Contains(help, "env:NAME") {
		t.Errorf("--db help %q does not mention env:NAME", help)
	}
}

func TestExit_RedactsSecretsInEveryMessage(t *testing.T) {
	err := Exit("open postgres://alice:"+sourceSecret+"@h/db: connection refused", 4)
	assertNoSecret(t, err.Error())
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 4 {
		t.Fatalf("Exit must keep its code: %v", err)
	}
	if plain := Exit("--overwrite must be one of recreate, reload", 2); plain.Error() != "--overwrite must be one of recreate, reload" {
		t.Errorf("a message without secrets must pass unchanged: %q", plain.Error())
	}
}
