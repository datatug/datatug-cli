package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/joinmeta"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type participantDoc struct {
	Instance string                        `json:"instance"`
	Schema   string                        `json:"schema"`
	Relation string                        `json:"relation"`
	DB       *string                       `json:"db"`
	Kind     string                        `json:"kind"`
	Role     string                        `json:"role"`
	Join     *queryresults.ParticipantJoin `json:"join"`
}

type describedDoc struct {
	Participants []participantDoc       `json:"participants"`
	Related      []queryresults.Related `json:"related"`
	Unavailable  map[string]string      `json:"unavailable"`
	Columns      []queryresults.Column  `json:"columns"`
}

func readDescribed(t *testing.T, root, id string) (describedDoc, string) {
	t.Helper()
	_, _, text := readResult(t, root, id)
	var doc describedDoc
	require.NoError(t, json.Unmarshal([]byte(text), &doc))
	return doc, text
}

// relatedLines is "<from> <direction> <schema>.<target> <cardinality> <a=b,...>" per candidate.
func relatedLines(doc describedDoc) []string {
	var lines []string
	for _, related := range doc.Related {
		var fields []string
		for _, field := range related.Fields {
			fields = append(fields, field.SourceField+"="+field.TargetField)
		}
		lines = append(lines, related.FromInstance+" "+related.Direction+" "+related.Target.Schema+"."+related.Target.Relation+" "+related.Cardinality+" "+strings.Join(fields, ","))
	}
	return lines
}

func columnOf(doc describedDoc, name string) queryresults.Column {
	for _, column := range doc.Columns {
		if column.Name == name {
			return column
		}
	}
	return queryresults.Column{}
}

const invoiceCustomerDTQL = `from:
  name: Invoice
  joins:
    - type: inner
      from: {name: Customer}
      on:
        - left: {field: CustomerId, source: Invoice}
          op: '=='
          right: {field: CustomerId, source: Customer}
columns:
  - {field: InvoiceId, source: Invoice}
  - {field: Total, source: Invoice}
  - {field: FirstName, source: Customer}
  - {aggregate: {function: count, args: [{star: true}]}, as: n}
groupBy:
  - {field: InvoiceId, source: Invoice}
  - {field: Total, source: Invoice}
  - {field: FirstName, source: Customer}
`

func TestQueryRunResultMetaFromInvoice(t *testing.T) {
	root := resultsHome(t)
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Invoice", "--no-policies", "--result-id", "f1")
	require.Equal(t, 0, code, stderr)
	doc, _ := readDescribed(t, root, "f1")
	require.Len(t, doc.Participants, 1)
	assert.Equal(t, participantDoc{Instance: "root", Schema: "main", Relation: "Invoice", Kind: "table", Role: "from"}, doc.Participants[0])
	assert.Equal(t, []string{
		"root outgoing main.Customer many-to-one CustomerId=CustomerId",
		"root incoming main.InvoiceLine one-to-many InvoiceId=InvoiceId",
	}, relatedLines(doc))
	assert.Empty(t, doc.Unavailable)
	id := columnOf(doc, "InvoiceId")
	assert.Equal(t, "integer", id.Type)
	assert.Equal(t, "int", *id.DeclaredType)
	assert.Equal(t, 1, *id.PK)
	assert.False(t, *id.Nullable)
	assert.Equal(t, queryresults.ColumnSource{Instance: "root", Schema: "main", Relation: "Invoice", Column: "InvoiceId"}, *id.Source)
	customer := columnOf(doc, "CustomerId")
	assert.Equal(t, []queryresults.ColumnFK{{RefSchema: "main", RefRelation: "Customer", RefColumn: "CustomerId"}}, customer.ForeignKeys)
	total := columnOf(doc, "Total")
	assert.Equal(t, "decimal", total.Type)
	assert.Equal(t, "decimal(10,2)", *total.DeclaredType)
	key := columnOf(doc, "$key")
	assert.Nil(t, key.Source)
	assert.Nil(t, key.DeclaredType)
}

