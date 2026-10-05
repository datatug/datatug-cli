package dbcopy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnv returns a lookup function over a fixed map, so these tests never
// touch the process environment and can run in parallel.
func fakeEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func TestParse_Env_ResolvesSupportedURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		value      string
		wantScheme string
		wantPath   string
	}{
		{"postgres", "postgres://alice:s3cret@db.example.com:5432/shop", "postgres", "postgres://alice:s3cret@db.example.com:5432/shop"},
		{"postgresql alias", "postgresql://alice:s3cret@db.example.com/shop", "postgres", "postgresql://alice:s3cret@db.example.com/shop"},
		{"sqlite", "sqlite:///tmp/shop.db", "sqlite", "/tmp/shop.db"},
		{"ingitdb", "ingitdb://./crm", "ingitdb", "./crm"},
		{"openvaultdb", "openvaultdb:///etc/ovdb.json", "openvaultdb", "/etc/ovdb.json"},
		{"http project", "http://./demo-project", "http", "./demo-project"},
		{"surrounding whitespace is trimmed", "  postgres://alice:s3cret@h/db\n", "postgres", "postgres://alice:s3cret@h/db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, err := parseSource("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": tc.value}))
			assert.NoError(t, err)
			assert.Equal(t, tc.wantScheme, ref.Scheme)
			assert.Equal(t, tc.wantPath, ref.Path)
			assert.Equal(t, "env:SHOP_PG_URL", ref.Raw, "Raw must name the variable, never hold its value")
			assert.NotContains(t, ref.String(), "s3cret")
		})
	}
}

func TestParse_Env_ReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("DT01_TEST_PG_URL", "postgres://alice:s3cret@h/shop")
	ref, err := Parse("env:DT01_TEST_PG_URL")
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.Equal(t, "postgres://alice:s3cret@h/shop", ref.Path)
	assert.Equal(t, "env:DT01_TEST_PG_URL", ref.Raw)
}

func TestValidEnvName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"A", "SHOP_PG_URL", "A1", "A_1_B", "PG2"} {
		assert.True(t, ValidEnvName(name), name)
	}
	for _, name := range []string{"", "a", "shop_pg_url", "1ABC", "_ABC", "A-B", "A B", "A=B", "A\x00B", "ÄB", "A\n", "postgres://u:p@h/db"} {
		assert.False(t, ValidEnvName(name), "%q", name)
	}
}

func TestParse_Env_RejectsBadNamesWithoutEchoingInput(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"env:",
		"env:shop_pg_url",
		"env:1SHOP",
		"env:SHOP-PG",
		"env:SHOP PG",
		"env:SHOP\x00PG",
		"env:postgres://alice:s3cret@h/db", // a URL typed after env: by mistake
		"env:host=db password=s3cret",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			_, err := parseSource(input, fakeEnv(map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h/db"}))
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "^[A-Z][A-Z0-9_]*$")
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "alice")
		})
	}
}

func TestParse_Env_ErrorsNameTheVariableAndNeverEchoTheValue(t *testing.T) {
	t.Parallel()
	const secretURL = "mysql://alice:s3cret@db.example.com/shop"
	tests := []struct {
		name    string
		env     map[string]string
		wantMsg string
	}{
		{"unset", map[string]string{}, "environment variable SHOP_PG_URL is not set"},
		{"empty", map[string]string{"SHOP_PG_URL": ""}, "environment variable SHOP_PG_URL is empty"},
		{"blank", map[string]string{"SHOP_PG_URL": " \t\n"}, "environment variable SHOP_PG_URL is empty"},
		{"nested reference", map[string]string{"SHOP_PG_URL": "env:OTHER_URL"}, "must hold a URL, not another env: reference"},
		{"unsupported scheme", map[string]string{"SHOP_PG_URL": secretURL}, "does not hold a supported source URL"},
		{"not a URL at all", map[string]string{"SHOP_PG_URL": "alice s3cret"}, "does not hold a supported source URL"},
		{"malformed postgres", map[string]string{"SHOP_PG_URL": "postgres://alice:s3cret@h:notaport/db"}, "does not hold a supported source URL"},
		{"empty ingitdb", map[string]string{"SHOP_PG_URL": "ingitdb://"}, "does not hold a supported source URL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref, err := parseSource("env:SHOP_PG_URL", fakeEnv(tc.env))
			assert.Error(t, err)
			assert.Equal(t, BackendRef{}, ref)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.Contains(t, err.Error(), "SHOP_PG_URL")
			for _, secret := range []string{"s3cret", "alice", "db.example.com", "mysql://", "notaport"} {
				assert.NotContains(t, err.Error(), secret)
			}
		})
	}
}

