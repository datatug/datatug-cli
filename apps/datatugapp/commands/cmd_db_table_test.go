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
	"github.com/spf13/cobra"
)

func runMetadata(t *testing.T, command *cobra.Command, args ...string) (string, error) {
	t.Helper()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func metadataJSON(t *testing.T, command *cobra.Command, args ...string) map[string]any {
	t.Helper()
	out, err := runMetadata(t, command, args...)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if strings.Contains(out, "$key") || strings.Contains(out, "__dalgo_record_id") {
		t.Fatalf("internal key in output:\n%s", out)
	}
	return doc
}

func sqliteShop(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE Artist (ArtistId INTEGER PRIMARY KEY, Name NVARCHAR(120))`,
		`CREATE TABLE Album (AlbumId INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, Title NVARCHAR(160) NOT NULL, Price NUMERIC(5,2) DEFAULT 9.99,
			ArtistId INTEGER NOT NULL REFERENCES Artist(ArtistId) ON DELETE CASCADE, Active BOOLEAN, Added DATETIME, Cover BLOB)`,
		`CREATE INDEX AlbumByTitle ON Album(Title) WHERE Active = 1`,
		`CREATE INDEX AlbumLower ON Album(lower(Title))`,
		`CREATE UNIQUE INDEX AlbumArtistTitle ON Album(ArtistId, Title)`,
		`CREATE VIEW AlbumTitles AS SELECT Title FROM Album`,
		`INSERT INTO Artist VALUES (1,'a')`,
		`INSERT INTO Album (Title, ArtistId) VALUES ('x',1),('y',1)`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func columnNamed(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	for _, raw := range doc["columns"].([]any) {
		if column := raw.(map[string]any); column["name"] == name {
			return column
		}
	}
	t.Fatalf("no column %s", name)
	return nil
}

func TestDBTableSQLiteInFull(t *testing.T) {
	path := sqliteShop(t)
	doc := metadataJSON(t, dbTableCommand(), "Album", "--db", path, "--counts")
	if doc["provider"] != "sqlite" || doc["schema"] != "main" || doc["kind"] != "table" || doc["detectedFrom"] != "file name" || doc["rowCount"] != float64(2) {
		t.Fatalf("header: %v", doc)
	}
	if pk := doc["primaryKey"].([]any); len(pk) != 1 || pk[0] != "AlbumId" {
		t.Fatalf("primaryKey = %v", pk)
	}
	price := columnNamed(t, doc, "Price")
	if price["type"] != "NUMERIC(5,2)" || price["precision"] != float64(5) || price["scale"] != float64(2) || price["default"] != "9.99" || price["family"] != "numeric" || price["ordinal"] != float64(3) {
		t.Fatalf("price = %v", price)
	}
	title := columnNamed(t, doc, "Title")
	if title["length"] != float64(160) || title["family"] != "text" || title["nullable"] != false {
		t.Fatalf("title = %v", title)
	}
	id := columnNamed(t, doc, "AlbumId")
	if id["autoIncrement"] != true || id["primaryKeyPosition"] != float64(1) {
		t.Fatalf("id = %v", id)
	}
	for name, family := range map[string]string{"Active": "boolean", "Added": "date", "Cover": "binary"} {
		if got := columnNamed(t, doc, name)["family"]; got != family {
			t.Errorf("%s family = %v, want %s", name, got, family)
		}
	}
	artistID := columnNamed(t, doc, "ArtistId")
	fk := artistID["foreignKeys"].([]any)
	if len(fk) != 1 || fk[0].(map[string]any)["referencedTable"] != "Artist" || fk[0].(map[string]any)["onDelete"] != "CASCADE" || fk[0].(map[string]any)["referencedSchema"] != "main" {
		t.Fatalf("foreign keys = %v", fk)
	}
	if len(doc["foreignKeys"].([]any)) != 1 {
		t.Fatalf("table foreignKeys = %v", doc["foreignKeys"])
	}
	indexes := map[string]map[string]any{}
	for _, raw := range doc["indexes"].([]any) {
		index := raw.(map[string]any)
		indexes[index["name"].(string)] = index
	}
	if indexes["AlbumByTitle"]["partial"] != true || indexes["AlbumLower"]["expression"] != true || indexes["AlbumArtistTitle"]["unique"] != true {
		t.Fatalf("indexes = %v", indexes)
	}
	if len(indexes["AlbumArtistTitle"]["columns"].([]any)) != 2 {
		t.Fatalf("composite index = %v", indexes["AlbumArtistTitle"])
	}
	if len(doc["constraints"].([]any)) == 0 {
		t.Fatal("no constraints listed")
	}
	if !strings.Contains(strings.Join(anyStrings(doc["unsupported"]), ","), "checkConstraints") {
		t.Fatalf("unsupported = %v", doc["unsupported"])
	}

	artist := metadataJSON(t, dbTableCommand(), "Artist", "--db", path)
	referencedBy := artist["referencedBy"].([]any)
	if len(referencedBy) != 1 || referencedBy[0].(map[string]any)["table"] != "Album" {
		t.Fatalf("referencedBy = %v", referencedBy)
	}
	if _, has := artist["rowCount"]; has {
		t.Fatal("rowCount without --counts")
	}
	byColumn := columnNamed(t, artist, "ArtistId")["referencedBy"].([]any)
	if len(byColumn) != 1 {
		t.Fatalf("column referencedBy = %v", byColumn)
	}
}

func anyStrings(value any) []string {
	var out []string
	for _, item := range value.([]any) {
		out = append(out, item.(string))
	}
	return out
}

func TestDBTableViewHasDefinitionAndNoRowCount(t *testing.T) {
	doc := metadataJSON(t, dbTableCommand(), "AlbumTitles", "--db", sqliteShop(t), "--counts")
	if doc["kind"] != "view" || !strings.Contains(doc["definition"].(string), "SELECT Title FROM Album") {
		t.Fatalf("view = %v", doc)
	}
	if _, has := doc["rowCount"]; has {
		t.Fatal("a view carries rowCount")
	}
	if columns := doc["columns"].([]any); len(columns) != 1 || columns[0].(map[string]any)["name"] != "Title" {
		t.Fatalf("view columns = %v", columns)
	}
}

func TestDBColumn(t *testing.T) {
	path := sqliteShop(t)
	doc := metadataJSON(t, dbColumnCommand(), "Album", "ArtistId", "--db", path)
	column := doc["column"].(map[string]any)
	if doc["table"] != "Album" || doc["schema"] != "main" || column["name"] != "ArtistId" || len(column["foreignKeys"].([]any)) != 1 {
		t.Fatalf("column doc = %v", doc)
	}
	if !strings.Contains(strings.Join(anyStrings(doc["unsupported"]), ","), "columns.collation") {
		t.Fatalf("unsupported = %v", doc["unsupported"])
	}
}

func TestDBMetadataText(t *testing.T) {
	path := sqliteShop(t)
	out, err := runMetadata(t, dbTableCommand(), "Album", "--db", path, "--format", "text", "--counts")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"table main.Album", "1\tAlbumId\tINTEGER\tnumeric\tNOT NULL\tPK1", "primary key: AlbumId", "foreign key: (ArtistId) -> main.Artist (ArtistId)", "index: AlbumArtistTitle (ArtistId, Title) unique=true", "row count: 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	out, err = runMetadata(t, dbTableCommand(), "Artist", "--db", path, "--format", "text")
	if err != nil || !strings.Contains(out, "referenced by: main.Album (ArtistId)") || strings.Contains(out, "row count") {
		t.Fatalf("artist text: %v\n%s", err, out)
	}
	out, err = runMetadata(t, dbColumnCommand(), "Artist", "Name", "--db", path, "--format", "text")
	if err != nil || !strings.Contains(out, "2\tName\tNVARCHAR(120)\ttext\tNULL") {
		t.Fatalf("column text: %v\n%s", err, out)
	}
}

func TestRowCountText(t *testing.T) {
	n := 4
	if rowCountText(rowCount{n: &n}) != "4" || rowCountText(rowCount{}) != "unknown" {
		t.Fatal("row count text")
	}
	var out bytes.Buffer
	writeTableText(&out, tableDetail{RowCount: &rowCount{}})
	if !strings.Contains(out.String(), "row count: unknown") {
		t.Fatalf("got %s", out.String())
	}
}

func TestDBMetadataRefusals(t *testing.T) {
	path := sqliteShop(t)
	cases := map[string]struct {
		command *cobra.Command
		args    []string
	}{
		"missing table":    {dbTableCommand(), []string{"Nope", "--db", path}},
		"missing column":   {dbColumnCommand(), []string{"Album", "Nope", "--db", path}},
		"column of absent": {dbColumnCommand(), []string{"Nope", "Title", "--db", path}},
		"bad format":       {dbTableCommand(), []string{"Album", "--db", path, "--format", "yaml"}},
		"bad schema":       {dbTableCommand(), []string{"Album", "--db", path, "--schema", "other"}},
		"unsupported":      {dbTableCommand(), []string{"Album", "--db", "ingitdb://./x"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := runMetadata(t, tc.command, tc.args...)
			if err == nil || exitCodeOf(t, err) != exitCodeUsage || strings.Contains(err.Error(), "\n") {
				t.Fatalf("got %v", err)
			}
			if out != "" {
				t.Fatalf("output on error: %s", out)
			}
		})
	}
	if _, err := runMetadata(t, dbTableCommand(), "--db", path); err == nil {
		t.Fatal("missing argument accepted")
	}
}

// fakeCatalog is a PostgreSQL-like source with several schemas.
type fakeCatalog struct {
	dal.DB
	tables         map[string][]string
	defs           map[string]*dbschema.CollectionDef
	describeErr    map[string]error
	constraintsErr error
	referrers      map[string][]dbschema.Referrer
	referrersErr   error
	listErr        error
}

func (f fakeCatalog) ListSchemas(context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return []string{"public", "sales"}, nil
}

func (f fakeCatalog) ListSchemaCollections(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	return refsOf(schema, f.tables[schema]), nil
}

func (f fakeCatalog) ListSchemaViews(context.Context, string) ([]dal.CollectionRef, error) {
	return nil, nil
}

func (f fakeCatalog) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, nil
}

func (f fakeCatalog) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	key := ref.Schema() + "." + ref.Name()
	if err := f.describeErr[key]; err != nil {
		return nil, err
	}
	if def := f.defs[key]; def != nil {
		return def, nil
	}
	return nil, errors.New("collection not found")
}

func (f fakeCatalog) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (f fakeCatalog) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return []dbschema.ConstraintDef{{Name: "orders_pkey", Type: "PRIMARY KEY"}}, f.constraintsErr
}

func (f fakeCatalog) ListReferrers(_ context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return f.referrers[ref.Schema()+"."+ref.Name()], f.referrersErr
}

func catalogFixture() fakeCatalog {
	return fakeCatalog{
		tables: map[string][]string{"public": {"orders", "customers"}, "sales": {"orders"}},
		defs: map[string]*dbschema.CollectionDef{
			"public.orders": {
				Name:       "orders",
				Fields:     []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}, {Name: "customer", Type: dbschema.String, Nullable: true}, {Name: "note", Type: dbschema.Null}},
				PrimaryKey: []dal.FieldName{"id"},
				ForeignKeys: []dbschema.ForeignKeyDef{{Name: "orders_customer_fk", Fields: []dal.FieldName{"customer"}, ReferencedCollection: "customers",
					ReferencedNamespace: "public", ReferencedFields: []dal.FieldName{"id"}}, {Name: "x", Fields: []dal.FieldName{"id"}, ReferencedCollection: "customers", ReferencedFields: []dal.FieldName{"id"}}},
				Indexes: []dbschema.IndexDef{{Name: "orders_customer_idx", Fields: []dal.FieldName{"customer"}}},
			},
			"public.customers": {
				Name: "customers", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}, PrimaryKey: []dal.FieldName{"id"},
			},
			"sales.orders": {Name: "orders", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.Int}}},
		},
		referrers: map[string][]dbschema.Referrer{
			"public.customers": {
				{Collection: dal.NewQualifiedRootCollectionRef("public", "orders", ""), Fields: []dal.FieldName{"customer"}},
				{Collection: dal.NewQualifiedRootCollectionRef("public", "gone", ""), Fields: []dal.FieldName{"c"}},
				{Collection: dal.NewQualifiedRootCollectionRef("sales", "orders", ""), Fields: []dal.FieldName{"id"}},
			},
		},
	}
}

func useCatalog(t *testing.T, catalog fakeCatalog) {
	t.Helper()
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	withSchemaSource(t, func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return catalog, nil })
}

const catalogURL = "postgres://alice:hunter2@db.example.com/shop?sslmode=require"

func TestDBTablePostgresLikeCatalog(t *testing.T) {
	useCatalog(t, catalogFixture())
	doc := metadataJSON(t, dbTableCommand(), "orders", "--db", catalogURL, "--schema", "public")
	if doc["schema"] != "public" || doc["target"] != "postgres://db.example.com/shop" {
		t.Fatalf("header: %v", doc)
	}
	fks := doc["foreignKeys"].([]any)
	if fks[0].(map[string]any)["referencedSchema"] != "public" || fks[1].(map[string]any)["referencedSchema"] != "public" {
		t.Fatalf("fks = %v", fks)
	}
	indexes := doc["indexes"].([]any)
	if len(indexes) != 1 || indexes[0].(map[string]any)["name"] != "orders_customer_idx" {
		t.Fatalf("indexes = %v", indexes)
	}
	if note := columnNamed(t, doc, "note"); note["family"] != "other" {
		t.Fatalf("note = %v", note)
	}
	customers := metadataJSON(t, dbTableCommand(), "customers", "--db", catalogURL)
	incoming := customers["referencedBy"].([]any)
	if len(incoming) != 4 {
		t.Fatalf("referencedBy = %v", incoming)
	}
	if incoming[2].(map[string]any)["table"] != "gone" || incoming[3].(map[string]any)["schema"] != "sales" {
		t.Fatalf("fallback referrers = %v", incoming)
	}
	if strings.Contains(strings.Join(anyStrings(customers["unsupported"]), ","), "constraints") && !strings.Contains(strings.Join(anyStrings(customers["unsupported"]), ","), "uniqueConstraints") {
		t.Fatal("unexpected unsupported")
	}
}

func TestDBTableCatalogAmbiguousAndUnreadable(t *testing.T) {
	useCatalog(t, catalogFixture())
	_, err := runMetadata(t, dbTableCommand(), "orders", "--db", catalogURL)
	if err == nil || exitCodeOf(t, err) != exitCodeUsage || !strings.Contains(err.Error(), "public, sales") {
		t.Fatalf("ambiguous: %v", err)
	}

	broken := catalogFixture()
	broken.describeErr = map[string]error{"public.customers": errors.New("driver echoed postgres://alice:hunter2@db.example.com/shop")}
	useCatalog(t, broken)
	_, err = runMetadata(t, dbTableCommand(), "customers", "--db", catalogURL)
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("describe failure: %v", err)
	}

	listing := catalogFixture()
	listing.listErr = errors.New("boom")
	useCatalog(t, listing)
	_, err = runMetadata(t, dbColumnCommand(), "customers", "id", "--db", catalogURL)
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase {
		t.Fatalf("list failure: %v", err)
	}
}

func TestDBTableCatalogWithoutConstraintsOrReferrers(t *testing.T) {
	catalog := catalogFixture()
	catalog.constraintsErr = errors.New("not supported")
	catalog.referrersErr = errors.New("not supported")
	useCatalog(t, catalog)
	doc := metadataJSON(t, dbTableCommand(), "customers", "--db", catalogURL, "--schema", "public")
	if !strings.Contains(strings.Join(anyStrings(doc["unsupported"]), ","), "constraints") || len(doc["referencedBy"].([]any)) != 0 {
		t.Fatalf("doc = %v", doc)
	}
}

func TestFamilyOfTypeAndSizes(t *testing.T) {
	for declared, want := range map[string]string{
		"jsonb": familyJSON, "boolean": familyBoolean, "timestamp without time zone": familyDate, "interval": familyDate, "bytea": familyBinary,
		"character varying(20)": familyText, "uuid": familyText, "point": familyOther, "bigint": familyNumeric, "double precision": familyNumeric, "money": familyNumeric,
	} {
		if got := familyOfType(declared, dbschema.Null); got != want {
			t.Errorf("%s: %s, want %s", declared, got, want)
		}
	}
	for portable, want := range map[dbschema.Type]string{
		dbschema.Int: familyNumeric, dbschema.Float: familyNumeric, dbschema.Decimal: familyNumeric, dbschema.String: familyText,
		dbschema.Time: familyDate, dbschema.Bool: familyBoolean, dbschema.Bytes: familyBinary, dbschema.Null: familyOther,
	} {
		if got := familyOfType("weird", portable); got != want {
			t.Errorf("portable %v: %s, want %s", portable, got, want)
		}
	}
	length, precision, scale := typeSizes("VARCHAR(20)", familyText)
	if *length != 20 || precision != nil || scale != nil {
		t.Fatal("varchar sizes")
	}
	length, precision, scale = typeSizes("DECIMAL(10)", familyNumeric)
	if length != nil || *precision != 10 || scale != nil {
		t.Fatal("single numeric size is a precision")
	}
	length, precision, scale = typeSizes("TEXT", familyText)
	if length != nil || precision != nil || scale != nil {
		t.Fatal("no sizes")
	}
}

func TestDBColumnDescribeFailure(t *testing.T) {
	broken := catalogFixture()
	broken.describeErr = map[string]error{"public.customers": errors.New("boom")}
	useCatalog(t, broken)
	_, err := runMetadata(t, dbColumnCommand(), "customers", "id", "--db", catalogURL, "--schema", "public")
	if err == nil || exitCodeOf(t, err) != exitCodeDatabase {
		t.Fatalf("got %v", err)
	}
}