func TestQueryRunResultMetaForAJoin(t *testing.T) {
	root := resultsHome(t)
	_, stderr, code := runQuery(t, invoiceCustomerDTQL, "--db", "sqlite://"+chinookFixture(t), "-f", "-", "--no-policies", "--result-id", "t4")
	require.Equal(t, 0, code, stderr)
	doc, _ := readDescribed(t, root, "t4")
	require.Len(t, doc.Participants, 2)
	assert.Equal(t, "from", doc.Participants[0].Role)
	assert.Equal(t, participantDoc{Instance: "root/0", Schema: "main", Relation: "Customer", Kind: "table", Role: "join",
		Join: &queryresults.ParticipantJoin{Type: "inner", On: []queryresults.JoinPair{{Left: "Invoice.CustomerId", Right: "Customer.CustomerId"}}}}, doc.Participants[1])
	// the join the query already holds is not offered again, in either direction
	assert.Equal(t, []string{
		"root incoming main.InvoiceLine one-to-many InvoiceId=InvoiceId",
		"root/0 outgoing main.Employee many-to-one SupportRepId=EmployeeId",
	}, relatedLines(doc))
	assert.Equal(t, "Customer", columnOf(doc, "FirstName").Source.Relation)
	computed := columnOf(doc, "n")
	assert.Nil(t, computed.Source, "a computed column has no source")
	assert.Nil(t, computed.DeclaredType)
}

// writeMarkedDB is a SQLite file under a folder named secret-dir whose rows
// hold unique markers, with a view and a column of several kinds.
func writeMarkedDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret-dir", "marked.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE Owner (id INTEGER PRIMARY KEY, name TEXT);
CREATE TABLE Pet (id INTEGER PRIMARY KEY, owner_id INTEGER REFERENCES Owner(id), name TEXT, tag INTEGER);
CREATE VIEW PetNames AS SELECT name FROM Pet;
INSERT INTO Owner VALUES (1, 'MARKER-OWNER-41');
INSERT INTO Pet VALUES (1, 1, 'MARKER-PET-42', 7), (2, 1, 'MARKER-PET-43', 'MARKER-TAG-44');`)
	require.NoError(t, err)
	return path
}

func TestQueryRunResultMetaHoldsNoValuesOrPaths(t *testing.T) {
	root := resultsHome(t)
	path := writeMarkedDB(t)
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+path, "--from", "Pet", "--no-policies", "--result-id", "m1")
	require.Equal(t, 0, code, stderr)
	doc, text := readDescribed(t, root, "m1")
	for _, forbidden := range []string{"MARKER", "secret-dir", "marked.db", os.Getenv("HOME")} {
		assert.NotContains(t, text, forbidden)
	}
	assert.Nil(t, readMetaRef(t, text))
	// a column whose declared type does not fit its values is a string column
	tag := columnOf(doc, "tag")
	assert.Equal(t, "string", tag.Type)
	assert.Equal(t, []string{"integer", "string"}, tag.ObservedTypes)
	assert.Equal(t, "int", *tag.DeclaredType)
	assert.Equal(t, []string{"root outgoing main.Owner many-to-one owner_id=id"}, relatedLines(doc))

	// a view participant: its kind is known, its columns are not described
	_, stderr, code = runQuery(t, "", "--db", "sqlite://"+path, "--from", "PetNames", "--no-policies", "--result-id", "m2")
	require.Equal(t, 0, code, stderr)
	doc, _ = readDescribed(t, root, "m2")
	assert.Equal(t, "view", doc.Participants[0].Kind)
	assert.Equal(t, "schema-unreadable", doc.Unavailable["declaredTypes"])
}

func readMetaRef(t *testing.T, text string) *string {
	t.Helper()
	var doc struct {
		DB struct {
			Ref *string `json:"ref"`
		} `json:"db"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &doc))
	return doc.DB.Ref
}

func TestQueryRunResultMetaOfAnHTTPSource(t *testing.T) {
	root := resultsHome(t)
	useInsecureLoopbackBackend(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Gadget","color":"blue"}`))
	}))
	defer up.Close()
	project := writeHTTPSourceProject(t, up.URL+"/widgets?name={name}")
	_, stderr, code := runQuery(t, widgetsQuery, "--db", "http://"+project, "-f", "-", "--no-policies", "--var", "name=Gadget", "--result-id", "h1")
	require.Equal(t, 0, code, stderr)
	doc, _ := readDescribed(t, root, "h1")
	assert.Equal(t, "widgets", doc.Participants[0].Relation)
	assert.Empty(t, doc.Related)
	assert.Equal(t, "no-relationship-metadata", doc.Unavailable["related"])
}