func TestParse_Env_UnsupportedValueErrorListsSchemes(t *testing.T) {
	t.Parallel()
	_, err := parseSource("env:SHOP_PG_URL", fakeEnv(map[string]string{"SHOP_PG_URL": "mysql://u:p@h/db"}))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), strings.Join(supportedSchemes, ", "))
}

func TestParse_LiteralCredentialsNeverEchoedByErrors(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"mysql://alice:s3cret@db.example.com/shop",      // unsupported scheme
		"postgres://alice:s3cret@db.example.com:bad/db", // malformed postgres URL
		"postgres://alice:s3%zzcret@db.example.com/db",  // bad percent-escape
	} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "s3%zzcret", input)
		assert.NotContains(t, err.Error(), "%zz", input)
	}
}

func TestParse_LiteralPostgresRawIsTheDisplayForm(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://alice:s3cret@db.example.com:5432/shop?sslmode=require&password=other")
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.Equal(t, "postgres://db.example.com:5432/shop", ref.Raw)
	assert.Contains(t, ref.Path, "s3cret", "Path stays the real URL: Open needs it")
}

func TestParse_EveryNonEnvSchemeKeepsRawVerbatim(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"sqlite:///tmp/foo.db", "sqlite://./rel/foo.db", "ingitdb://./project", "http://./demo-project-1", "openvaultdb:///tmp/c.json"} {
		ref, err := Parse(input)
		assert.NoError(t, err)
		assert.Equal(t, input, ref.Raw)
	}
}

func TestBackendRef_StringFallsBackToPath(t *testing.T) {
	t.Parallel()
	ref := BackendRef{Scheme: "postgres", Path: "postgres://alice:s3cret@h/db"}
	assert.Equal(t, "postgres://h/db", ref.String())
	assert.Equal(t, "postgres://h/db", fmt.Sprintf("%v", ref))
}

func TestParse_InputThatIsNotAURLIsNeverEchoed(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"alice:s3cret@db.example.com/shop", "s3cret", "host=db password=s3cret", ""} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.Contains(t, err.Error(), "env:NAME", input)
	}
}

