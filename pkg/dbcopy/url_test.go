package dbcopy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/record"
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

	// srv is an httptest.Server: plain HTTP on loopback. OpenForTest sets
	// dalgo2http v0.2.0's TEST-ONLY Collection.InsecureAllowLoopback (see
	// its doc comment) so this exercises the dbcopy -> httpsource wiring
	// without a real https:// endpoint.
	db, err := ref.OpenForTest(context.Background())
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

// writeInGitDBFormulaProject builds a minimal on-disk inGitDB project with
// one "people" collection whose "full_name" column is a formula
// (first_name + " " + last_name) over two stored columns, mirroring
// dal-go/dalgo2ingitdb's own formula_read_test.go setupFormulaDB fixture.
// dbschema.FieldDef (the type ddl.SchemaModifier.CreateCollection takes)
// has no Formula concept, so a formula column can only be declared by
// writing the raw .collection/definition.yaml dalgo2ingitdb reads directly.
func writeInGitDBFormulaProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	defDir := filepath.Join(root, "people", ".collection")
	assert.NoError(t, os.MkdirAll(defDir, 0o755))
	def := `id: people
record_file:
  name: "{key}.yaml"
  format: yaml
  type: map[string]any
columns:
  first_name:
    type: string
  last_name:
    type: string
  full_name:
    type: string
    formula: 'first_name + " " + last_name'
`
	assert.NoError(t, os.WriteFile(filepath.Join(defDir, "definition.yaml"), []byte(def), 0o644))
	ingitDir := filepath.Join(root, ".ingitdb")
	assert.NoError(t, os.MkdirAll(ingitDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(ingitDir, "root-collections.yaml"), []byte("people: people\n"), 0o644))
	recordsDir := filepath.Join(root, "people", "$records")
	assert.NoError(t, os.MkdirAll(recordsDir, 0o755))
	assert.NoError(t, os.WriteFile(filepath.Join(recordsDir, "ada.yaml"), []byte("first_name: Ada\nlast_name: Lovelace\n"), 0o644))
	return root
}

// TestOpen_InGitDB_EvaluatesFormulaColumns pins the legacy behaviour
// OpenProtected must NOT change for Open's own callers (`datatug db copy`
// and schema introspection — both trusted, operator-level, with no
// pkg/accesspolicies wrapper above them): Open still evaluates and returns
// a formula column's computed value, exactly like it did before dalgo2ingitdb
// v0.4.0 introduced WithStoredOnlyReads.
func TestOpen_InGitDB_EvaluatesFormulaColumns(t *testing.T) {
	t.Parallel()
	root := writeInGitDBFormulaProject(t)
	ref, err := Parse("ingitdb://" + root)
	assert.NoError(t, err)

	db, err := ref.Open(context.Background())
	assert.NoError(t, err)
	if !assert.NotNil(t, db) {
		return
	}
	rec := record.NewRecordWithData(record.NewKeyWithID("people", "ada"), map[string]any{})
	assert.NoError(t, db.Get(context.Background(), rec))
	data := rec.Data().(map[string]any)
	assert.Equal(t, "Ada", data["first_name"])
	assert.Equal(t, "Ada Lovelace", data["full_name"], "Open (unprotected) must keep evaluating formula columns")
}

// TestOpenProtected_InGitDB_SuppressesFormulaColumns proves OpenProtected
// wires dalgo2ingitdb v0.4.0's WithStoredOnlyReads() database option in:
// stored columns come back with the SAME values Open returns (a protected
// read yields the same rows/columns as before for every stored field), but
// the formula column comes back suppressed instead of evaluated — the
// protection pkg/secureread.openSource needs because it always layers
// pkg/accesspolicies (the local YAML policy) on top of whatever this
// package returns (see OpenProtected's doc comment). Without this, a
// policy that allows the "full_name" field name would leak the value of
// whichever stored field(s) it hides, evaluated through the formula.
func TestOpenProtected_InGitDB_SuppressesFormulaColumns(t *testing.T) {
	t.Parallel()
	root := writeInGitDBFormulaProject(t)
	ref, err := Parse("ingitdb://" + root)
	assert.NoError(t, err)

	db, err := ref.OpenProtected(context.Background())
	assert.NoError(t, err)
	if !assert.NotNil(t, db) {
		return
	}
	rec := record.NewRecordWithData(record.NewKeyWithID("people", "ada"), map[string]any{})
	assert.NoError(t, db.Get(context.Background(), rec))
	data := rec.Data().(map[string]any)
	assert.Equal(t, "Ada", data["first_name"], "stored column must be unchanged from Open")
	assert.Equal(t, "Lovelace", data["last_name"], "stored column must be unchanged from Open")
	assert.Nil(t, data["full_name"], "OpenProtected must suppress the computed column, not evaluate it")
}

// TestOpenProtected_SQLite_MatchesOpen proves that opting protected sqlite
// reads into dalgo2sql's StructuredQueryDialect: "sqlite" (see
// OpenProtected's doc comment) changes how the SQL is compiled, not what it
// returns: a protected sqlite:// open still returns the exact same
// rows/columns as a plain Open (which keeps using the legacy, undialected
// emitSQL rendering), for the same structured query.
func TestOpenProtected_SQLite_MatchesOpen(t *testing.T) {
	t.Parallel()
	absPath, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Customer", ""))).
		Where(dal.WhereField("CustomerId", dal.Equal, 1)).
		SelectIntoRecord(nil)

	openRef, err := Parse("sqlite://" + absPath)
	assert.NoError(t, err)
	openDB, err := openRef.Open(context.Background())
	assert.NoError(t, err)
	openReader, err := openDB.ExecuteQueryToRecordsReader(context.Background(), q)
	assert.NoError(t, err)
	openRec, err := openReader.Next()
	assert.NoError(t, err)

	protectedRef, err := Parse("sqlite://" + absPath)
	assert.NoError(t, err)
	protectedDB, err := protectedRef.OpenProtected(context.Background())
	assert.NoError(t, err)
	protectedReader, err := protectedDB.ExecuteQueryToRecordsReader(context.Background(), q)
	assert.NoError(t, err)
	protectedRec, err := protectedReader.Next()
	assert.NoError(t, err)

	assert.Equal(t, openRec.Data(), protectedRec.Data())
}