func TestQueryRunSavedResultMeta(t *testing.T) {
	db := covCSQLiteFile(t, "main.sqlite",
		`CREATE TABLE Owner(id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE Item(id INTEGER PRIMARY KEY, name TEXT, owner_id INTEGER REFERENCES Owner(id))`,
		`INSERT INTO Owner VALUES (1,'o')`,
		`INSERT INTO Item VALUES (1,'a',1),(2,'b',1)`)
	other := covCSQLiteFile(t, "other.sqlite", `CREATE TABLE Owner(id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO Owner VALUES (1,'o')`)
	federated := "from:\n  database: main\n  name: Item\n  alias: i\n  joins:\n    - from: {database: other, name: Owner, alias: o}\n      on: [{left: {field: owner_id, source: i}, op: '==', right: {field: id, source: o}}]\ncolumns:\n  - {field: name, source: i}\n  - {field: name, source: o, as: owner}\n"
	dir := covCWriteProject(t, covCMerge(
		covCEnvFiles(map[string]string{"main": db, "other": other}),
		covCQueryFiles("f", "dtqlq", "DTQL", "dtql", "from: {name: Item}\n", `,"targets":[{"catalog":"main"}]`),
		covCQueryFiles("f", "sqlq", "SQL", "sql", "SELECT id, name FROM Item", `,"targets":[{"catalog":"main"}]`),
		covCQueryFiles("f", "fed", "DTQL", "dtql", federated, ""),
	))
	root := resultsHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".datatug.yaml"), []byte("projects:\n  - id: covc\n    path: "+dir+"\n"), 0o600))

	_, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/dtqlq", "--env", "local", "--result-id", "d1")
	require.Equal(t, 0, code, stderr)
	doc, _ := readDescribed(t, root, "d1")
	assert.Equal(t, "Item", doc.Participants[0].Relation)
	assert.Equal(t, []string{"root outgoing main.Owner many-to-one owner_id=id"}, relatedLines(doc))
	assert.Equal(t, "int", *columnOf(doc, "id").DeclaredType)

	_, stderr, code = runQuery(t, "", "--project", dir, "--query", "f/sqlq", "--env", "local", "--result-id", "d2")
	require.Equal(t, 0, code, stderr)
	doc, _ = readDescribed(t, root, "d2")
	assert.Empty(t, doc.Participants)
	assert.Empty(t, doc.Related)
	assert.Equal(t, map[string]string{"participants": "native-sql", "related": "native-sql", "declaredTypes": "native-sql"}, doc.Unavailable)
	assert.NotEmpty(t, columnOf(doc, "name").Type)

	_, stderr, code = runQuery(t, "", "--project", dir, "--query", "f/fed", "--env", "local", "--result-id", "d3")
	require.Equal(t, 0, code, stderr)
	doc, _ = readDescribed(t, root, "d3")
	require.Len(t, doc.Participants, 2)
	assert.Equal(t, "datatug://covc/main?env=local", *doc.Participants[0].DB)
	assert.Equal(t, "datatug://covc/other?env=local", *doc.Participants[1].DB)
	// the join already expresses Item's key to Owner, so it is not offered again
	assert.Empty(t, doc.Related)
	assert.Equal(t, "no-relationship-metadata", doc.Unavailable["related"], "the other database declares no keys")
	assert.Equal(t, "Owner", columnOf(doc, "owner").Source.Relation)
}

