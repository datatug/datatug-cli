package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DT-0C: a source string is never echoed. What a message shows is built from
// the parts SourceDisplay reads out of the string, so these tests assert on
// what Parse, Open and CheckSourceFile return themselves, with no redactor in
// between.

// --- The five findings left open on pull request 320 -----------------------

// Finding 1: a wrapped URL whose userinfo reads as host or host:port was
// echoed whole by the "looks remote" error.
func TestParse_IngitdbWrappedURLNeverEchoesAPassword(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"ingitdb://https://alice:42/s3cret@github.com/org/repo",
		"ingitdb://https://s3cret:42/x@github.com/org/repo",
		"ingitdb://https://s3/cret@github.com/org/repo",
		"ingitdb://https://:s3/cret@github.com/org/repo",
		"ingitdb://HTTPS://s3cret@github.com/org/repo",
		"ingitdb://https://alice:s3cret@github.com/org/repo",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "looks remote", input)
		assert.ErrorContains(t, err, `"ingitdb://https://github.com/org/repo"`, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "s3/cret", input)
		assert.NotContains(t, err.Error(), "alice", input)
	}
}

// Finding 2: an upper- or mixed-case scheme missed the case-sensitive
// "http://" check and fell into the unknown-scheme error, which quoted the URL.
func TestParse_UpperCaseHTTPSchemeIsRefusedWithoutEcho(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"HTTP://alice:42/s3cret@host/x",
		"Https://alice:42/s3cret@host/x",
		"hTTps://alice:s3cret@host/x",
		"HTTP://s3cret@host/x",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "alice", input)
		assert.NotContains(t, err.Error(), "unsupported scheme", input)
	}
}

// Finding 3: an empty user name passed the userinfo refusal, and the open error
// quoted the path.
func TestParse_EmptyUserNameIsRefused(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"http://:42/s3cret@host/x",
		"http://:s3/cret@host/x",
		"https://:s3cret@host/x",
		"ingitdb://:s3/cret@github.com/org/repo",
		"ingitdb://:s3cret@github.com/org/repo",
		"sqlite://:s3/cret@host/x.db",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "credentials are not supported", input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "s3/cret", input)
	}
	// sqlite://:memory: holds no "@" and stays a path.
	ref, err := Parse("sqlite://:memory:")
	require.NoError(t, err)
	assert.Equal(t, "sqlite://:memory:", ref.Raw)
}

// Finding 4: only the "http" and "https" half of the drive-path exemption
// protects a PostgreSQL password, and no test held it.
func TestParse_PostgresOneLetterUserWithAPasswordThatStartsWithASlash(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://C:/s3cret@db.example.com/shop")
	require.NoError(t, err)
	assert.Equal(t, "postgres://db.example.com/shop", ref.Raw)
	assert.Equal(t, "postgres://db.example.com/shop", ref.String())
	assert.NotContains(t, fmt.Sprintf("%v %+v %#v", ref, ref, ref), "s3cret")
	// And the exemption itself stays scoped to http and https paths.
	for _, input := range []string{`http://C:\work\a@b`, `https://C:/work/a@b/proj`} {
		parsed, err := Parse(input)
		require.NoError(t, err, input)
		assert.Equal(t, input, parsed.Raw)
	}
}

// Finding 5: the refusal texts. The classic "http://alice:s3cret@host/x" keeps
// the http wording; the schemes that name a file say "path".
func TestParse_RefusalWordingPerScheme(t *testing.T) {
	t.Parallel()
	_, err := Parse("http://alice:s3cret@host/x")
	assert.ErrorContains(t, err, "names a local datatug project directory, not a remote endpoint")
	assert.ErrorContains(t, err, `"http://host/x"`)
	_, err = Parse("https://alice:s3cret@host/x")
	assert.ErrorContains(t, err, "https:// names a local datatug project directory")
	_, err = Parse("sqlite://alice:s3cret@host/x.db")
	assert.ErrorContains(t, err, "sqlite:// names a local path; write a relative path as ./path")
	assert.ErrorContains(t, err, `"sqlite://host/x.db"`)
	_, err = Parse("openvaultdb://alice:s3cret@host/c.json")
	assert.ErrorContains(t, err, "openvaultdb:// names a local path; write a relative path as ./path")
	_, err = Parse("ingitdb://alice:s3cret@github.com/org/repo")
	assert.ErrorContains(t, err, "ingitdb:// names a local path; write a relative path as ./path")
	_, err = Parse("ingitdb://s3cret@github.com/org/repo")
	assert.ErrorContains(t, err, "ingitdb:// names a local directory, not a remote repository")
	assert.ErrorContains(t, err, `"ingitdb://github.com/org/repo"`)
}

