package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
)

func runDBSchema(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := dbSchemaCommand()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func sqliteWithViews(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE Zebra (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE Album (id INTEGER PRIMARY KEY, title TEXT)`,
		`CREATE VIEW AlbumTitles AS SELECT title FROM Album`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestDBSchemaSQLiteJSONViaEnv(t *testing.T) {
	path := sqliteWithViews(t)
	t.Setenv("SCHEMA_TEST_DB", "sqlite://"+path)
	out, err := runDBSchema(t, "--db", "env:SCHEMA_TEST_DB")
	if err != nil {
		t.Fatal(err)
	}
	var got schemaListing
	if err = json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	want := []schemaEntry{
		{"main", "Album", "table"},
		{"main", "Zebra", "table"},
		{"main", "AlbumTitles", "view"},
	}
	if got.Provider != "sqlite" || len(got.Tables) != len(want) {
		t.Fatalf("unexpected listing: %+v", got)
	}
	for i := range want {
		if got.Tables[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got.Tables[i], want[i])
		}
	}
	if got.Target != "sqlite://"+path {
		t.Fatalf("target = %q", got.Target)
	}
}

func TestDBSchemaEmptyDatabaseListsEmptyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE t (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE t`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	out, err := runDBSchema(t, "--db", "sqlite://"+path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"tables": []`) {
		t.Fatalf("tables is not an empty array:\n%s", out)
	}
}

func TestDBSchemaText(t *testing.T) {
	path := sqliteWithViews(t)
	out, err := runDBSchema(t, "--db", "sqlite://"+path, "--format", "text")
	if err != nil {
		t.Fatal(err)
	}
	want := "provider: sqlite\ntarget: sqlite://" + path + "\nmain\ttable\tAlbum\nmain\ttable\tZebra\nmain\tview\tAlbumTitles\n"
	if out != want {
		t.Fatalf("got\n%q\nwant\n%q", out, want)
	}
}

func TestDBSchemaUsageErrors(t *testing.T) {
	t.Setenv("SCHEMA_EMPTY_VAR", "")
	cases := map[string][]string{
		"unknown format":       {"--db", "sqlite://./x.db", "--format", "yaml"},
		"unparseable":          {"--db", "nonsense"},
		"unset env":            {"--db", "env:SCHEMA_UNSET_VAR_XYZ"},
		"unsupported provider": {"--db", "ingitdb://./x"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runDBSchema(t, args...)
			if err == nil || exitCodeOf(t, err) != exitCodeUsage {
				t.Fatalf("want exit %d, got %v", exitCodeUsage, err)
			}
		})
	}
	_, err := runDBSchema(t, "--db", "bigquery://my-project-1/ds?location=EU")
	if err == nil || !strings.Contains(err.Error(), "supported providers are sqlite, postgres") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("unsupported provider message: %v", err)
	}
}

func TestDBSchemaMissingDBFlag(t *testing.T) {
	_, err := runDBSchema(t)
	if err == nil || exitCodeOf(t, err) != exitCodeUsage {
		t.Fatalf("want exit %d, got %v", exitCodeUsage, err)
	}
}

func TestDBSchemaPostgresPreviewOff(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	_, err := runDBSchema(t, "--db", "postgres://alice:hunter2@example.invalid/shop")
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
}

func withSchemaSource(t *testing.T, open func(context.Context, dbcopy.BackendRef) (dal.DB, error)) {
	t.Helper()
	original := openSchemaSource
	t.Cleanup(func() { openSchemaSource = original })
	openSchemaSource = open
}

func TestDBSchemaOpenFailureNeverLeaksCredentials(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return nil, errors.New("dial postgres://alice:hunter2@example.invalid/shop failed")
	})
	out, err := runDBSchema(t, "--db", "postgres://alice:hunter2@example.invalid/shop?sslmode=disable")
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(out+err.Error(), "hunter2") || strings.Contains(out+err.Error(), "alice") {
		t.Fatalf("credentials leaked: %q %v", out, err)
	}
}

func TestDBSchemaSQLiteMissingFile(t *testing.T) {
	_, err := runDBSchema(t, "--db", "sqlite://"+filepath.Join(t.TempDir(), "absent.sqlite"))
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase {
		t.Fatalf("got %v", err)
	}
}

// fakeSQLite is a database with the SQLite capabilities of dalgo2sqlite.
type fakeSQLite struct {
	dal.DB
	tables   []string
	tableErr error
	views    []string
	viewErr  error
}

func (f fakeSQLite) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	refs := make([]dal.CollectionRef, 0, len(f.tables))
	for _, name := range f.tables {
		refs = append(refs, dal.NewRootCollectionRef(name, ""))
	}
	return refs, f.tableErr
}

func (fakeSQLite) DescribeCollection(context.Context, *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return nil, nil
}

func (fakeSQLite) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (fakeSQLite) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, nil
}

func (fakeSQLite) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, nil
}

func (f fakeSQLite) ListSourceViews(context.Context) ([]dbschema.SourceViewDef, error) {
	defs := make([]dbschema.SourceViewDef, 0, len(f.views))
	for _, name := range f.views {
		defs = append(defs, dbschema.SourceViewDef{Name: name})
	}
	return defs, f.viewErr
}

func TestSQLiteEntriesErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := sqliteEntries(ctx, fakeSQLite{tableErr: errors.New("boom")}); err == nil || !strings.Contains(err.Error(), "list tables") {
		t.Fatalf("table error: %v", err)
	}
	if _, err := sqliteEntries(ctx, fakeSQLite{viewErr: errors.New("boom")}); err == nil || !strings.Contains(err.Error(), "list views") {
		t.Fatalf("view error: %v", err)
	}
	if _, err := sqliteEntries(ctx, nopDB{}); err == nil {
		t.Fatal("a database without schema reading was accepted")
	}
}

// nopDB has no schema capability at all.
type nopDB struct{ dal.DB }

func (nopDB) Adapter() dal.Adapter { return nil }

func TestSQLiteEntriesWithoutViewCapability(t *testing.T) {
	// A plain SchemaReader that is not a SourceViewReader: only tables.
	entries, err := sqliteEntries(context.Background(), tablesOnlyDB{inner: fakeSQLite{tables: []string{"A"}}})
	if err != nil || len(entries) != 1 || entries[0].Kind != schemaKindTable {
		t.Fatalf("got %+v, %v", entries, err)
	}
}

// tablesOnlyDB hides ListSourceViews of the embedded fake by not promoting it.
type tablesOnlyDB struct {
	dal.DB
	inner fakeSQLite
}

func (d tablesOnlyDB) ListCollections(ctx context.Context, p *record.Key) ([]dal.CollectionRef, error) {
	return d.inner.ListCollections(ctx, p)
}

func (d tablesOnlyDB) DescribeCollection(ctx context.Context, r *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return d.inner.DescribeCollection(ctx, r)
}

func (d tablesOnlyDB) ListIndexes(ctx context.Context, r *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return d.inner.ListIndexes(ctx, r)
}

func (d tablesOnlyDB) ListConstraints(ctx context.Context, r *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return d.inner.ListConstraints(ctx, r)
}

func (d tablesOnlyDB) ListReferrers(ctx context.Context, r *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return d.inner.ListReferrers(ctx, r)
}

// fakePostgres is a database with the PostgreSQL capabilities of
// dalgo2postgres.
type fakePostgres struct {
	dal.DB
	schemas    []string
	collection map[string][]string
	views      map[string][]string
	schemasErr error
	viewsErr   error
	collErr    error
}

func refsOf(schema string, names []string) []dal.CollectionRef {
	refs := make([]dal.CollectionRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, dal.NewQualifiedRootCollectionRef(schema, name, ""))
	}
	return refs
}

func (f fakePostgres) ListSchemas(context.Context) ([]string, error) { return f.schemas, f.schemasErr }

func (f fakePostgres) ListSchemaCollections(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	return refsOf(schema, f.collection[schema]), f.collErr
}

func (f fakePostgres) ListSchemaViews(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	return refsOf(schema, f.views[schema]), f.viewsErr
}

func TestDBSchemaPostgresListsSchemasSorted(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return fakePostgres{
			schemas:    []string{"sales", "public"},
			collection: map[string][]string{"public": {"users", "active_users", "accounts"}, "sales": {"orders"}},
			views:      map[string][]string{"public": {"active_users"}},
		}, nil
	})
	out, err := runDBSchema(t, "--db", "postgres://alice:hunter2@db.example.com:5432/shop?sslmode=require", "--format", "text")
	if err != nil {
		t.Fatal(err)
	}
	want := "provider: postgres\ntarget: postgres://db.example.com:5432/shop\n" +
		"public\ttable\taccounts\npublic\ttable\tusers\npublic\tview\tactive_users\nsales\ttable\torders\n"
	if out != want {
		t.Fatalf("got\n%q\nwant\n%q", out, want)
	}
}

func TestPostgresEntriesErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	cases := map[string]fakePostgres{
		"list schemas": {schemasErr: boom},
		"views":        {schemas: []string{"public"}, viewsErr: boom},
		"tables":       {schemas: []string{"public"}, collErr: boom},
	}
	for want, db := range cases {
		if _, err := postgresEntries(ctx, db); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v", want, err)
		}
	}
	if _, err := postgresEntries(ctx, nopDB{}); err == nil {
		t.Fatal("a source without schema listing was accepted")
	}
}

func TestDBSchemaListFailureIsRedacted(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
		return fakePostgres{schemasErr: errors.New("driver echoed postgres://alice:hunter2@db.example.com/shop")}, nil
	})
	_, err := runDBSchema(t, "--db", "postgres://alice:hunter2@db.example.com/shop")
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
}

func TestDBSchemaActionWithoutContext(t *testing.T) {
	path := sqliteWithViews(t)
	command := dbSchemaCommand()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Flags().Set("db", "sqlite://"+path); err != nil {
		t.Fatal(err)
	}
	if err := dbSchemaAction(command, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"Album"`) {
		t.Fatalf("unexpected output %s", out.String())
	}
}

func TestWriteSchemaListingJSONWriteError(t *testing.T) {
	if err := writeSchemaListing(failingWriter{}, "json", schemaListing{}); err == nil {
		t.Fatal("write error swallowed")
	}
}

func TestDBSchemaIsRegisteredUnderDB(t *testing.T) {
	var found *cobra.Command
	for _, sub := range dbCommand().Commands() {
		if sub.Name() == "schema" {
			found = sub
		}
	}
	if found == nil {
		t.Fatal("db schema is not registered")
	}
}
