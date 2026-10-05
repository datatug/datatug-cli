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
	t.Setenv(dbcopy.PostgresPreviewEnv, "") // the preview is off: nothing is dialled, and the answer is its sentence
	literal := "postgres://alice:" + sourceSecret + "@127.0.0.1:1/shop?sslmode=disable"
	stdout, stderr, code := runQuery(t, "", "--db", literal, "--from", "customers", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	assertNoSecret(t, stdout, stderr)
	if strings.Contains(stderr, "alice") || strings.Contains(stderr, "127.0.0.1:1/shop?") {
		t.Errorf("stderr must not show the user or the query of the URL: %q", stderr)
	}
	if !strings.Contains(stderr, dbcopy.ErrPostgresPreview.Error()) {
		t.Errorf("stderr should say that PostgreSQL sources are a preview and are switched off: %q", stderr)
	}

	t.Setenv("DT01_QUERY_PG", literal)
	stdout, stderr, code = runQuery(t, "", "--db", "env:DT01_QUERY_PG", "--from", "customers", "--no-policies")
	if code != exitCodeDatabase {
		t.Fatalf("exit %d, want %d: %s", code, exitCodeDatabase, stderr)
	}
	assertNoSecret(t, stdout, stderr)
	if strings.Contains(stderr, "alice") || strings.Contains(stderr, "127.0.0.1") {
		t.Errorf("an env source must not echo any part of the variable's value: %q", stderr)
	}
}

// The open failure names the source once: OpenFailure already says which source
// failed, so the command no longer puts "open <source>:" in front of it. An env
// source is named by its variable, never by what the variable holds.
func TestQuery_OpenFailureNamesTheSourceOnce(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1") // with the preview off the command answers before the open this test replaces
	original := openBackend
	t.Cleanup(func() { openBackend = original })
	openBackend = func(_ context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		return nil, ref.OpenFailure(errors.New("driver text that is never shown"))
	}
	t.Setenv("DT01_QUERY_NAMED", "postgres://alice:"+sourceSecret+"@127.0.0.1:1/shop")
	for source, want := range map[string]string{
		"sqlite:///x.db":       `open sqlite source "sqlite:///x.db": the driver could not open the source`,
		"env:DT01_QUERY_NAMED": `open postgres source "env:DT01_QUERY_NAMED": the driver could not open the source`,
	} {
		stdout, stderr, code := runQuery(t, "", "--db", source, "--from", "customers", "--no-policies")
		if code != exitCodeDatabase {
			t.Fatalf("%s: exit %d, want %d: %s", source, code, exitCodeDatabase, stderr)
		}
		assertNoSecret(t, stdout, stderr)
		if !strings.Contains(stderr, want) {
			t.Errorf("%s: stderr %q lacks %q", source, stderr, want)
		}
		if strings.Contains(stderr, "open "+source+":") || strings.Count(stderr, source) != 1 {
			t.Errorf("%s: stderr names the source more than once: %q", source, stderr)
		}
		if strings.Contains(stderr, "never shown") || strings.Contains(stderr, "alice") {
			t.Errorf("%s: stderr shows the driver text or the user: %q", source, stderr)
		}
	}
}

func TestQuery_DriverErrorsThatQuoteTheURLAreNeverShown(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1") // with the preview off the command answers before the open this test replaces
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

// Exit shows its message as it is: no message path relies on a redactor (the
// top-level handler in main.go keeps one as a last line of defence).
func TestExit_KeepsItsMessageAndItsCode(t *testing.T) {
	err := Exit("--overwrite must be one of recreate, reload", 2)
	if err.Error() != "--overwrite must be one of recreate, reload" {
		t.Errorf("the message must pass unchanged: %q", err.Error())
	}
	var coder ExitCoder
	if !errors.As(err, &coder) || coder.ExitCode() != 2 {
		t.Fatalf("Exit must keep its code: %v", err)
	}
}