// --- Construction, classification --------------------------------------------

func TestParse_UnsupportedSchemeNamesOnlyTheScheme(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"mysql://alice:s3cret@db.example.com/shop",
		"MySQL://alice:s3cret@db.example.com/shop?password=s3cret",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.Equal(t, `unsupported scheme "mysql": supported schemes are `+strings.Join(supportedSchemes, ", ")+", or env:NAME", err.Error())
	}
}

func TestParse_OverlongSchemeIsNotEchoed(t *testing.T) {
	t.Parallel()
	_, err := Parse(strings.Repeat("s3cret", 8) + "://h/x")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
}

func TestParse_SchemesAreCaseInsensitive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, scheme, path, raw string
	}{
		{"SQLITE:///tmp/x.db", "sqlite", "/tmp/x.db", "sqlite:///tmp/x.db"},
		{"SQLite:foo.db", "sqlite", "foo.db", "sqlite:foo.db"},
		{"INGITDB://./p", "ingitdb", "./p", "ingitdb://./p"},
		{"HTTP://./proj", "http", "./proj", "http://./proj"},
		{"Https://./proj", "https", "./proj", "https://./proj"},
		{"OpenVaultDB:///c.json", "openvaultdb", "/c.json", "openvaultdb:///c.json"},
		{"Postgres://u:s3cret@h:5432/db", "postgres", "postgres://u:s3cret@h:5432/db", "postgres://h:5432/db"},
		{"POSTGRESQL://u:s3cret@h/db", "postgres", "postgresql://u:s3cret@h/db", "postgresql://h/db"},
	}
	for _, tc := range tests {
		ref, err := Parse(tc.in)
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.scheme, ref.Scheme, tc.in)
		assert.Equal(t, tc.path, ref.Path, tc.in)
		assert.Equal(t, tc.raw, ref.Raw, tc.in)
	}
}

func TestParse_RawIsTheDisplayForm(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"postgres://alice:s3cret@db.example.com:5432/shop?sslmode=require&password=other": "postgres://db.example.com:5432/shop",
		"sqlite:///x.db?_pragma_key=s3cret&mode=ro":                                       "sqlite:///x.db",
		"http://./proj?token=s3cret":                                                      "http://./proj",
		"sqlite:///tmp/foo.db":                                                            "sqlite:///tmp/foo.db",
	} {
		ref, err := Parse(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, ref.Raw, in)
		assert.NotContains(t, ref.Raw, "s3cret")
	}
}

func TestParse_InvalidURLsAreClassifiedNotQuoted(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"postgres://alice:s3cret@db.example.com:bad/db",
		"postgres://alice:s3%zzcret@db.example.com/db",
		"POSTGRES://alice:s3cret with space@db.example.com/db",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.ErrorContains(t, err, "invalid postgres URL", input)
		assert.ErrorContains(t, err, "not a valid connection URL", input)
		assert.NotContains(t, err.Error(), "s3", input)
		assert.NotContains(t, err.Error(), "%zz", input)
	}
	_, err := Parse("sqlite://[::1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid sqlite URL")
	assert.ErrorContains(t, err, "not a valid sqlite URL")
	assert.NotContains(t, err.Error(), "missing")
}