func TestParse_UnknownSchemeErrorNamesTheSchemeAndNothingElse(t *testing.T) {
	t.Parallel()
	_, err := Parse("mysql://alice:s3cret@db.example.com/shop")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported scheme "mysql"`)
	assert.NotContains(t, err.Error(), "db.example.com")
	assert.NotContains(t, err.Error(), "alice")
	assert.Contains(t, err.Error(), "env:NAME")
}

func TestParse_HTTPCredentialsAreRefusedWithoutEcho(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"https://alice:s3cret@api.example.com/x", "http://alice:s3cret@host:8080", "https://s3cret@host/x"} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.Contains(t, err.Error(), "credentials are not supported")
		assert.NotContains(t, err.Error(), "s3cret")
	}
	// A project directory that merely contains an "@" later in its path is fine.
	ref, err := Parse("https:///Users/alex/team@work/project")
	assert.NoError(t, err)
	assert.Equal(t, "/Users/alex/team@work/project", ref.Path)
}

func TestRedactError(t *testing.T) {
	t.Parallel()
	assert.NoError(t, RedactError(nil))
	original := fmt.Errorf("dial postgres://alice:s3cret@h/db: %w", ErrPostgresNotWired)
	redacted := RedactError(original)
	assert.NotContains(t, redacted.Error(), "s3cret")
	assert.Contains(t, redacted.Error(), "postgres://alice:xxxxx@h/db")
	assert.ErrorIs(t, redacted, ErrPostgresNotWired, "the wrapped chain must survive redaction")
	assert.ErrorIs(t, fmt.Errorf("open: %w", redacted), ErrPostgresNotWired)
}

func TestParse_IngitdbNeverEchoesAPassword(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"ingitdb://https://alice:s3cret@github.com/org/repo",
		"ingitdb://http://alice:s3cret@github.com/org/repo?token=s3cret",
		"ingitdb://github.com/org/repo?password=s3cret",
		"ingitdb://alice:s3cret@github.com/org/repo",
		"ingitdb://alice:s3/cret@github.com/org/repo",
		"ingitdb://https://s3cret:x-oauth-basic@github.com/org/repo",
	} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "s3/cret", input)
	}
	_, err := Parse("ingitdb://https://alice:s3cret@github.com/org/repo")
	assert.ErrorContains(t, err, "looks remote")
	assert.ErrorContains(t, err, "ingitdb://https://github.com/org/repo")
	_, err = Parse("ingitdb://https://TOKEN:x-oauth-basic@github.com/org/repo")
	assert.ErrorContains(t, err, "ingitdb://https://github.com/org/repo")
	assert.NotContains(t, err.Error(), "x-oauth-basic")
	_, err = Parse("ingitdb://alice:s3cret@github.com/org/repo")
	assert.ErrorContains(t, err, "credentials are not supported")
	// A token written as the user name has no colon and is still a credential.
	_, err = Parse("ingitdb://https://s3cret@github.com/org/repo")
	assert.ErrorContains(t, err, "ingitdb://https://github.com/org/repo")
	_, err = Parse("ingitdb://s3cret@github.com/org/repo")
	assert.ErrorContains(t, err, "credentials are not supported")
	assert.ErrorContains(t, err, "./dir", "the error says how to write a directory that holds an at sign")
}

func TestParse_IngitdbDirectoryWithAnAtSignNeedsTheDotForm(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"my@proj", "@acme/proj", "my@proj/data/ingitdb", "a@b", "team/a@b", "a/b/c@d/e"} {
		_, err := Parse("ingitdb://" + dir)
		assert.ErrorContains(t, err, "credentials are not supported", dir)
		assert.NotContains(t, err.Error(), dir, "the directory is not echoed")

		source := LocalSourceURL("ingitdb", dir)
		assert.Equal(t, "ingitdb://./"+dir, source)
		ref, err := Parse(source)
		assert.NoError(t, err, dir)
		assert.Equal(t, "./"+dir, ref.Path, "the same relative directory")
		assert.Equal(t, "ingitdb", ref.Scheme)
	}
	for dir, want := range map[string]string{
		"demo":          "ingitdb://demo",
		"./a@b":         "ingitdb://./a@b",
		"/abs/a@b/proj": "ingitdb:///abs/a@b/proj",
		"team/a@b":      "ingitdb://./team/a@b",
		"team/dir":      "ingitdb://team/dir",
		"../a@b":        "ingitdb://../a@b",
		"~/a@b":         "ingitdb://~/a@b",
		`C:\work\a@b`:   `ingitdb://C:\work\a@b`,
		"a:b/c@d":       "ingitdb://./a:b/c@d",
	} {
		assert.Equal(t, want, LocalSourceURL("ingitdb", dir), dir)
	}
}

func TestParse_PathSchemesRefuseUserinfoButKeepRealPaths(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"sqlite://alice:s3cret@host/x.db",
		"openvaultdb://alice:s3cret@host/c.json",
	} {
		_, err := Parse(input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		assert.NotContains(t, err.Error(), "s3cret", input)
	}
	for _, input := range []string{
		"sqlite:///tmp/a@b:c.db",
		"sqlite://./a@b.db",
		"sqlite:a@b:c.db",
		"ingitdb://./a@b:c",
		"ingitdb://./a@b",
		`ingitdb://C:\work\a@b`,
		"openvaultdb:///a@b:c.json",
	} {
		_, err := Parse(input)
		assert.NoError(t, err, input)
	}
}

func TestParse_SqliteParseErrorIsRedacted(t *testing.T) {
	t.Parallel()
	_, err := Parse("sqlite://%zz:s3cret@")
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestParse_BareSchemeStringNamesNothing(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"mongodb:localhost/test", "alice:s3cret", "ghp1234567890:x-oauth-basic", "postgres:alice:s3cret@h/db", "12345:rest", "alice:s3cret@db.example.com/shop", "s3cret", "host=db password=s3cret", ""} {
		_, err := Parse(input)
		assert.Error(t, err, input)
		assert.Equal(t, "unsupported source: expected a URL such as sqlite://..., or env:NAME; supported schemes are "+strings.Join(supportedSchemes, ", "), err.Error(), input)
	}
}

