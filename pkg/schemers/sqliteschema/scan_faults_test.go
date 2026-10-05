package sqliteschema

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// These tests are the mistakes a SQLite file can hold that a scan must read past: its
// own tables, objects with no name, a view that cannot work, a foreign key to a table
// that is not there. The same cases are run through the whole scan, on files, in
// pkg/api (TestScanDbCatalog_SQLite3_*) and in the journey of the command.

// memoryDB is an in-memory database that has run statements.
func memoryDB(t *testing.T, statements ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// One connection: every connection to ":memory:" is a database of its own.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range statements {
		if _, err = db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return db
}

func providerOf(db *sql.DB, warnings io.Writer) schemaProvider {
	return NewSchemaProviderWithWarnings(func() (*sql.DB, error) { return db, nil }, warnings).(schemaProvider)
}

func collectionNames(t *testing.T, provider schemaProvider) []string {
	t.Helper()
	reader, err := provider.GetCollections(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetCollections: %v", err)
	}
	var names []string
	for {
		collection, err := reader.NextCollection()
		if err == io.EOF {
			sort.Strings(names)
			return names
		}
		if err != nil {
			t.Fatalf("NextCollection: %v", err)
		}
		names = append(names, collection.DbType+" "+collection.Name())
	}
}

func equalStrings(t *testing.T, got, want []string, what string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func TestGetCollections_OwnTablesAreNotListed(t *testing.T) {
	db := memoryDB(t,
		`CREATE TABLE counter (id INTEGER PRIMARY KEY AUTOINCREMENT, label TEXT)`,
		`INSERT INTO counter (label) VALUES ('a')`,
		`ANALYZE`,
		`CREATE VIEW labels AS SELECT label FROM counter`,
	)
	var all []string
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE name LIKE 'sqlite\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		all = append(all, name)
	}
	_ = rows.Close()
	if len(all) < 2 {
		t.Fatalf("the file should hold SQLite's own tables, found %q", all)
	}

	var warnings bytes.Buffer
	equalStrings(t, collectionNames(t, providerOf(db, &warnings)), []string{"BASE TABLE counter", "VIEW labels"}, "the collections")
	if warnings.Len() != 0 {
		t.Errorf("SQLite's own tables are not named: %q", warnings.String())
	}

	// The same goes for the list of tables alone.
	reader, err := getCollections(db, collectionsFilter{CollectionType: datatug.CollectionTypeTable})
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	tableRows := reader.(*collectionsReader).rows
	for tableRows.Next() {
		var name, sqlText string
		if err = tableRows.Scan(&name, &sqlText); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	equalStrings(t, tables, []string{"counter"}, "the tables")
}

func TestGetCollections_AnObjectWithNoNameIsNamedAndLeftOut(t *testing.T) {
	for kind, statement := range map[string]string{
		"table": `CREATE TABLE "" (a INTEGER)`,
		"view":  `CREATE VIEW "" AS SELECT 1 AS x`,
	} {
		t.Run(kind, func(t *testing.T) {
			db := memoryDB(t, `CREATE TABLE kept (id INTEGER PRIMARY KEY)`, statement)
			var warnings bytes.Buffer

			names := collectionNames(t, providerOf(db, &warnings))

			equalStrings(t, names, []string{"BASE TABLE kept"}, "the collections")
			want := `warning: ` + kind + ` "" of schema "main" is left out of the project: its name is empty` + "\n"
			if warnings.String() != want {
				t.Errorf("warnings = %q, want %q", warnings.String(), want)
			}
			// Without anywhere to say it, the object is still left out.
			equalStrings(t, collectionNames(t, providerOf(db, nil)), []string{"BASE TABLE kept"}, "the collections, with no warnings stream")
		})
	}
}

func TestGetCollections_AViewThatCannotWorkIsNamedAndLeftOut(t *testing.T) {
	db := memoryDB(t,
		`CREATE TABLE doomed (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE kept (id INTEGER PRIMARY KEY)`,
		`CREATE VIEW broken AS SELECT id FROM doomed`,
		`CREATE VIEW "it's" AS SELECT id FROM kept`,
		`DROP TABLE doomed`,
	)
	var warnings bytes.Buffer

	names := collectionNames(t, providerOf(db, &warnings))

	equalStrings(t, names, []string{"BASE TABLE kept", "VIEW it's"}, "the collections")
	if !strings.HasPrefix(warnings.String(), `warning: view "broken" of schema "main" is left out of the project: its definition cannot be read: "`) ||
		!strings.Contains(warnings.String(), "doomed") || strings.Count(warnings.String(), "\n") != 1 {
		t.Errorf("warnings = %q", warnings.String())
	}
}

func TestGetCollections_Errors(t *testing.T) {
	t.Run("the database cannot be had", func(t *testing.T) {
		provider := NewSchemaProvider(func() (*sql.DB, error) { return nil, errors.New("no database") })
		if _, err := provider.GetCollections(context.Background(), nil); err == nil {
			t.Error("expected the error of the database")
		}
	})
	t.Run("the list cannot be read", func(t *testing.T) {
		closed, _ := sql.Open("sqlite3", ":memory:")
		_ = closed.Close()
		if _, err := providerOf(closed, nil).GetCollections(context.Background(), nil); err == nil {
			t.Error("expected an error for a closed database")
		}
	})
	t.Run("a row of the list fails", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		rows := sqlmock.NewRows([]string{"type", "name", "sql"}).AddRow("table", "a", "").RowError(0, errors.New("disk I/O"))
		mock.ExpectQuery("SELECT type, name, sql FROM sqlite_schema").WillReturnRows(rows)
		if _, err = providerOf(db, nil).GetCollections(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "disk I/O") {
			t.Errorf("err = %v, want the error of the row", err)
		}
	})
}