func TestParse_RefusalsAreCaseInsensitiveAndNameTheDisplayForm(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"INGITDB://alice:s3cret@github.com/org/repo",
		"SQLite://alice:s3cret@host/x.db",
		"OPENVAULTDB://alice:s3cret@host/c.json",
		"Ingitdb://github.com/org/repo",
		"ingitdb://GITHUB.COM/org/repo",
		"ingitdb://nosuch://x",
		"ingitdb://postgres://u:s3cret@h/db",
		"ingitdb://postgres://h/db?password=s3cret",
	} {
		_, err := Parse(input)
		require.Error(t, err, input)
		assert.NotContains(t, err.Error(), "s3cret", input)
		assert.NotContains(t, err.Error(), "alice", input)
	}
}

func TestParse_EmptyPaths(t *testing.T) {
	t.Parallel()
	_, err := Parse("ingitdb://")
	assert.ErrorContains(t, err, `invalid ingitdb URL "ingitdb://": missing local path`)
	_, err = Parse("HTTP://")
	assert.ErrorContains(t, err, `invalid http URL "http://": missing project path`)
	_, err = Parse("openvaultdb://")
	assert.ErrorContains(t, err, "connection descriptor path is required")
}

// --- Open and CheckSourceFile ------------------------------------------------

// quotingDriverError is how a driver writes its open error: with the DSN it was
// given and a password keyword.
func quotingDriverError(dsn string) error {
	return fmt.Errorf("driver: PingContext(%q): refused (password=s3cret)", dsn)
}

func TestOpen_DriverTextIsWithheldAndTheCauseIsKept(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.db")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	cause := errors.New("driver: the DSN is " + file + "?password=s3cret")

	origSQLite, origInGitDB := newSQLiteDatabaseWithOptions, newInGitDBDatabase
	t.Cleanup(func() { newSQLiteDatabaseWithOptions, newInGitDBDatabase = origSQLite, origInGitDB })
	newSQLiteDatabaseWithOptions = func(string, dal.Schema, dalgo2sql.DbOptions) (*dalgo2sqlite.Database, error) {
		return nil, cause
	}
	newInGitDBDatabase = func(string, ingitdb.CollectionsReader, ...dalgo2ingitdb.DatabaseOption) (dal.DB, error) {
		return nil, cause
	}

	for _, ref := range []BackendRef{
		{Scheme: "sqlite", Path: file, Raw: "sqlite://" + file},
		{Scheme: "ingitdb", Path: file, Raw: "ingitdb://" + file},
	} {
		_, err := ref.Open(context.Background())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "s3cret")
		assert.NotContains(t, err.Error(), "driver:")
		assert.ErrorContains(t, err, "open "+ref.Scheme+" source "+fmt.Sprintf("%q", ref.Raw))
		assert.ErrorIs(t, err, cause, "the cause stays reachable for errors.Is and errors.As")
	}
}

func TestOpen_HTTPFailureIsClassifiedAndNeverQuotesTheDirectory(t *testing.T) {
	t.Parallel()
	ref, err := Parse("http://" + t.TempDir() + "?token=s3cret")
	require.NoError(t, err)
	_, err = ref.Open(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.NotContains(t, err.Error(), "no HTTP QueryDefs", "driver text is not shown")
	assert.ErrorContains(t, err, "open http source")
}

func TestOpen_PostgresKeepsItsOwnSentinelText(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://alice:s3cret@127.0.0.1:1/shop")
	require.NoError(t, err)
	_, err = ref.Open(context.Background())
	require.ErrorIs(t, err, ErrPostgresNotWired)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.NotContains(t, err.Error(), "alice")
}

func TestOpen_MissingFileKeepsItsSentinelAndShowsThePathWithoutTheQuery(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone.db")
	ref, err := Parse("sqlite://" + missing + "?_pragma_key=s3cret")
	require.NoError(t, err)
	_, err = ref.Open(context.Background())
	require.ErrorIs(t, err, ErrSourceFileMissing)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.ErrorContains(t, err, missing)
}

func TestCheckSourceFile_ShowsTheDisplayFormOfThePath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := CheckSourceFile(filepath.Join(dir, "gone.db") + "?password=s3cret#s3cret")
	require.ErrorIs(t, err, ErrSourceFileMissing)
	assert.NotContains(t, err.Error(), "s3cret")

	// A path that starts like userinfo is shown without it.
	err = CheckSourceFile("s3cret@host/gone.db")
	require.ErrorIs(t, err, ErrSourceFileMissing)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.ErrorContains(t, err, "host/gone.db")
	err = CheckSourceFile("alice:s3cret@host/gone.db")
	assert.NotContains(t, err.Error(), "s3cret")

	// A URL handed to it (a postgres Path) is shown as its display form.
	err = CheckSourceFile("postgres://alice:s3cret@db.example.com/shop")
	require.ErrorIs(t, err, ErrSourceFileMissing)
	assert.NotContains(t, err.Error(), "s3cret")
	assert.ErrorContains(t, err, "postgres://db.example.com/shop")

	// A path that cannot be shown is named as such.
	err = CheckSourceFile("gone\xff.db")
	require.ErrorIs(t, err, ErrSourceFileMissing)
	assert.ErrorContains(t, err, unprintablePath)
}