func TestParse_HTTPDirectoryWithAnAtSignNeedsTheDotForm(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"my@proj", "@acme/proj", "my@proj/queries", "a@b", "a:b@c/d", "a:b/c@d", "alice:42/s3cret@host/x", "team/a@b"} {
		_, err := Parse("http://" + dir)
		assert.ErrorContains(t, err, "credentials are not supported", dir)
		assert.ErrorContains(t, err, "./dir", "the error says how to write it")
		assert.NotContains(t, err.Error(), "@"+dir, "the directory is not echoed")
		assert.NotContains(t, err.Error(), dir)

		source := ProjectSourceURL(dir)
		assert.Equal(t, "http://./"+dir, source)
		ref, err := Parse(source)
		assert.NoError(t, err, dir)
		assert.Equal(t, "./"+dir, ref.Path, "the same relative directory")
		assert.Equal(t, "http", ref.Scheme)
	}
	for dir, want := range map[string]string{
		"demo":             "http://demo",
		"./a@b":            "http://./a@b",
		"/abs/a@b/proj":    "http:///abs/a@b/proj",
		"team/a@b":         "http://./team/a@b",
		"team/dir":         "http://team/dir",
		`C:\work\a@b`:      `http://C:\work\a@b`,
		"C:/work/a@b/proj": "http://C:/work/a@b/proj",
	} {
		assert.Equal(t, want, ProjectSourceURL(dir), dir)
	}
	for _, dir := range []string{`C:\work\a@b`, "C:/work/a@b/proj"} {
		ref, err := Parse(ProjectSourceURL(dir))
		assert.NoError(t, err, dir)
		assert.Equal(t, dir, ref.Path)
	}
}

func TestParse_HTTPAndHTTPSRefuseAPasswordThatReadsAsHostAndPort(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"http://alice:42/s3cret@host/x",
		"https://alice:42/s3cret@host/x",
	} {
		_, err := Parse(input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "alice", input)
	}
	// A real project path that merely holds a colon and an at sign later is a path.
	for _, input := range []string{"http://./a:b/c@d", "http:///abs/a:b/c@d", `http://C:\work\a:b@c`} {
		_, err := Parse(input)
		assert.NoError(t, err, input)
	}
}

func TestOpen_ErrorsAreScrubbedOfTheRealURLsSecrets(t *testing.T) {
	// not parallel: it swaps a package seam
	original := newSQLiteDatabaseWithOptions
	t.Cleanup(func() { newSQLiteDatabaseWithOptions = original })
	newSQLiteDatabaseWithOptions = func(string, dal.Schema, dalgo2sql.DbOptions) (*dalgo2sqlite.Database, error) {
		return nil, fmt.Errorf(`dial "postgres://alice:s3cret@db.example.com/shop": %w`, ErrSourceFileMissing)
	}
	file := filepath.Join(t.TempDir(), "x.db")
	assert.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err := BackendRef{Scheme: "sqlite", Path: file}.Open(context.Background())
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.ErrorIs(t, err, ErrSourceFileMissing)

	// A driver that formats the DSN its own way is scrubbed by literal value.
	postgres := BackendRef{Scheme: "postgres", Path: "postgres://alice:s3cret@127.0.0.1:1/shop?sslmode=disable"}
	_, err = postgres.Open(context.Background())
	assert.ErrorIs(t, err, ErrPostgresNotWired, "an error without secrets is returned as it was")
	assert.Equal(t, ErrPostgresNotWired, err)

	// An http source whose path carries a secret-named parameter.
	_, err = BackendRef{Scheme: "http", Path: filepath.Join(t.TempDir(), "nope") + "?api_key=K3Y-secret"}.Open(context.Background())
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "K3Y-secret")
}

// A user name or a token that holds a slash puts the colon or the "@" after a
// slash, so it used to read as a relative path and was shown whole. What stands
// in front of the last "@" is userinfo unless the text is certainly a path.
func TestParse_ProjectDirectorySchemesRefuseAUserNameOrTokenThatHoldsASlash(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"ingitdb://ab/cdefghij@github.com/org/repo",
		"ingitdb://team/alice:s3cretpass@github.com/org/repo",
		"INGITDB://ab/cdefghij@github.com/org/repo",
		"http://team/alice:s3cretpass@host/x",
		"https://team/alice:s3cretpass@host/x",
		"HTTP://ab/cdefghij@host/x",
		"http://ab/cdefghij@host",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		assert.ErrorContains(t, err, "./dir", "the error says how to write a directory that holds an at sign")
		for _, secret := range []string{"s3cretpass", "cdefghij", "alice", "ab/", "team/"} {
			assert.NotContains(t, err.Error(), secret, input)
		}
	}
	_, err := Parse("ingitdb://ab/cdefghij@github.com/org/repo")
	assert.ErrorContains(t, err, `"ingitdb://github.com/org/repo"`)
}