func TestSliceCollectionsReader(t *testing.T) {
	a, b := &datatug.CollectionInfo{}, &datatug.CollectionInfo{}
	reader := &sliceCollectionsReader{collections: []*datatug.CollectionInfo{a, b}}
	for _, want := range []*datatug.CollectionInfo{a, b} {
		got, err := reader.NextCollection()
		if err != nil || got != want {
			t.Fatalf("NextCollection = %v, %v", got, err)
		}
	}
	if _, err := reader.NextCollection(); err != io.EOF {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

func TestWarnf(t *testing.T) {
	warnf(nil, "nothing %s", "said")
	var out bytes.Buffer
	warnf(&out, "%s %d", "said", 2)
	if out.String() != "said 2" {
		t.Errorf("warnf wrote %q", out.String())
	}
}

func constraintsOf(t *testing.T, provider schemaProvider, table string) []*schemer.Constraint {
	t.Helper()
	reader, err := provider.GetConstraints(context.Background(), "", "main", table)
	if err != nil {
		t.Fatalf("GetConstraints(%s): %v", table, err)
	}
	var constraints []*schemer.Constraint
	for {
		constraint, err := reader.NextConstraint()
		if err == io.EOF {
			return constraints
		}
		if err != nil {
			t.Fatal(err)
		}
		constraints = append(constraints, constraint)
	}
}

func TestGetConstraints_ForeignKeyToATableThatIsNotInTheFile(t *testing.T) {
	db := memoryDB(t,
		`CREATE TABLE Parent (a INTEGER, b INTEGER, PRIMARY KEY (a, b))`,
		`CREATE TABLE child (
			id INTEGER PRIMARY KEY,
			gone_a INTEGER, gone_b INTEGER,
			p_a INTEGER, p_b INTEGER,
			FOREIGN KEY (gone_a, gone_b) REFERENCES gone(a, b),
			FOREIGN KEY (p_a, p_b) REFERENCES PARENT(a, b))`,
		`CREATE VIEW parent_view AS SELECT a FROM Parent`,
		`CREATE TABLE viewref (id INTEGER, v INTEGER REFERENCES parent_view(a))`,
	)
	var warnings bytes.Buffer

	child := constraintsOf(t, providerOf(db, &warnings), "child")

	var kept []string
	for _, constraint := range child {
		if constraint.Type == "FOREIGN KEY" {
			kept = append(kept, constraint.ColumnName+"->"+constraint.RefTableName+"."+constraint.RefColName)
		}
	}
	equalStrings(t, kept, []string{"p_a->Parent.a", "p_b->Parent.b"}, "the foreign keys that are read, to the table as the file spells it")
	wantWarning := `warning: table "child" of schema "main" has a foreign key to "gone", which is not a table of the database: the reference is not read` + "\n"
	if warnings.String() != wantWarning {
		t.Errorf("a two-column foreign key is named once: %q, want %q", warnings.String(), wantWarning)
	}

	// A foreign key to a view is not to a table.
	warnings.Reset()
	if got := constraintsOf(t, providerOf(db, &warnings), "viewref"); len(got) != 0 {
		t.Errorf("constraints of viewref = %d, want none", len(got))
	}
	if !strings.Contains(warnings.String(), `"parent_view"`) {
		t.Errorf("warnings = %q", warnings.String())
	}
}

func TestGetConstraints_TheTableCannotBeLookedUp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectQuery("PRAGMA foreign_key_list").WillReturnRows(
		sqlmock.NewRows([]string{"id", "seq", "table", "from", "to", "on_update", "on_delete", "match"}).
			AddRow(0, 0, "parent", "p", "id", "NO ACTION", "NO ACTION", "NONE"))
	mock.ExpectQuery("SELECT name FROM sqlite_schema").WillReturnError(errors.New("disk I/O"))

	_, err = providerOf(db, nil).GetConstraints(context.Background(), "", "main", "child")

	if err == nil || !strings.Contains(err.Error(), "failed to look up the table a foreign key refers to") || !strings.Contains(err.Error(), "disk I/O") {
		t.Errorf("err = %v", err)
	}
}