// aliasDialectMaliciousBillingCountry is a classic SQL-injection payload
// shaped for a naive string-interpolation vulnerability: a closing quote
// followed by a statement-terminating `;` and a `--` line comment. Against
// the legacy emitSQL path (still used by plain Open, and by OpenProtected
// before dalgo2sql StructuredQueryDialect: "sqlite" was opted in) this
// exact value renders through dal-go/dalgo's quoteString, which doubles
// embedded single quotes — a real mitigation, but the two tests below prove
// the dialect this stream opts protected reads into does something
// stronger: it never touches the SQL text at all, binding the value as a
// driver arg instead.
const aliasDialectMaliciousBillingCountry = `x'; DROP TABLE Invoice; --`

// TestOpenProtected_SQLite_StructuredDialect_StringParamBoundNotInterpolated
// is S114 Stage 2's second required test, its behavioral half: a string
// parameter containing a `'` and a `--`/`;` sequence, filtered against a
// real chinook.db over the actual OpenProtected -> dalgo2sqlite ->
// dalgo2sql pipeline, returns the honest empty result and no error — not a
// SQL syntax error (which a naively-terminated statement would produce),
// and not every row (which an always-true `OR 1=1`-shaped injection would
// produce). A same-connection follow-up query for a BillingCountry value
// that really exists (chinook.db has 91 USA invoices) proves the Invoice
// table is still intact, i.e. the payload's `; DROP TABLE Invoice; --`
// never executed as SQL on this connection.
func TestOpenProtected_SQLite_StructuredDialect_StringParamBoundNotInterpolated(t *testing.T) {
	t.Parallel()
	absPath, err := filepath.Abs("testdata/chinook.db")
	assert.NoError(t, err)

	ref, err := Parse("sqlite://" + absPath)
	assert.NoError(t, err)
	db, err := ref.OpenProtected(context.Background())
	assert.NoError(t, err)

	maliciousQuery := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Invoice", "i"))).
		WhereField("BillingCountry", dal.Equal, aliasDialectMaliciousBillingCountry).
		SelectColumns(dal.Column{Expression: dal.Field("InvoiceId")})
	maliciousReader, err := db.ExecuteQueryToRecordsReader(context.Background(), maliciousQuery)
	assert.NoError(t, err)
	defer func() { _ = maliciousReader.Close() }()
	_, err = maliciousReader.Next()
	assert.ErrorIs(t, err, dal.ErrNoMoreRecords,
		"a BillingCountry no row has must match nothing — not error, and not silently match every row")

	usaQuery := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Invoice", "i"))).
		WhereField("BillingCountry", dal.Equal, "USA").
		SelectColumns(dal.Column{Expression: dal.Field("InvoiceId")})
	usaReader, err := db.ExecuteQueryToRecordsReader(context.Background(), usaQuery)
	assert.NoError(t, err)
	defer func() { _ = usaReader.Close() }()
	_, err = usaReader.Next()
	assert.NoError(t, err, "Invoice must still contain USA rows on this same connection — the payload must not have executed as SQL")
}

// TestStructuredDialect_StringParamEmitsPlaceholderNotLiteral is S114 Stage
// 2's second required test, its SQL-text half: it proves, directly on the
// emitted SQL and bound args (via sqlmock, the same technique
// dal-go/dalgo2sql's own structured_sql_test.go uses), that the malicious
// BillingCountry value becomes a `?` placeholder plus a bound arg — never a
// quoted literal in the query text. sqlmock.WithArgs enforces this two
// ways at once: ExpectQuery's regex only matches SQL containing a literal
// `?`, so text with the malicious value inlined would not match at all; and
// WithArgs(aliasDialectMaliciousBillingCountry) fails the expectation
// unless that exact value arrives as a driver argument.
func TestStructuredDialect_StringParamEmitsPlaceholderNotLiteral(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	assert.NoError(t, err)
	defer func() { _ = mockDB.Close() }()

	sqlDB := dalgo2sql.NewDatabase(mockDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"})

	q := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("Invoice", "i"))).
		WhereField("BillingCountry", dal.Equal, aliasDialectMaliciousBillingCountry).
		SelectColumns(dal.Column{Expression: dal.Field("InvoiceId")})

	mock.ExpectQuery("SELECT `InvoiceId` FROM `Invoice` AS `i` WHERE `BillingCountry` = \\?").
		WithArgs(aliasDialectMaliciousBillingCountry).
		WillReturnRows(sqlmock.NewRows([]string{"InvoiceId"}))

	reader, err := sqlDB.ExecuteQueryToRecordsReader(context.Background(), q)
	assert.NoError(t, err)
	defer func() { _ = reader.Close() }()
	_, err = reader.Next()
	assert.ErrorIs(t, err, dal.ErrNoMoreRecords)

	assert.NoError(t, mock.ExpectationsWereMet())
}