func TestBackendRef_CheckFileOnlyChecksFileBackedSources(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "gone")
	for _, scheme := range []string{"sqlite", "ingitdb"} {
		assert.ErrorIs(t, BackendRef{Scheme: scheme, Path: missing}.CheckFile(), ErrSourceFileMissing, scheme)
	}
	for _, scheme := range []string{"postgres", "http", "https", "openvaultdb", ""} {
		assert.NoError(t, BackendRef{Scheme: scheme, Path: "postgres://alice:s3cret@h/db"}.CheckFile(), scheme)
	}
	assert.NoError(t, BackendRef{Scheme: "sqlite", Path: t.TempDir()}.CheckFile())
}

func TestOpenFailure_ReasonsAreFixedSentences(t *testing.T) {
	t.Parallel()
	ref := BackendRef{Scheme: "sqlite", Path: "/tmp/x.db", Raw: "sqlite:///tmp/x.db"}
	timeout := &net.DNSError{IsTimeout: true, Name: "s3cret.example"}
	tests := []struct {
		name  string
		cause error
		want  string
	}{
		{"missing file", fmt.Errorf("open s3cret.db: %w", os.ErrNotExist), "the file or directory does not exist"},
		{"permission", fmt.Errorf("open s3cret.db: %w", os.ErrPermission), "permission denied"},
		{"deadline", fmt.Errorf("s3cret: %w", context.DeadlineExceeded), "the attempt timed out"},
		{"network timeout", timeout, "the attempt timed out"},
		{"cancelled", fmt.Errorf("s3cret: %w", context.Canceled), "the attempt was cancelled"},
		{"connection refused", fmt.Errorf("dial s3cret: %w", syscall.ECONNREFUSED), "the connection was refused"},
		{"anything else", errors.New("driver says s3cret"), "the driver could not open the source (its own message is not shown: a driver can quote the connection string)"},
	}
	for _, tc := range tests {
		err := ref.OpenFailure(tc.cause)
		require.Error(t, err, tc.name)
		assert.Equal(t, `open sqlite source "sqlite:///tmp/x.db": `+tc.want, err.Error(), tc.name)
		assert.ErrorIs(t, err, tc.cause, tc.name)
		assert.NotContains(t, err.Error(), "s3cret", tc.name)
	}
	assert.NoError(t, ref.OpenFailure(nil))
	// The errors this package wrote pass through, recognised by what they are.
	own := &missingSourceFileError{shown: "gone.db"}
	assert.Same(t, own, ref.OpenFailure(own))
	classified := ref.OpenFailure(errors.New("driver says s3cret"))
	assert.Same(t, classified, ref.OpenFailure(classified), "classifying twice changes nothing")
	assert.Equal(t, ErrPostgresNotWired, ref.OpenFailure(ErrPostgresNotWired))
	assert.Equal(t, errUnsupportedBackend, ref.OpenFailure(errUnsupportedBackend))
	// A driver that wraps one of this package's sentinels around its own text does
	// not get its text through.
	wrapped := fmt.Errorf("dial postgres://alice:s3cret@h/db: %w", ErrSourceFileMissing)
	got := ref.OpenFailure(wrapped)
	assert.NotContains(t, got.Error(), "s3cret")
	assert.ErrorIs(t, got, ErrSourceFileMissing)
}
