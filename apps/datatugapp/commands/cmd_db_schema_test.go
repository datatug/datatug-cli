package commands

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
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
	want := [][3]string{
		{"main", "Album", "table"},
		{"main", "Zebra", "table"},
		{"main", "AlbumTitles", "view"},
	}
	if got.Provider != "sqlite" || len(got.Tables) != len(want) {
		t.Fatalf("unexpected listing: %+v", got)
	}
	for i := range want {
		if got.Tables[i].Schema != want[i][0] || got.Tables[i].Name != want[i][1] || got.Tables[i].Kind != want[i][2] {
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

func TestDBSchemaPostgresSourceThatCannotBeOpenedExitsWithTheDatabaseCode(t *testing.T) {
	failingServerOpeners(t)
	_, err := runDBSchema(t, "--db", "postgres://alice:hunter2@example.invalid/shop")
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), sourceSecret) {
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

func runDBSchemaJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, err := runDBSchema(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return doc
}

func tablesByName(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	byName := map[string]map[string]any{}
	for _, raw := range doc["tables"].([]any) {
		entry := raw.(map[string]any)
		byName[entry["name"].(string)] = entry
	}
	return byName
}

func TestDBSchemaColumnsAndCountsOnSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE Album (AlbumId INTEGER NOT NULL PRIMARY KEY, Title NVARCHAR(160) NOT NULL, Price NUMERIC(5,2) DEFAULT 9.99, Note TEXT)`,
		`INSERT INTO Album (AlbumId, Title) VALUES (1, 'a'), (2, 'b')`,
		`CREATE VIEW AlbumTitles AS SELECT Title FROM Album`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	plain := tablesByName(t, runDBSchemaJSON(t, "--db", "sqlite://"+path))
	if _, has := plain["Album"]["columns"]; has {
		t.Fatal("columns present without --columns")
	}
	if _, has := plain["Album"]["rowCount"]; has {
		t.Fatal("rowCount present without --counts")
	}

	doc := runDBSchemaJSON(t, "--db", "sqlite://"+path, "--columns", "--counts")
	tables := tablesByName(t, doc)
	if tables["Album"]["rowCount"] != float64(2) {
		t.Fatalf("rowCount = %v", tables["Album"]["rowCount"])
	}
	if _, has := tables["AlbumTitles"]["rowCount"]; has {
		t.Fatal("a view carries rowCount")
	}
	columns := tables["Album"]["columns"].([]any)
	first := columns[0].(map[string]any)
	if first["name"] != "AlbumId" || first["type"] != "INTEGER" || first["nullable"] != false || first["primaryKeyPosition"] != float64(1) {
		t.Fatalf("first column = %v", first)
	}
	title := columns[1].(map[string]any)
	if title["type"] != "NVARCHAR(160)" || title["nullable"] != false {
		t.Fatalf("title = %v", title)
	}
	price := columns[2].(map[string]any)
	if price["type"] != "NUMERIC(5,2)" || price["default"] != "9.99" || price["nullable"] != true {
		t.Fatalf("price = %v", price)
	}
	view := tables["AlbumTitles"]["columns"].([]any)
	if len(view) != 1 || view[0].(map[string]any)["name"] != "Title" || view[0].(map[string]any)["type"] != "" {
		t.Fatalf("view columns = %v", view)
	}
}

func TestSchemaColumnsFallBackToPortableDefinition(t *testing.T) {
	def := &dbschema.CollectionDef{
		Name:       "t",
		Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int, Nullable: false}, {Name: "n", Type: dbschema.String, Nullable: true}},
		PrimaryKey: []dal.FieldName{"id"},
	}
	columns := columnsOf(def)
	if len(columns) != 2 || columns[0].Type != dbschema.Int.String() || columns[0].PrimaryKeyPosition != 1 || columns[1].PrimaryKeyPosition != 0 || !columns[1].Nullable {
		t.Fatalf("columns = %+v", columns)
	}
}

func TestAddSchemaColumnsUnreadableTableIsNull(t *testing.T) {
	entries := []schemaEntry{{Name: "x", Kind: schemaKindTable}}
	addSchemaColumns(context.Background(), nopDB{}, "sqlite", entries)
	out, err := json.Marshal(entries[0])
	if err != nil || !strings.Contains(string(out), `"columns":null`) {
		t.Fatalf("got %s, %v", out, err)
	}
}

func TestAddSchemaCountsUncountableIsNull(t *testing.T) {
	entries := []schemaEntry{{Schema: "public", Name: "x", Kind: schemaKindTable}, {Schema: "public", Name: "v", Kind: schemaKindView}}
	addSchemaCounts(context.Background(), nopDB{}, "postgres", entries)
	out, err := json.Marshal(entries[0])
	if err != nil || !strings.Contains(string(out), `"rowCount":null`) {
		t.Fatalf("got %s, %v", out, err)
	}
	if entries[1].RowCount != nil {
		t.Fatal("a view was counted")
	}
}

func TestDBSchemaPathDetection(t *testing.T) {
	dir := t.TempDir()
	good := sqliteWithViews(t)
	noExt := filepath.Join(dir, "chinook")
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(noExt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(dir, "SHOP.DB3")
	if err = os.WriteFile(upper, data, 0o600); err != nil {
		t.Fatal(err)
	}

	byName := runDBSchemaJSON(t, "--db", good)
	if byName["detectedFrom"] != "file name" || byName["provider"] != "sqlite" {
		t.Fatalf("by name: %v", byName)
	}
	if runDBSchemaJSON(t, "--db", upper)["detectedFrom"] != "file name" {
		t.Fatal("extension is not case-insensitive")
	}
	if runDBSchemaJSON(t, "--db", noExt)["detectedFrom"] != "file content" {
		t.Fatal("header not detected")
	}
	if runDBSchemaJSON(t, "--db", "sqlite://"+good)["detectedFrom"] != "scheme" {
		t.Fatal("scheme not reported")
	}
}

func TestResolveSchemaSourceRelativeAndHome(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	source, from, err := resolveSchemaSource("shop.sqlite")
	resolved, _ := filepath.EvalSymlinks(dir)
	if err != nil || from != detectedFromName || !strings.HasSuffix(source, filepath.Join(filepath.Base(resolved), "shop.sqlite")) || !strings.HasPrefix(source, "sqlite://") {
		t.Fatalf("relative: %q %q %v", source, from, err)
	}
	original := userHomeDir
	t.Cleanup(func() { userHomeDir = original })
	userHomeDir = func() (string, error) { return "/home/tester", nil }
	for _, arg := range []string{"~/data/x.sqlite"} {
		source, _, err = resolveSchemaSource(arg)
		if err != nil || source != "sqlite:///home/tester/data/x.sqlite" {
			t.Fatalf("home: %q %v", source, err)
		}
	}
	userHomeDir = func() (string, error) { return "", errors.New("no home") }
	if _, _, err = resolveSchemaSource("~/x.sqlite"); err == nil {
		t.Fatal("home error swallowed")
	}
}

func TestResolveSchemaSourcePassesURLsAndEmptyThrough(t *testing.T) {
	for _, arg := range []string{"", "env:SOME_VAR", "postgres://h/d", "sqlite:rel.db"} {
		source, from, err := resolveSchemaSource(arg)
		if err != nil || source != arg || from != detectedFromScheme {
			t.Fatalf("%q: %q %q %v", arg, source, from, err)
		}
	}
}

func TestResolveSchemaSourceRefusals(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.txt")
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(text, []byte("hello, this is a text file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(short, []byte("SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{text, short, filepath.Join(dir, "missing"), dir} {
		if _, _, err := resolveSchemaSource(arg); err == nil || !strings.Contains(err.Error(), "not a recognised database file") || strings.Contains(err.Error(), "\n") {
			t.Fatalf("%q: %v", arg, err)
		}
	}
	for _, name := range []string{"a?b.sqlite", "a#b.sqlite", "a@b.sqlite", "a%41.sqlite"} {
		if _, _, err := resolveSchemaSource(filepath.Join(dir, name)); err == nil || !strings.Contains(err.Error(), "path not supported") {
			t.Fatalf("%q: %v", name, err)
		}
	}
}

func TestDBSchemaUnrecognisedFileExitsUsage(t *testing.T) {
	text := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(text, []byte("plain text"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runDBSchema(t, "--db", text)
	if err == nil || exitCodeOf(t, err) != exitCodeUsage {
		t.Fatalf("got %v", err)
	}
}

func TestRowCountMarshal(t *testing.T) {
	n := 3
	for in, want := range map[rowCount]string{{n: &n}: "3", {}: "null"} {
		out, err := json.Marshal(in)
		if err != nil || string(out) != want {
			t.Fatalf("got %s, %v", out, err)
		}
	}
}

// failingCountDB declares a native COUNT and fails every query.
type failingCountDB struct{ dal.DB }

func (failingCountDB) QueryCapabilities() dal.QueryCapabilities {
	return dal.QueryCapabilities{Aggregate: dal.AggregateCapabilities{Count: true}}
}

func (failingCountDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return nil, errors.New("permission denied")
}

func TestAddSchemaCountsFailedCountIsNull(t *testing.T) {
	entries := []schemaEntry{{Schema: "public", Name: "x", Kind: schemaKindTable}}
	addSchemaCounts(context.Background(), failingCountDB{}, "postgres", entries)
	if entries[0].RowCount == nil || entries[0].RowCount.n != nil {
		t.Fatalf("got %+v", entries[0].RowCount)
	}
}

func TestResolveSchemaSourceUnresolvableWorkingDirectory(t *testing.T) {
	original := absolutePath
	t.Cleanup(func() { absolutePath = original })
	absolutePath = func(string) (string, error) { return "", errors.New("no working directory") }
	if _, _, err := resolveSchemaSource("shop.sqlite"); err == nil || !strings.Contains(err.Error(), "cannot resolve") {
		t.Fatalf("got %v", err)
	}
}

func TestDBSchemaColumnsListForeignKeys(t *testing.T) {
	path := sqliteShop(t)
	doc := runDBSchemaJSON(t, "--db", path, "--columns")
	tables := tablesByName(t, doc)
	keys := tables["Album"]["foreignKeys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("Album foreignKeys = %v", keys)
	}
	key := keys[0].(map[string]any)
	ref := key["refTable"].(map[string]any)
	if key["columns"].([]any)[0] != "ArtistId" || ref["name"] != "Artist" || ref["schema"] != "main" || key["refColumns"].([]any)[0] != "ArtistId" || key["name"] == "" {
		t.Fatalf("key = %v", key)
	}
	if got := tables["Artist"]["foreignKeys"]; len(got.([]any)) != 0 {
		t.Fatalf("Artist foreignKeys = %v, want an empty list", got)
	}
	if _, has := tables["AlbumTitles"]["foreignKeys"]; has {
		t.Fatal("a view carries foreignKeys")
	}
	if _, has := tablesByName(t, runDBSchemaJSON(t, "--db", path))["Album"]["foreignKeys"]; has {
		t.Fatal("foreignKeys without --columns")
	}
}

func TestAddSchemaColumnsUnreadableTableHasNoForeignKeys(t *testing.T) {
	entries := []schemaEntry{{Name: "x", Kind: schemaKindTable}}
	addSchemaColumns(context.Background(), nopDB{}, "sqlite", entries)
	if entries[0].ForeignKeys != nil {
		t.Fatal("foreign keys of a table that could not be read")
	}
}

func TestAddSchemaColumnsDescribedViewHasNoForeignKeys(t *testing.T) {
	// A PostgreSQL view is described like a table; it still holds no keys.
	db := fakeCatalog{defs: map[string]*dbschema.CollectionDef{
		"public.v": {Name: "v", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}},
		"public.t": {Name: "t", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}},
	}}
	entries := []schemaEntry{
		{Schema: "public", Name: "v", Kind: schemaKindView, ref: dal.NewQualifiedRootCollectionRef("public", "v", "")},
		{Schema: "public", Name: "t", Kind: schemaKindTable, ref: dal.NewQualifiedRootCollectionRef("public", "t", "")},
	}
	addSchemaColumns(context.Background(), db, "postgres", entries)
	if entries[0].Columns == nil || entries[0].ForeignKeys != nil {
		t.Fatalf("view: columns=%v foreignKeys=%v", entries[0].Columns, entries[0].ForeignKeys)
	}
	if entries[1].ForeignKeys == nil || len(*entries[1].ForeignKeys) != 0 {
		t.Fatalf("table: %v", entries[1].ForeignKeys)
	}
}
