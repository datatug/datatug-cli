package dbcopy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/stretchr/testify/assert"
)

func TestParse_SQLite_AbsolutePath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("sqlite:///tmp/foo.db")
	assert.NoError(t, err)
	assert.Equal(t, "sqlite", ref.Scheme)
	assert.Equal(t, "/tmp/foo.db", ref.Path)
	assert.Equal(t, "sqlite:///tmp/foo.db", ref.Raw)
}

func TestParse_SQLite_RelativePath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("sqlite://./rel/foo.db")
	assert.NoError(t, err)
	assert.Equal(t, "sqlite", ref.Scheme)
	// dburl preserves the "./" prefix; we accept whatever dburl emits as long
	// as the resulting path is a valid relative path pointing at rel/foo.db.
	assert.Equal(t, "./rel/foo.db", ref.Path)
}

func TestParse_InGitDB_LocalPath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("ingitdb://./project")
	assert.NoError(t, err)
	assert.Equal(t, "ingitdb", ref.Scheme)
	assert.Equal(t, "./project", ref.Path)
}

func TestParse_InGitDB_RemoteRejected(t *testing.T) {
	t.Parallel()
	_, err := Parse("ingitdb://github.com/owner/repo")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "local paths only")
}

func TestParse_Postgres_Recognized(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://user@host/db")
	assert.NoError(t, err)
	assert.Equal(t, "postgres", ref.Scheme)
	assert.NotEmpty(t, ref.Path)
}

// TestParse_UnknownScheme verifies REQ:unknown-scheme-rejected: the error must
// name the unsupported scheme AND list all supported schemes.
func TestParse_UnknownScheme(t *testing.T) {
	t.Parallel()
	_, err := Parse("mongodb://host/db")
	assert.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "mongodb")
	assert.Contains(t, msg, "sqlite")
	assert.Contains(t, msg, "ingitdb")
	assert.Contains(t, msg, "postgres")
	assert.Contains(t, msg, "http")
	assert.Contains(t, msg, "https")
}

func TestParse_HTTP_LocalPath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("http://./demo-project-1")
	assert.NoError(t, err)
	assert.Equal(t, "http", ref.Scheme)
	assert.Equal(t, "./demo-project-1", ref.Path)
	assert.Equal(t, "http://./demo-project-1", ref.Raw)
}

func TestParse_HTTPS_LocalPath(t *testing.T) {
	t.Parallel()
	ref, err := Parse("https:///abs/demo-project-1")
	assert.NoError(t, err)
	assert.Equal(t, "https", ref.Scheme)
	assert.Equal(t, "/abs/demo-project-1", ref.Path)
}

func TestParse_HTTP_MissingPath(t *testing.T) {
	t.Parallel()
	_, err := Parse("http://")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing project path")
}

// writeHTTPSourceProject builds a minimal on-disk datatug project with one
// HTTP QueryDef ("greeting", keyed by its own "name" parameter, which the
// stub server echoes back) pointed at server's URL, plus a recorded
// fixture, mirroring the real demo project's file layout closely enough to
// exercise the dbcopy → httpsource wiring without depending on
// datatug-demo-projects being checked out (pkg/httpsource's own tests cover
// that translation layer's behaviour in depth; this is only the dispatch
// wiring in url.go).
func writeHTTPSourceProject(t *testing.T, urlTemplate, fixtureBody string) string {
	t.Helper()
	root := t.TempDir()
	queriesDir := filepath.Join(root, "queries", "reference")
	assert.NoError(t, os.MkdirAll(queriesDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(queriesDir, "greeting.query.json"), []byte(`{
		"id": "greeting",
		"title": "Greeting",
		"type": "HTTP",
		"parameters": [{"id": "name", "type": "string", "isRequired": true}],
		"recordsets": [{"columns": [{"name": "name", "type": "string"}, {"name": "message", "type": "string"}]}]
	}`), 0o644))
	assert.NoError(t, os.WriteFile(filepath.Join(queriesDir, "greeting.query.http"), []byte(urlTemplate+"\n"), 0o644))
	fixturesDir := filepath.Join(root, "fixtures", "http")
	assert.NoError(t, os.MkdirAll(fixturesDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(fixturesDir, "greeting.json"), []byte(fixtureBody), 0o644))
	return root
}

// TestOpen_HTTP_OpensDemoLikeProject proves the dbcopy → httpsource wiring:
// Parse+Open on an http:// URL naming a project directory returns a working
// dal.DB backed by that project's HTTP QueryDef.
func TestOpen_HTTP_OpensDemoLikeProject(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		_, _ = w.Write([]byte(`{"name":"` + name + `","message":"hello"}`))
	}))
	defer srv.Close()

	root := writeHTTPSourceProject(t, srv.URL+"/greet?name={name}", `{"name":"World","message":"hi"}`)
	ref, err := Parse("http://" + root)
	assert.NoError(t, err)
	assert.Equal(t, "http", ref.Scheme)

	db, err := ref.Open(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, db)
	if db == nil {
		return
	}

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("greeting", ""))).
		Where(dal.WhereField("name", dal.Equal, "Ada")).
		SelectIntoRecord(nil)
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
	assert.NoError(t, err)
	rec, err := reader.Next()
	assert.NoError(t, err)
	assert.Equal(t, "Ada", rec.Key().ID)
}

