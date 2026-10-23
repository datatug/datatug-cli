package commands

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/joinmeta"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeOrderDB is a SQLite file whose tables and view declare their columns in
// an order that is not alphabetical.
func writeOrderDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "order.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE Pet (id INTEGER PRIMARY KEY, zone TEXT, name TEXT, age INTEGER, breed TEXT);
CREATE TABLE LineItem (orderId INTEGER, lineNo INTEGER, sku TEXT, qty INTEGER, PRIMARY KEY (orderId, lineNo));
CREATE VIEW PetCard AS SELECT zone, id, breed, name FROM Pet;
INSERT INTO Pet VALUES (1,'z','n',3,'b');
INSERT INTO LineItem VALUES (1,2,'s',4);`)
	require.NoError(t, err)
	return path
}

func storedNames(recordset apicontract.Recordset) []string {
	names := make([]string, len(recordset.Columns))
	for i, column := range recordset.Columns {
		names[i] = column.Name
	}
	return names
}

func TestQueryRunResultColumnsFollowTheTableOrder(t *testing.T) {
	root := resultsHome(t)
	path := writeOrderDB(t)
	for _, c := range []struct {
		name, from string
		want       []string
	}{
		{"table", "Pet", []string{"$key", "id", "zone", "name", "age", "breed"}},
		{"composite primary key", "LineItem", []string{"$key", "orderId", "lineNo", "sku", "qty"}},
		{"view", "PetCard", []string{"$key", "zone", "id", "breed", "name"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := "o-" + strings.ToLower(c.from)
			_, stderr, code := runQuery(t, "", "--db", "sqlite://"+path, "--from", c.from, "--no-policies", "--result-id", id)
			require.Equal(t, 0, code, stderr)
			meta, recordset, _ := readResult(t, root, id)
			assert.Equal(t, c.want, storedNames(recordset))
			var metaNames []string
			for _, column := range meta.Columns {
				metaNames = append(metaNames, column.Name)
			}
			assert.Equal(t, c.want, metaNames, "meta.json describes the columns in the same order")
			for i, column := range meta.Columns {
				assert.Equal(t, i == 0, column.Internal, column.Name)
			}
			assert.Equal(t, "__dalgo_record_id", recordset.Rows[0][0].Str, "$key stays in the data for readers that predate the marker")
		})
	}
}

func TestQueryRunResultOfChinookInvoiceStartsAtItsKey(t *testing.T) {
	root := resultsHome(t)
	_, stderr, code := runQuery(t, "", "--db", "sqlite://"+chinookFixture(t), "--from", "Invoice", "--no-policies", "--result-id", "inv")
	require.Equal(t, 0, code, stderr)
	_, recordset, _ := readResult(t, root, "inv")
	names := storedNames(recordset)
	assert.Equal(t, []string{"$key", "InvoiceId", "CustomerId", "InvoiceDate"}, names[:4])
	assert.Equal(t, "Total", names[len(names)-1])
}

// stdout keeps its documented order: the sorted union, or the request order.
func TestQueryRunStdoutOrderIsUnchanged(t *testing.T) {
	path := writeOrderDB(t)
	stdout, stderr, code := runQuery(t, "", "--db", "sqlite://"+path, "--from", "Pet", "--no-policies", "--format", "csv")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "$key,age,breed,id,name,zone", strings.SplitN(stdout, "\n", 2)[0])
}

func TestQueryRunResultColumnsOfTugQL(t *testing.T) {
	root := resultsHome(t)
	path := writeOrderDB(t)
	for _, c := range []struct {
		name, document string
		want           []string
	}{
		{"an explicit select list keeps its order", "from: {name: Pet}\ncolumns:\n  - {field: name}\n  - {field: breed}\n  - {field: id}\n", []string{"$key", "name", "breed", "id"}},
		{"no select list is the table order", "from: {name: Pet}\n", []string{"$key", "id", "zone", "name", "age", "breed"}},
		{"an alias keeps its place", "from: {name: Pet}\ncolumns:\n  - {field: name, as: label}\n  - {field: id}\n", []string{"$key", "label", "id"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := fmt.Sprintf("d%d", len(c.name))
			_, stderr, code := runQuery(t, c.document, "--db", "sqlite://"+path, "-f", "-", "--no-policies", "--result-id", id)
			require.Equal(t, 0, code, stderr)
			_, recordset, _ := readResult(t, root, id)
			assert.Equal(t, c.want, storedNames(recordset))
		})
	}
}

// project is a registered project whose catalog "main" runs on db; scanned, when
// not empty, is the columns file the project holds for the table Item.
func orderProject(t *testing.T, db, scanned string) string {
	t.Helper()
	files := covCMerge(
		covCEnvFiles(map[string]string{"main": db}),
		covCQueryFiles("f", "tugqlq", "TugQL", "tugql", "from: {name: Item}\n", `,"targets":[{"catalog":"main"}]`),
		covCQueryFiles("f", "sqlq", "SQL", "sql", "SELECT name, id FROM Item", `,"targets":[{"catalog":"main"}]`),
		covCQueryFiles("f", "viewq", "TugQL", "tugql", "from: {name: ItemNames}\n", `,"targets":[{"catalog":"main"}]`),
	)
	if scanned != "" {
		files["environments/local/catalogs/main/main.db.json"] = fmt.Sprintf(`{"driver":"sqlite3","path":%q,"dbModel":"shop"}`, db)
		files["dbmodels/shop/main/tables/Item/main.Item.columns.json"] = scanned
	}
	dir := covCWriteProject(t, files)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("HOME"), ".datatug.yaml"), []byte("projects:\n  - id: covc\n    path: "+dir+"\n"), 0o600))
	return dir
}

func orderItemDB(t *testing.T) string {
	return covCSQLiteFile(t, "main.sqlite",
		`CREATE TABLE Item(id INTEGER PRIMARY KEY, name TEXT, owner_id INTEGER)`,
		`CREATE VIEW ItemNames AS SELECT name, id FROM Item`,
		`INSERT INTO Item VALUES (1,'a',1),(2,'b',1)`)
}

func TestQueryRunSavedResultColumnOrder(t *testing.T) {
	root := resultsHome(t)
	dir := orderProject(t, orderItemDB(t), "")
	for id, query := range map[string]string{"s1": "f/tugqlq", "s2": "f/sqlq", "s3": "f/viewq"} {
		_, stderr, code := runQuery(t, "", "--project", dir, "--query", query, "--env", "local", "--result-id", id)
		require.Equal(t, 0, code, stderr)
	}
	_, recordset, _ := readResult(t, root, "s1")
	assert.Equal(t, []string{"$key", "id", "name", "owner_id"}, storedNames(recordset), "saved TugQL: the table order")
	_, recordset, _ = readResult(t, root, "s2")
	assert.Equal(t, []string{"$key", "name", "id"}, storedNames(recordset), "saved native SQL: the SELECT list")
	_, recordset, _ = readResult(t, root, "s3")
	assert.Equal(t, []string{"$key", "name", "id"}, storedNames(recordset), "saved TugQL over a view")
	// the stdout rows keep the sorted union
	stdout, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/sqlq", "--env", "local", "--format", "csv")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "$key,id,name", strings.SplitN(stdout, "\n", 2)[0])
}

// A project that scanned the database holds the order its scan recorded; it is
// preferred to asking the database again, for a saved query and for an ad-hoc
// run against the project's datatug:// reference.
func TestQueryRunResultColumnOrderOfAScannedProject(t *testing.T) {
	root := resultsHome(t)
	scanned := `{"columns":[{"name":"owner_id","ordinalPosition":1},{"name":"name","ordinalPosition":2},{"name":"id","ordinalPosition":3}]}`
	dir := orderProject(t, orderItemDB(t), scanned)

	_, stderr, code := runQuery(t, "", "--project", dir, "--query", "f/tugqlq", "--env", "local", "--result-id", "sc1")
	require.Equal(t, 0, code, stderr)
	_, recordset, _ := readResult(t, root, "sc1")
	assert.Equal(t, []string{"$key", "owner_id", "name", "id"}, storedNames(recordset))

	_, stderr, code = runQuery(t, "", "--db", "datatug://covc/main?env=local", "--from", "Item", "--no-policies", "--result-id", "sc2")
	require.Equal(t, 0, code, stderr)
	_, recordset, _ = readResult(t, root, "sc2")
	assert.Equal(t, []string{"$key", "owner_id", "name", "id"}, storedNames(recordset))
}

func TestScannedColumnsAndProjectDirOfRef(t *testing.T) {
	assert.Equal(t, "", projectDirOfRef("datatug://%zz/db"), "not a reference")
	tempDatatugHome(t)
	assert.Equal(t, "", projectDirOfRef("datatug://nobody/db?env=local"), "not a registered project")
	assert.Equal(t, "", projectDirOfRef("datatug:///db?env=local"), "no project named and none to default to")
	dir := orderProject(t, orderItemDB(t), `{"columns":[{"name":"b","ordinalPosition":2},{"name":"a","ordinalPosition":1}]}`)
	require.Equal(t, dir, projectDirOfRef("datatug://covc/main?env=local"))

	scanned := scannedColumns(dir, "local", "main")
	names, ok := scanned("", "", "Item")
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, names)
	for _, missing := range []func(string, string, string) ([]string, bool){
		scannedColumns("", "local", "main"), scannedColumns(dir, "", "main"), scannedColumns(dir, "local", ""),
	} {
		_, ok := missing("", "", "Item")
		assert.False(t, ok)
	}
}

func TestSourceOrderOfDescribedQueries(t *testing.T) {
	ctx := context.Background()
	db := chinookDB(t)
	sources := tugqlSources{open: func(context.Context, string) (dal.DB, string, func(), error) { return db, "sqlite", func() {}, nil }}
	// no select list: the root's columns, then each joined table's
	description := describeTugQL(ctx, invoiceCustomerTugQL[:strings.Index(invoiceCustomerTugQL, "columns:")], nil, sources)
	require.NotEmpty(t, description.order)
	assert.Equal(t, "InvoiceId", description.order[0])
	assert.Contains(t, description.order, "FirstName")
	assert.Less(t, indexOf(description.order, "Total"), indexOf(description.order, "FirstName"), "the root's columns come first")

	// a wildcard of one source expands that source only
	description = describeTugQL(ctx, "from: {name: Genre, alias: g}\ncolumns:\n  - {value: 1, as: one}\n  - {wildcard: {source: g, exclude: [Nope]}}\n", nil, sources)
	assert.Equal(t, []string{"one", "GenreId", "Name"}, description.order)

	// the project's scan wins over the database, and a relation it lacks falls back to the database
	sources.scanned = func(_, _, relation string) ([]string, bool) {
		return []string{"Name", "GenreId"}, relation == "Genre"
	}
	description = describeTugQL(ctx, "from: {name: Genre}\n", nil, sources)
	assert.Equal(t, []string{"Name", "GenreId"}, description.order)
	description = describeTugQL(ctx, "from: {name: Artist}\n", nil, sources)
	assert.Equal(t, []string{"ArtistId", "Name"}, description.order)

	// a source that cannot be read has no order
	unreadable := tugqlSources{open: func(context.Context, string) (dal.DB, string, func(), error) {
		return nil, "sqlite", nil, assert.AnError
	}}
	assert.Empty(t, describeTugQL(ctx, "from: {name: Genre}\n", nil, unreadable).order)
}

func indexOf(names []string, name string) int {
	for i, candidate := range names {
		if candidate == name {
			return i
		}
	}
	return -1
}

func TestAddViewColumnsIgnoresWhatTheSourceDoesNotReport(t *testing.T) {
	ctx := context.Background()
	db := chinookDB(t)
	instances := []joinmeta.RelationInstance{{ID: "root", Relation: "Nope"}, {ID: "root/0", Relation: "Genre", Database: "other"}}
	snapshot := joinmeta.ForeignKeySnapshot{Columns: map[string][]string{}}
	addViewColumns(ctx, db, &snapshot, "", instances)
	assert.Empty(t, snapshot.Columns, "a relation that is not a reported view, and one of another database, add nothing")
	addViewColumns(ctx, db, &joinmeta.ForeignKeySnapshot{}, "", instances) // no column map: nothing to add to
	addViewColumns(ctx, struct{ dal.DB }{}, &snapshot, "", instances)      // a source with no view reader
}

func TestSavedTugQLSourcesScannedOrderNeedsTheQuerysDatabase(t *testing.T) {
	dir := orderProject(t, orderItemDB(t), `{"columns":[{"name":"b","ordinalPosition":2},{"name":"a","ordinalPosition":1}]}`)
	store := covCFileStore(dir)
	ctx := context.Background()
	good := savedTugQLSources(ctx, store, dir, "local", &datatug.QueryDef{ID: "q", Targets: []datatug.QueryDefTarget{{Catalog: "main"}}})
	names, ok := good.scanned("", "", "Item")
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, names)
	names, ok = good.scanned("main", "", "Item")
	assert.True(t, ok, "a federated query names its database")
	assert.Equal(t, []string{"a", "b"}, names)
	unresolved := savedTugQLSources(ctx, store, dir, "nowhere", &datatug.QueryDef{ID: "q"})
	_, ok = unresolved.scanned("", "", "Item")
	assert.False(t, ok, "the query's database cannot be resolved")
}