// The sqlite:// and openvaultdb:// forms refuse a relative path that holds an "@"
// and does not start like a path, as the project directory schemes do: dburl reads
// "sqlite://a@b.db" as user a and opens b.db, and every message that names such a
// path shows what follows the "@" as a host. What a user means by it is written
// with a dot, and what Parse refuses shows no token.
func TestParse_SqliteAndOpenVaultDBRefuseARelativePathThatHoldsAnAtSign(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"sqlite://ab/cdefghij@github.com/org/repo",
		"sqlite://data/my@db.sqlite",
		"sqlite://a@b.db",
		"sqlite://@acme/proj",
		"openvaultdb://alice/cdefghij@host/c.json",
		"openvaultdb://data/my@db.json",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		assert.ErrorContains(t, err, "write a relative path as ./path", input)
		for _, secret := range []string{"cdefghij", "alice", "ab/", "my@db", "a@b"} {
			assert.NotContains(t, err.Error(), secret, input)
		}
	}
	// The dot form is a path, and keeps its "@".
	for _, input := range []string{"sqlite://./data/my@db.sqlite", "sqlite://./@acme/proj", "openvaultdb://./data/my@db.json", "sqlite:///abs/my@db.sqlite"} {
		ref, err := Parse(input)
		require.NoError(t, err, input)
		assert.Equal(t, input, ref.Raw, "an explicit path is shown as it was typed")
	}
	// The form without slashes always names the file whole, so it is accepted; what
	// is shown of it holds no token.
	ref, err := Parse("sqlite:ab/cdefghij@github.com/org/repo")
	require.NoError(t, err)
	for _, shown := range []string{ref.Raw, ref.String(), ref.Display(), fmt.Sprintf("%v %+v %#v", ref, ref, ref), PathDisplay(ref.Path)} {
		assert.NotContains(t, shown, "cdefghij")
	}
	err = ref.CheckFile()
	require.ErrorIs(t, err, ErrSourceFileMissing)
	assert.NotContains(t, err.Error(), "cdefghij")
}

// LocalSourceURL writes a relative path that holds an "@" in the dot form for
// sqlite and openvaultdb too, and the URL it gives names the same file: dburl
// would read "sqlite://@acme/proj.db" as user @acme and open acme/proj.db.
func TestLocalSourceURL_WritesTheDotFormForSqliteAndOpenVaultDB(t *testing.T) {
	t.Parallel()
	for scheme, path := range map[string]string{"sqlite": "@acme/proj.db", "openvaultdb": "team/a@b.json"} {
		source := LocalSourceURL(scheme, path)
		assert.Equal(t, scheme+"://./"+path, source)
		ref, err := Parse(source)
		require.NoError(t, err, source)
		assert.Equal(t, "./"+path, ref.Path, "the URL names the file it was built from")
	}
	assert.Equal(t, "sqlite:///abs/@acme/proj.db", LocalSourceURL("sqlite", "/abs/@acme/proj.db"))
	assert.Equal(t, "sqlite://plain/proj.db", LocalSourceURL("sqlite", "plain/proj.db"))
}

// A UNC start counts as a path, and "\\alice:password@host" starts with one all
// the same: it is refused as credentials, and the refusal shows no credentials.
func TestParse_UserinfoAfterAUNCStartIsRefusedWithoutEcho(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		`http://\\alice:s3cretpass@host/x`,
		`https://\\alice:s3cretpass@host/x`,
		`ingitdb://\\tok:x-oauth-basic@github.com/org/repo`,
		`sqlite://\\alice:s3cretpass@host/x.db`,
		`openvaultdb://\\alice:s3cretpass@host/c.json`,
		`HTTP://\\alice:s3cretpass@host/x`,
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		for _, secret := range []string{"s3cretpass", "alice", "tok", "x-oauth-basic", `\\`} {
			assert.NotContains(t, err.Error(), secret, input)
		}
	}
	// A real UNC path keeps its "@".
	for _, input := range []string{`ingitdb://\\server\share@x\proj`, `http://\\server\share@x\proj`} {
		ref, err := Parse(input)
		require.NoError(t, err, input)
		assert.Equal(t, input, ref.Raw, "a UNC path is shown as it was typed")
	}
}

// A directory that is certainly a path keeps its "@": the explicit forms.
func TestParse_ExplicitPathsKeepTheirAtSign(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"ingitdb://./team/a@b",
		"ingitdb:///abs/team@work/proj",
		"ingitdb://../team/a@b",
		"ingitdb://~/team/a@b",
		`ingitdb://\\server\share@x\proj`,
		`ingitdb://C:\work\a@b`,
		"http://./team/a@b",
		"https:///abs/team@work/proj",
		"http://../a@b/p",
	} {
		ref, err := Parse(input)
		require.NoError(t, err, input)
		assert.Equal(t, input, ref.Raw, "an explicit path is shown as it was typed")
	}
}