// TestOpen_HTTP_NoQueriesErrors proves httpsource.Open's "no HTTP QueryDefs"
// error propagates through dbcopy's Open unchanged (wrapped with context,
// not swallowed).
func TestOpen_HTTP_NoQueriesErrors(t *testing.T) {
	t.Parallel()
	ref, err := Parse("http://" + t.TempDir())
	assert.NoError(t, err)
	_, err = ref.Open(context.Background())
	assert.Error(t, err)
}

func TestOpen_Postgres_ReturnsErrPostgresNotWired(t *testing.T) {
	t.Parallel()
	ref, err := Parse("postgres://user@host/db")
	assert.NoError(t, err)
	db, openErr := ref.Open(context.Background())
	assert.Nil(t, db)
	assert.True(t, errors.Is(openErr, ErrPostgresNotWired),
		"expected ErrPostgresNotWired, got %v", openErr)
}

// TestOpen_SQLite_OpensChinookFixture exercises the sqlite Open path against
// the checked-in Chinook fixture. Verifies the returned dal.DB is non-nil,
// has an adapter, and advertises SupportsConcurrentConnections()==false
// (dalgo2sqlite embeds dal.NoConcurrency).
func TestOpen_SQLite_OpensChinookFixture(t *testing.T) {
	t.Parallel()
	absPath, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)
	_, err = os.Stat(absPath)
	assert.NoError(t, err, "chinook.db fixture missing at %s", absPath)

	ref, err := Parse("sqlite://" + absPath)
	assert.NoError(t, err)
	assert.Equal(t, "sqlite", ref.Scheme)

	db, err := ref.Open(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, db)
	if db != nil {
		assert.NotNil(t, db.Adapter())
		// dalgo2sqlite embeds dal.NoConcurrency.
		assert.False(t, db.SupportsConcurrentConnections(),
			"dalgo2sqlite must advertise NoConcurrency for the parallel-streams cap rule")
	}
}

// TestOpen_InGitDB_OpensEmptyProject exercises the ingitdb Open path against
// a freshly-created empty project directory. The dalgo2ingitdb constructor
// only validates the path; opening doesn't require a populated project.
// Asserts the driver reports SupportsConcurrentConnections()==false
// (dalgo2ingitdb is single-writer; the git working tree is not concurrent-safe).
func TestOpen_InGitDB_OpensEmptyProject(t *testing.T) {
	t.Parallel()
	projDir := t.TempDir()

	ref, err := Parse("ingitdb://" + projDir)
	assert.NoError(t, err)
	assert.Equal(t, "ingitdb", ref.Scheme)
	assert.Equal(t, projDir, ref.Path)

	db, err := ref.Open(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, db)
	if db != nil {
		assert.NotNil(t, db.Adapter())
		// dalgo2ingitdb is single-writer (git working tree); reports false.
		assert.False(t, db.SupportsConcurrentConnections(),
			"dalgo2ingitdb is single-writer and must not advertise ConcurrencyAvailable")
	}
}

// TestCheckSourceFile covers S80 Fix 2: a missing file-backed source used to
// surface only through the driver's own opaque "unable to open database
// file" text (sqlite) reaching an HTTP 500, or an equivalent ingitdb path
// failure — CheckSourceFile gives every caller (Open, and
// pkg/secureread's own read-only connection) one typed, errors.Is-checkable
// signal naming the path and the `datatug demo` recovery.
func TestCheckSourceFile(t *testing.T) {
	t.Parallel()

	t.Run("existing file is nil", func(t *testing.T) {
		t.Parallel()
		absPath, err := filepath.Abs("testdata/chinook.db")
		assert.NoError(t, err)
		assert.NoError(t, CheckSourceFile(absPath))
	})

	t.Run("existing directory is nil", func(t *testing.T) {
		t.Parallel()
		assert.NoError(t, CheckSourceFile(t.TempDir()))
	})

	t.Run("missing path wraps ErrSourceFileMissing naming the path and recovery", func(t *testing.T) {
		t.Parallel()
		missing := filepath.Join(t.TempDir(), "does-not-exist.sqlite")
		err := CheckSourceFile(missing)
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrSourceFileMissing), "expected ErrSourceFileMissing, got %v", err)
		assert.Contains(t, err.Error(), missing)
		assert.Contains(t, err.Error(), "datatug demo")
	})
}

// TestOpen_SQLite_MissingFile_WrapsErrSourceFileMissing proves Open's sqlite
// branch reports ErrSourceFileMissing (not the raw driver error) before the
// modernc.org/sqlite driver ever runs, for a path that simply does not
// exist — this is the exact chinook-local.sqlite-not-fetched-yet scenario
// S77 found (`datatug serve --project` with no prior `datatug demo` run).
func TestOpen_SQLite_MissingFile_WrapsErrSourceFileMissing(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "chinook-local.sqlite")
	ref, err := Parse("sqlite://" + missing)
	assert.NoError(t, err)

	db, openErr := ref.Open(context.Background())
	assert.Nil(t, db)
	assert.True(t, errors.Is(openErr, ErrSourceFileMissing), "expected ErrSourceFileMissing, got %v", openErr)
}

// TestOpen_InGitDB_MissingPath_WrapsErrSourceFileMissing is the equivalent
// inGitDB case the brief names alongside the sqlite one.
func TestOpen_InGitDB_MissingPath_WrapsErrSourceFileMissing(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist-project")
	ref, err := Parse("ingitdb://" + missing)
	assert.NoError(t, err)

	db, openErr := ref.Open(context.Background())
	assert.Nil(t, db)
	assert.True(t, errors.Is(openErr, ErrSourceFileMissing), "expected ErrSourceFileMissing, got %v", openErr)
}