func TestSavedDTQLSources(t *testing.T) {
	dir := covCWriteProject(t, covCEnvFiles(map[string]string{"main": filepath.Join(t.TempDir(), "absent.sqlite")}))
	tempDatatugHome(t)
	sources := savedDTQLSources(context.Background(), covCFileStore(dir), dir, "local", &datatug.QueryDef{ID: "q", Targets: []datatug.QueryDefTarget{{Catalog: "nope"}}})
	_, _, _, err := sources.open(context.Background(), "")
	assert.Error(t, err, "the query's own database cannot be resolved")
	origParse := chatParseSource
	chatParseSource = func(string) (dbcopy.BackendRef, error) { return dbcopy.BackendRef{}, errors.New("unparsable") }
	_, _, _, err = sources.open(context.Background(), "main")
	assert.ErrorContains(t, err, "unparsable")
	chatParseSource = origParse
	_, _, _, err = sources.open(context.Background(), "nope")
	assert.Error(t, err, "a database the environment does not list")
	assert.Nil(t, sources.ref("main"), "an unregistered project has no reference")
	// a source that cannot be opened
	orig := openBackend
	t.Cleanup(func() { openBackend = orig })
	boom := errors.New("boom")
	openBackend = func(context.Context, dbcopy.BackendRef) (dal.DB, error) { return nil, boom }
	_, driver, _, err := sources.open(context.Background(), "main")
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, "sqlite", driver)
}

func TestDescribeHelpers(t *testing.T) {
	assert.Equal(t, "no-relationship-metadata", snapshotReason(&dbschema.NotSupportedError{Op: "x"}))
	assert.Equal(t, "too-many", snapshotReason(joinmeta.ErrTooManyCollections))
	assert.Equal(t, "schema-unreadable", snapshotReason(errors.New("x")))
	assert.Equal(t, "public", schemaOr("", "postgres"))
	assert.Equal(t, "s", schemaOr("s", "postgres"))
	length, precision := 40, &dbschema.Precision{Total: 10, Scale: 2}
	assert.Equal(t, "string(40)", declaredTypeOf(dbschema.FieldDef{Type: dbschema.String, Length: &length}))
	assert.Equal(t, "decimal(10,2)", declaredTypeOf(dbschema.FieldDef{Type: dbschema.Decimal, Precision: precision}))
	for typ, want := range map[dbschema.Type]string{dbschema.Bool: "boolean", dbschema.Int: "integer", dbschema.Float: "number", dbschema.String: "string", dbschema.Time: "datetime", dbschema.Decimal: "decimal", dbschema.Bytes: ""} {
		assert.Equal(t, want, contractTypeOf(typ), typ.String())
	}
	native := describeNativeSQL(map[string]string{"n": "integer", "x": "weird"})
	assert.Equal(t, map[string]string{"n": "integer"}, native.declared)
	assert.Empty(t, native.unavailable.DeclaredTypes)

	failed := describeDTQL(context.Background(), ":", nil, dtqlSources{})
	assert.Equal(t, "schema-unreadable", failed.unavailable.Participants)

	// a database that cannot be opened, and columns that are not plain fields
	open := func(context.Context, string) (dal.DB, string, func(), error) {
		return nil, "sqlite", nil, errors.New("closed")
	}
	description := describeDTQL(context.Background(), "from: {name: T, alias: t}\ncolumns:\n  - {field: a, source: t}\n  - {wildcard: {source: t, exclude: [z]}}\n  - {field: b, source: nope}\n  - {value: 1, as: one}\n", []string{"a", "c", "b", "one"}, dtqlSources{open: open, ref: func(string) *string { return nil }})
	assert.Equal(t, "schema-unreadable", description.unavailable.Related)
	assert.Equal(t, "schema-unreadable", description.unavailable.DeclaredTypes)
	assert.Empty(t, description.facts)
}

func TestDescribeDTQLWithOneSnapshot(t *testing.T) {
	ctx := context.Background()
	db := chinookDB(t)
	sources := dtqlSources{open: func(context.Context, string) (dal.DB, string, func(), error) { return db, "sqlite", func() {}, nil }, ref: func(string) *string { return nil }}
	// a wildcard of one source, a column that is not in it, and a column of no source
	description := describeDTQL(ctx, "from: {name: Genre, alias: g}\ncolumns:\n  - {wildcard: {source: g, exclude: [Name]}}\n", []string{"GenreId", "Nope"}, sources)
	assert.Equal(t, "Genre", description.facts["GenreId"].Source.Relation)
	assert.NotContains(t, description.facts, "Nope")
	// a direct column that is not a field of its relation
	description = describeDTQL(ctx, "from: {name: Genre}\ncolumns:\n  - {field: Missing}\n", []string{"Missing"}, sources)
	assert.Empty(t, description.facts)
	// a related list whose discovery cannot run
	states := map[string]*snapshotState{"": {snapshot: joinmeta.ForeignKeySnapshot{Keys: []joinmeta.ForeignKey{{ConstraintID: "x", FromRelation: "A", FromFields: []string{"a"}, ToRelation: "B", ToFields: []string{"b"}}}}}}
	_, reason := relatedOf(":", states)
	assert.Equal(t, "schema-unreadable", reason)
}

func chinookDB(t *testing.T) dal.DB {
	t.Helper()
	backend, err := dbcopy.Parse("sqlite://" + chinookFixture(t))
	require.NoError(t, err)
	db, err := backend.OpenProtected(context.Background())
	require.NoError(t, err)
	return db
}

func TestRelatedOfOrdersAndKeepsEachDatabasesOwn(t *testing.T) {
	text := "from:\n  database: a\n  name: A\n  alias: x\n  joins:\n    - from: {database: b, name: B, alias: y}\n      on: [{left: {field: id, source: x}, op: '==', right: {field: aid, source: y}}]\n"
	key := func(id, from, to, field string) joinmeta.ForeignKey {
		return joinmeta.ForeignKey{ConstraintID: id, FromRelation: from, FromFields: []string{field}, ToRelation: to, ToFields: []string{"id"}}
	}
	states := map[string]*snapshotState{
		"a": {driver: "sqlite", snapshot: joinmeta.ForeignKeySnapshot{Keys: []joinmeta.ForeignKey{key("c2", "A", "Y", "y_id"), key("c1", "A", "X", "x_id"), key("c0", "A", "X", "x2_id")}}},
		"b": {driver: "sqlite", snapshot: joinmeta.ForeignKeySnapshot{Keys: []joinmeta.ForeignKey{key("c9", "A", "Z", "z_id")}}},
	}
	related, reason := relatedOf(text, states)
	assert.Empty(t, reason)
	var got []string
	for _, item := range related {
		got = append(got, item.Target.Relation+"/"+item.ConstraintID)
	}
	assert.Equal(t, []string{"X/c0", "X/c1", "Y/c2"}, got, "a key of database b never describes A of database a")
}

func TestJoinsOf(t *testing.T) {
	from := dal.From(dal.NewRootCollectionRef("A", "a")).Join(dal.NewJoinedSource(dal.NewRootCollectionRef("B", "b"), dal.JoinLeft,
		dal.NewComparison(dal.NewFieldRef("", "id"), dal.Equal, dal.NewFieldRef("b", "aid")),
		dal.NewComparison(dal.NewFieldRef("a", "n"), dal.GreaterThen, dal.NewFieldRef("b", "n")),
		dal.NewComparison(dal.NewFieldRef("a", "k"), dal.Equal, dal.NewConstant(1)),
	))
	joins := joinsOf(from)
	assert.Equal(t, &queryresults.ParticipantJoin{Type: "left", On: []queryresults.JoinPair{{Left: "id", Right: "b.aid"}}}, joins["root/0"])
}

func TestDescribeColumnsOfAView(t *testing.T) {
	path := writeMarkedDB(t)
	backend, err := dbcopy.Parse("sqlite://" + path)
	require.NoError(t, err)
	db, err := backend.OpenProtected(context.Background())
	require.NoError(t, err)
	sources := dtqlSources{open: func(context.Context, string) (dal.DB, string, func(), error) { return db, "sqlite", func() {}, nil }}
	description := describeDTQL(context.Background(), "from: {name: PetNames, alias: p}\ncolumns:\n  - {wildcard: {source: p, exclude: [z]}}\n", []string{"name"}, sources)
	assert.Equal(t, "schema-unreadable", description.unavailable.DeclaredTypes)
	assert.Empty(t, description.facts)
	description = describeDTQL(context.Background(), "from: {name: PetNames, alias: p}\ncolumns:\n  - {field: name, source: p}\n", []string{"name"}, sources)
	assert.Equal(t, "schema-unreadable", description.unavailable.DeclaredTypes)
}

func TestLoadChatJoinApplicationWithoutSchemaMetadata(t *testing.T) {
	project := writeHTTPSourceProject(t, "https://example.test/widgets?name={name}")
	app, closeFn, err := loadChatJoinApplication(context.Background(), "http://"+project, nil, false)
	assert.Error(t, err)
	assert.Nil(t, app)
	assert.Nil(t, closeFn)
}
