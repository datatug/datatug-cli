package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the PostgreSQL twin of the journey in scan_journey_test.go: the real
// scan command, the real scanner and the readers behind chat, saved queries, serve and
// the web app, over a fake schema reader behind the one seam through which a PostgreSQL
// scan opens its source. No test of the scan dials a server: the opener that stands in
// for the real one fails the test when it is asked for anything it was not set up for,
// and the real server is the CI test of the scan against PostgreSQL.

const (
	journeyPgSecret = "Zk39x-pg-p4ss"
	journeyPgUser   = "alice"
	journeyPgHost   = "db.example.com"
	journeyPgPort   = "54329"
	journeyPgVar    = "DATATUG_JOURNEY_PG_URL"
)

// journeyPgURL is the connection URL of the fake server: every part a project must
// never hold is in it.
func journeyPgURL(database string) string {
	return "postgres://" + journeyPgUser + ":" + journeyPgSecret + "@" + journeyPgHost + ":" + journeyPgPort + "/" + database + "?sslmode=require"
}

// pgRelation is a table or a view of the fake server.
type pgRelation struct {
	schema, name string
	view         bool
	fields       []dbschema.FieldDef
	primaryKey   []dal.FieldName
	foreignKeys  []dbschema.ForeignKeyDef
	indexes      []dbschema.IndexDef
}

func pgField(name string, kind dbschema.Type, nullable bool) dbschema.FieldDef {
	return dbschema.FieldDef{Name: dal.FieldName(name), Type: kind, Nullable: nullable}
}

// storedDefaults is the default of each column of the columns file of a table or view of the
// model shop that has one, by the name of the column.
func storedDefaults(t *testing.T, projectDir, schema, folder, name string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, "dbmodels", "shop", schema, folder, name, schema+"."+name+".columns.json"))
	require.NoError(t, err, name)
	var file struct {
		Columns []struct {
			Name    string  `json:"name"`
			Default *string `json:"default"`
		} `json:"columns"`
	}
	require.NoError(t, json.Unmarshal(data, &file), string(data))
	defaults := map[string]string{}
	for _, column := range file.Columns {
		if column.Default != nil {
			defaults[column.Name] = *column.Default
		}
	}
	return defaults
}

// pgFieldDefault is field with the default the reader reports: the text of the SQL expression.
func pgFieldDefault(field dbschema.FieldDef, expression string) dbschema.FieldDef {
	field.Default = dbschema.DefaultLiteral{Value: expression}
	return field
}

// fakePgDatabase is the schema reader a PostgreSQL scan reads through. It lists the
// relations of more than one schema, with a view, mixed-case names and a composite key,
// and it tells a view from a table, as the reader of a real server could.
type fakePgDatabase struct {
	dal.DB
	mu        sync.Mutex
	relations []pgRelation
	closed    int
}

var _ dbcopy.SchemaScanDB = (*fakePgDatabase)(nil)

func (f *fakePgDatabase) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakePgDatabase) find(ref *dal.CollectionRef) (pgRelation, bool) {
	for _, relation := range f.relations {
		if relation.name == ref.Name() && (ref.Schema() == "" || relation.schema == ref.Schema()) {
			return relation, true
		}
	}
	return pgRelation{}, false
}

func (f *fakePgDatabase) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	refs := make([]dal.CollectionRef, len(f.relations))
	for i, relation := range f.relations {
		refs[i] = dal.NewQualifiedRootCollectionRef(relation.schema, relation.name, "")
	}
	return refs, nil
}

func (f *fakePgDatabase) ListViews(context.Context) ([]dal.CollectionRef, error) {
	var refs []dal.CollectionRef
	for _, relation := range f.relations {
		if relation.view {
			refs = append(refs, dal.NewQualifiedRootCollectionRef(relation.schema, relation.name, ""))
		}
	}
	return refs, nil
}

// ListSchemas, ListSchemaCollections and ListSchemaViews are the reader's own methods that the
// scan looks for (dalgoschema.SchemaLister): the schemas of the server in name order, and the
// relations and the views of one, each by a reference that names its schema.
func (f *fakePgDatabase) ListSchemas(context.Context) ([]string, error) {
	var schemas []string
	for _, relation := range f.relations {
		if !slices.Contains(schemas, relation.schema) {
			schemas = append(schemas, relation.schema)
		}
	}
	slices.Sort(schemas)
	return schemas, nil
}

func (f *fakePgDatabase) listSchema(schema string, viewsOnly bool) []dal.CollectionRef {
	var refs []dal.CollectionRef
	for _, relation := range f.relations {
		if relation.schema == schema && (relation.view || !viewsOnly) {
			refs = append(refs, dal.NewQualifiedRootCollectionRef(relation.schema, relation.name, ""))
		}
	}
	return refs
}

func (f *fakePgDatabase) ListSchemaCollections(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	return f.listSchema(schema, false), nil
}

func (f *fakePgDatabase) ListSchemaViews(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	return f.listSchema(schema, true), nil
}

var _ dalgoschema.SchemaLister = (*fakePgDatabase)(nil)

func (f *fakePgDatabase) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	relation, ok := f.find(ref)
	if !ok {
		return nil, fmt.Errorf("relation %q does not exist", ref.Name())
	}
	return &dbschema.CollectionDef{Name: relation.name, Fields: relation.fields, PrimaryKey: relation.primaryKey, ForeignKeys: relation.foreignKeys}, nil
}

func (f *fakePgDatabase) ListIndexes(_ context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	relation, _ := f.find(ref)
	return relation.indexes, nil
}

func (*fakePgDatabase) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints"}
}

func (*fakePgDatabase) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers"}
}

// newJourneyPgDatabase is the PostgreSQL twin of writeJourneyDB: a mixed-case table, a
// table whose composite primary key is in a different order from its columns, a
// foreign key, an index and a view in schema public, and a table and a view of the
// same kind in a second schema.
func newJourneyPgDatabase() *fakePgDatabase {
	return &fakePgDatabase{relations: []pgRelation{
		{schema: "public", name: "Customer",
			fields:     []dbschema.FieldDef{pgField("CustomerId", dbschema.Int, false), pgField("FirstName", dbschema.String, false), pgField("LastName", dbschema.String, true)},
			primaryKey: []dal.FieldName{"CustomerId"}},
		{schema: "public", name: "order_line",
			fields: []dbschema.FieldDef{pgField("order_id", dbschema.Int, false), pgField("line_no", dbschema.Int, false), pgField("customer_id", dbschema.Int, true), pgField("sku", dbschema.String, true), pgField("qty", dbschema.Int, true)},
			// The key is (line_no, order_id): not the order of the columns.
			primaryKey:  []dal.FieldName{"line_no", "order_id"},
			foreignKeys: []dbschema.ForeignKeyDef{{Name: "fk_order_line_customer", Fields: []dal.FieldName{"customer_id"}, ReferencedCollection: "Customer", ReferencedFields: []dal.FieldName{"CustomerId"}}},
			indexes:     []dbschema.IndexDef{{Name: "order_line_qty", Collection: "order_line", Fields: []dal.FieldName{"qty"}}}},
		{schema: "public", name: "customer_names", view: true,
			fields: []dbschema.FieldDef{pgField("CustomerId", dbschema.Int, true), pgField("full_name", dbschema.String, true)}},
		{schema: "sales", name: "Invoice",
			fields:     []dbschema.FieldDef{pgField("InvoiceId", dbschema.Int, false), pgField("Total", dbschema.Decimal, true)},
			primaryKey: []dal.FieldName{"InvoiceId"}},
		{schema: "sales", name: "OpenInvoices", view: true,
			fields: []dbschema.FieldDef{pgField("InvoiceId", dbschema.Int, true)}},
	}}
}

// pgOpener is the opener a PostgreSQL scan test stands in with: it is the only way a
// scan under test opens a source, and it never dials. A scan that reaches it for a
// source it was not set up for fails the test.
type pgOpener struct {
	t       *testing.T
	mu      sync.Mutex
	sources map[string]func() dbcopy.SchemaScanDB // by "env:NAME"
	opened  []dbcopy.BackendRef
}

func (o *pgOpener) open(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
	o.t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opened = append(o.opened, ref)
	db, ok := o.sources[ref.Raw]
	if !ok {
		o.t.Errorf("a scan opened %q, and this test was not set up for it: it must never dial", ref.Raw)
		return nil, errors.New("not a source of this test")
	}
	return db(), nil
}

// serve makes the fake server db the one that the variable names, with the connection
// URL of the database in the variable.
func (o *pgOpener) serve(variable, database string, db func() dbcopy.SchemaScanDB) {
	o.t.Setenv(variable, journeyPgURL(database))
	o.sources["env:"+variable] = db
}

// usePostgres puts db behind the seam through which the scan opens PostgreSQL, and the
// connection URL of the fake server into the variable the scan names.
func usePostgres(t *testing.T, variable, database string, db func() dbcopy.SchemaScanDB) *pgOpener {
	t.Helper()
	opener := &pgOpener{t: t, sources: map[string]func() dbcopy.SchemaScanDB{}}
	opener.serve(variable, database, db)
	t.Cleanup(api.SetOpenSchemaScanForTest(opener.open))
	return opener
}

func pgScanArgs(projectDir, variable, database, env string, more ...string) []string {
	return append([]string{"-d", projectDir, "-D", "postgres", "--dsn-env", variable, "--db", database, "--env", env}, more...)
}

// connectionKeys are the keys that a project file holds only for a connection: a file of a
// project that has any of them, at any depth, holds a host, a port, a user or a password,
// however its value is written (a port is a number in JSON, and ":54329" is not in the text).
var connectionKeys = []string{"host", "port", "user", "password", "server"}

// sourceLeaksIn is what is wrong with the tree under dir: each file, or name of one, that holds a
// part of the connection URL of the fake server (the secret, the user, the host, the port, the
// scheme or the query), and each key of a decoded .json file, at any depth, that is one of
// connectionKeys. It is empty for a tree that holds no part of the connection.
func sourceLeaksIn(dir string) (leaks []string, err error) {
	parts := []string{journeyPgSecret, journeyPgUser, journeyPgHost, ":" + journeyPgPort, "sslmode", "postgres://"}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		text := path
		if !entry.IsDir() {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			text += "\n" + string(content)
			if strings.HasSuffix(path, ".json") {
				var decoded any
				if json.Unmarshal(content, &decoded) == nil {
					for _, key := range keysNamed(decoded, connectionKeys) {
						leaks = append(leaks, fmt.Sprintf("%s holds the key %q", path, key))
					}
				}
			}
		}
		for _, part := range parts {
			if strings.Contains(text, part) {
				leaks = append(leaks, fmt.Sprintf("%s holds %q", path, part))
			}
		}
		return nil
	})
	return leaks, err
}

// keysNamed is each key of the decoded JSON value, at any depth, that is one of names (in any case).
func keysNamed(value any, names []string) (found []string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, inner := range typed {
			if slices.ContainsFunc(names, func(name string) bool { return strings.EqualFold(name, key) }) {
				found = append(found, key)
			}
			found = append(found, keysNamed(inner, names)...)
		}
	case []any:
		for _, inner := range typed {
			found = append(found, keysNamed(inner, names)...)
		}
	}
	return found
}

// assertNoSourceIn fails t when any file under dir, or the name of one, holds a part of the
// connection URL of the fake server, or a .json file of the tree has a key that only a connection
// has (see connectionKeys).
func assertNoSourceIn(t *testing.T, dir string) {
	t.Helper()
	leaks, err := sourceLeaksIn(dir)
	require.NoError(t, err)
	assert.Empty(t, leaks, "no file of the project holds a part of the connection")
}

// The check of the tree finds a port written as a JSON number, which a search for ":54329" does
// not, at any depth, and finds nothing in a tree of names that only look like it.
func TestAssertNoSourceInFindsAConnectionKeyAtAnyDepth(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"dbServers":[{"driver":"postgres","catalogs":["shop"]}]}`), 0o644))
	leaks, err := sourceLeaksIn(dir)
	require.NoError(t, err)
	assert.Empty(t, leaks, "dbServers is not a server: the keys are matched whole")

	for _, body := range []string{`{"port": 54329}`, `{"a":{"b":[{"Host":"x"}]}}`, `{"user":"u"}`, `{"password":""}`, `{"server":"s"}`} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(body), 0o644))
		leaks, err = sourceLeaksIn(dir)
		require.NoError(t, err)
		assert.NotEmpty(t, leaks, body)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte(`not json, with no part of a connection`), 0o644))
	leaks, err = sourceLeaksIn(dir)
	require.NoError(t, err)
	assert.Empty(t, leaks, "a .json file that does not decode is still searched for the parts, and holds none")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.json"), []byte("the "+journeyPgHost+" host"), 0o644))
	leaks, err = sourceLeaksIn(dir)
	require.NoError(t, err)
	assert.Len(t, leaks, 1)
}

func TestScanJourneyPostgres(t *testing.T) {
	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	fake := newJourneyPgDatabase()
	opener := usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return fake })
	scan := func() (string, error) {
		return runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
	}

	// 1. I scan my PostgreSQL database into a folder. Exit 0, the folder is a project.
	stderr, err := scan()
	require.NoError(t, err)
	assert.Empty(t, stderr, "a scan with nothing to leave out says nothing on stderr")
	require.Len(t, opener.opened, 1)
	assert.Equal(t, 1, fake.closed, "the connection is released")

	// The scan writes the layout every reader reads, the descriptor, and nothing else.
	assert.Equal(t, []string{
		"README.md",
		"connections/local/shop.json",
		"datatug-project.json",
		"dbmodels/shop/public/tables/Customer/public.Customer.columns.json",
		"dbmodels/shop/public/tables/order_line/public.order_line.columns.json",
		"dbmodels/shop/public/views/customer_names/public.customer_names.columns.json",
		"dbmodels/shop/sales/tables/Invoice/sales.Invoice.columns.json",
		"dbmodels/shop/sales/views/OpenInvoices/sales.OpenInvoices.columns.json",
		"dbmodels/shop/shop.dbmodel.json",
		"environments/local/catalogs/shop/shop.db.json",
		"environments/local/local.env.json",
	}, projectFiles(t, projectDir, ""), "the files of the scan")

	// The catalog file holds the driver, the path of the descriptor and the model; the
	// environment file names the driver and the catalog id, and no host, port or user.
	catalogFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json"))
	assert.Equal(t, "postgres", catalogFile["driver"])
	assert.Equal(t, "shop", catalogFile["dbModel"])
	assert.Equal(t, "connections/local/shop.json", catalogFile["path"])
	envFile := readJSONMap(t, filepath.Join(projectDir, "environments", "local", "local.env.json"))
	assert.Equal(t, []any{map[string]any{"driver": "postgres", "catalogs": []any{"shop"}}}, envFile["dbServers"])
	descriptor := readJSONMap(t, filepath.Join(projectDir, "connections", "local", "shop.json"))
	assert.Equal(t, map[string]any{"dsnEnv": journeyPgVar}, descriptor, "the descriptor names the variable and nothing else")
	modelFile := readJSONMap(t, filepath.Join(projectDir, "dbmodels", "shop", "shop.dbmodel.json"))
	assert.NotContains(t, modelFile, "schemas")
	assertNoSourceIn(t, projectDir)

	store, id := filestore.NewSingleProjectStore(projectDir, "")
	projStore := store.GetProjectStore(id)
	project, err := projStore.LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())
	assert.Equal(t, []string{"local"}, project.Environments.IDs())
	assert.Equal(t, []string{"shop"}, project.DbModels.IDs())

	// 2. I list what it found: tables, views and columns under their exact names, and the
	// position of each primary-key column, in both schemas.
	want := map[string][]journeyColumn{
		"public.Customer (BASE TABLE)": {
			{Name: "CustomerId", PKPos: 1, DbType: "int"},
			{Name: "FirstName", DbType: "string"},
			{Name: "LastName", DbType: "string"},
		},
		"public.customer_names (VIEW)": {
			{Name: "CustomerId", DbType: "int"},
			{Name: "full_name", DbType: "string"},
		},
		"public.order_line (BASE TABLE)": {
			{Name: "order_id", PKPos: 2, DbType: "int"},
			{Name: "line_no", PKPos: 1, DbType: "int"},
			{Name: "customer_id", DbType: "int"},
			{Name: "sku", DbType: "string"},
			{Name: "qty", DbType: "int"},
		},
		"sales.Invoice (BASE TABLE)": {
			{Name: "InvoiceId", PKPos: 1, DbType: "int"},
			{Name: "Total", DbType: "decimal"},
		},
		"sales.OpenInvoices (VIEW)": {
			{Name: "InvoiceId", DbType: "int"},
		},
	}
	schema, err := api.GetCatalogSchema(projectDir, "local", "shop")
	require.NoError(t, err)
	assert.Equal(t, want, relationColumns(schema))

	// 3. I run a query, open chat, start serve: the source resolves, to the variable that
	// holds the URL, and never to the URL. (Opening a PostgreSQL source for a query is the
	// next task's.)
	sources, err := api.ListSources(ctx, projStore, projectDir, "local")
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "shop", sources[0].ID)
	assert.Equal(t, "env:"+journeyPgVar, sources[0].URL)

	// Serve lists the tables of the database from the same files, with their schemas and exact
	// names, and chat's project catalog holds the same tables and views, under the source that
	// resolved.
	listed, err := api.GetCatalogTables(projectDir, "local", "shop")
	require.NoError(t, err)
	var listedTables, listedViews []string
	for _, table := range listed.Tables {
		listedTables = append(listedTables, table.Schema+"."+table.Name)
	}
	for _, view := range listed.Views {
		listedViews = append(listedViews, view.Schema+"."+view.Name)
	}
	assert.Equal(t, []string{"public.Customer", "public.order_line", "sales.Invoice"}, listedTables)
	assert.Equal(t, []string{"public.customer_names", "sales.OpenInvoices"}, listedViews)
	chatCatalog, urls, err := buildChatProjectCatalog(ctx, projectDir, projStore, "local")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"shop": "env:" + journeyPgVar}, urls)
	objects := map[string][]string{}
	for _, object := range chatCatalog.Objects {
		assert.Empty(t, object.Issue, "the source resolved and nothing about %s is unavailable", object.Reference.ObjectID)
		objects[object.Reference.Kind+" "+object.Reference.ObjectID] = object.Columns
	}
	assert.Equal(t, map[string][]string{
		"project " + project.ID:              nil,
		"source shop":                        nil,
		"table public.Customer":              {"CustomerId", "FirstName", "LastName"},
		"table public.order_line":            {"order_id", "line_no", "customer_id", "sku", "qty"},
		"project_view public.customer_names": {"CustomerId", "full_name"},
		"table sales.Invoice":                {"InvoiceId", "Total"},
		"project_view sales.OpenInvoices":    {"InvoiceId"},
	}, objects)

	// 4. I push the folder; a colleague opens the link: the web app lists the database and
	// its tables, in both schemas.
	envs, catalogs, tables, views := webReaderTables(t, projectDir)
	assert.Equal(t, []string{"local"}, envs)
	assert.Equal(t, []string{"shop"}, catalogs)
	assert.Equal(t, []string{"public.Customer", "public.order_line", "sales.Invoice"}, tables)
	assert.Equal(t, []string{"public.customer_names", "sales.OpenInvoices"}, views)

	// 5. I scan again. Nothing changed in the database, so nothing changes in the folder:
	// every file is as it was, by content.
	first := treeHashes(t, projectDir, "")
	require.Len(t, first, 11)
	stderr, err = scan()
	require.NoError(t, err)
	assert.Empty(t, stderr, "a rescan with nothing to leave out says nothing on stderr")
	assert.Equal(t, first, treeHashes(t, projectDir, ""), "a rescan of an unchanged database leaves the folder byte-identical")

	// 6. A table is dropped from the database and another is added; I scan again. The
	// dropped table's folder is gone, with its line, and nothing else changed.
	fake.relations = append(fake.relations[:1], fake.relations[2:]...)
	fake.relations = append(fake.relations, pgRelation{schema: "sales", name: "Payment",
		fields:     []dbschema.FieldDef{pgField("PaymentId", dbschema.Int, false)},
		primaryKey: []dal.FieldName{"PaymentId"}})
	stderr, err = scan()
	require.NoError(t, err)
	assert.Equal(t, `removed: dbmodels/shop/public/tables/order_line: table "order_line" of schema "public" is no longer in the database`+"\n", stderr,
		"one line for the folder that was removed, which names it")
	added, removed, changed := diffTrees(first, treeHashes(t, projectDir, ""))
	assert.Equal(t, []string{"dbmodels/shop/sales/tables/Payment/sales.Payment.columns.json"}, added)
	assert.Equal(t, []string{"dbmodels/shop/public/tables/order_line/public.order_line.columns.json"}, removed)
	assert.Empty(t, changed, "every other file is as the first scan wrote it")
	assert.NoDirExists(t, filepath.Join(projectDir, "dbmodels", "shop", "public", "tables", "order_line"), "the folder of the dropped table is removed, not left empty")
	assertNoSourceIn(t, projectDir)
	project, err = projStore.LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())
	_, _, tables, _ = webReaderTables(t, projectDir)
	assert.Equal(t, []string{"public.Customer", "sales.Invoice", "sales.Payment"}, tables)
}

// A column's default is saved in the field the columns file has for it, as the text of the
// expression the reader reported; a column with none, and an identity column, have no such field;
// and what is saved is saved again by a rescan, byte for byte. The relations of the journey above
// have no default, so that their files stay the files of main (TestScanJourneyIntoACleanFolderWritesWhatMainWrote).
func TestScanJourneyPostgresSavesColumnDefaults(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	identity := pgField("Id", dbschema.Int, false)
	identity.AutoIncrement = true
	fake := &fakePgDatabase{relations: []pgRelation{
		{schema: "public", name: "Item", primaryKey: []dal.FieldName{"Id"}, fields: []dbschema.FieldDef{
			identity,
			pgFieldDefault(pgField("Status", dbschema.String, false), "'new'::text"),
			pgFieldDefault(pgField("Made", dbschema.Time, false), "now()"),
			pgFieldDefault(pgField("Qty", dbschema.Int, false), "0"),
			pgFieldDefault(pgField("Hits", dbschema.Int, false), "nextval('sales.hits_seq'::regclass)"),
			pgFieldDefault(pgField("Double", dbschema.Int, true), `GENERATED ALWAYS AS (("Qty" * 2))`),
			pgField("Note", dbschema.String, true),
		}},
		{schema: "sales", name: "Tickets", view: true, fields: []dbschema.FieldDef{pgField("Id", dbschema.Int, true)}},
	}}
	usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return fake })
	scan := func() {
		t.Helper()
		stderr, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
		require.NoError(t, err)
		assert.Empty(t, stderr)
	}

	scan()

	assert.Equal(t, map[string]string{
		"Status": "'new'::text",
		"Made":   "now()",
		"Qty":    "0",
		"Hits":   "nextval('sales.hits_seq'::regclass)",
		"Double": `GENERATED ALWAYS AS (("Qty" * 2))`,
	}, storedDefaults(t, projectDir, "public", "tables", "Item"))
	assert.Empty(t, storedDefaults(t, projectDir, "sales", "views", "Tickets"))
	first := treeHashes(t, projectDir, "")
	scan()
	assert.Equal(t, first, treeHashes(t, projectDir, ""), "a rescan of an unchanged database leaves the folder byte-identical")
}

// A project scanned before views were told from tables holds a view as a table. The next scan
// takes that folder back, with the line a dropped table gets, and saves the view among the views;
// every other file stays as it was.
func TestScanJourneyPostgresMovesAViewSavedAsATableToTheViews(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	fake := newJourneyPgDatabase()
	for i := range fake.relations {
		if fake.relations[i].name == "customer_names" {
			fake.relations[i].view = false // as the reader of the release before could not tell
		}
	}
	usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return fake })
	scan := func() string {
		t.Helper()
		stderr, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
		require.NoError(t, err)
		return stderr
	}
	assert.Empty(t, scan())
	require.FileExists(t, filepath.Join(projectDir, "dbmodels", "shop", "public", "tables", "customer_names", "public.customer_names.columns.json"))
	before := treeHashes(t, projectDir, "")

	for i := range fake.relations {
		if fake.relations[i].name == "customer_names" {
			fake.relations[i].view = true
		}
	}
	stderr := scan()

	assert.Equal(t, `removed: dbmodels/shop/public/tables/customer_names: table "customer_names" of schema "public" is no longer in the database`+"\n", stderr)
	added, removed, changed := diffTrees(before, treeHashes(t, projectDir, ""))
	assert.Equal(t, []string{"dbmodels/shop/public/views/customer_names/public.customer_names.columns.json"}, added)
	assert.Equal(t, []string{"dbmodels/shop/public/tables/customer_names/public.customer_names.columns.json"}, removed)
	assert.Empty(t, changed)
}

// A PostgreSQL scan never writes the connection into a file, and never says of it more than where the
// connection string is read from: no host, no port, no database, no user name, no password, no query string.
func TestScanJourneyPostgresNamesNothingOfTheConnection(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	logged := captureScanLog(t)
	usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })

	stderr, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
	require.NoError(t, err)

	assert.Contains(t, logged.String(), "connecting; the PostgreSQL connection string is read from the environment variable "+journeyPgVar+"\n", "the line that says what the scan connects to")
	for _, shown := range []string{journeyPgSecret, journeyPgUser, journeyPgHost, ":" + journeyPgPort, "sslmode", "postgres://"} {
		assert.NotContains(t, logged.String(), shown)
		assert.NotContains(t, stderr, shown)
	}
}

// Two servers that a project records by driver alone do not collide: a SQLite file and a
// PostgreSQL database scanned into one environment of one project are both kept, each
// resolves to its own source, and a rescan of either leaves the other as it was. So are two
// PostgreSQL databases: the project records the driver and not the host, so they are on one
// server of the project, each with its own descriptor.
func TestScanJourneyPostgresAndSQLiteInOneEnvironment(t *testing.T) {
	for _, order := range []string{"sqlite first", "postgres first"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			projectDir := filepath.Join(t.TempDir(), "company")
			require.NoError(t, os.Mkdir(projectDir, 0o755))
			sqlitePath := filepath.Join(projectDir, "data", "crm.db")
			writeCRMDB(t, sqlitePath)
			fake := newJourneyPgDatabase()
			usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return fake })
			scanSQLite := func() {
				t.Helper()
				stderr, err := runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", sqlitePath, "--db", "crm", "--env", "local")
				require.NoError(t, err)
				assert.Empty(t, stderr)
			}
			scanPostgres := func() {
				t.Helper()
				stderr, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
				require.NoError(t, err)
				assert.Empty(t, stderr)
			}
			if order == "sqlite first" {
				scanSQLite()
				scanPostgres()
			} else {
				scanPostgres()
				scanSQLite()
			}

			store, id := filestore.NewSingleProjectStore(projectDir, "")
			projStore := store.GetProjectStore(id)
			project, err := projStore.LoadProject(ctx)
			require.NoError(t, err)
			require.NoError(t, project.Validate())
			env := project.Environments.GetByID("local")
			require.NotNil(t, env)
			drivers := map[string][]string{}
			for _, server := range env.DbServers {
				drivers[server.Driver] = server.Catalogs
				assert.Empty(t, server.Host, "no host is recorded: %s", server.Driver)
				assert.Zero(t, server.Port, "no port is recorded: %s", server.Driver)
			}
			assert.Equal(t, map[string][]string{"sqlite3": {"crm"}, "postgres": {"shop"}}, drivers, "both servers are kept, each with its own database")

			sources, err := api.ListSources(ctx, projStore, projectDir, "local")
			require.NoError(t, err)
			urls := map[string]string{}
			for _, source := range sources {
				urls[source.ID] = source.URL
			}
			assert.Equal(t, map[string]string{"crm": "sqlite://" + sqlitePath, "shop": "env:" + journeyPgVar}, urls, "each database resolves to its own source")
			for database, driver := range map[string]string{"crm": "sqlite3", "shop": "postgres"} {
				catalog, err := projStore.LoadEnvDbCatalog(ctx, "local", "", database)
				require.NoError(t, err)
				assert.Equal(t, driver, catalog.Driver, database)
			}
			assertNoSourceIn(t, projectDir)

			// A rescan of either leaves the other as it was.
			before := treeHashes(t, projectDir, "data/")
			scanSQLite()
			scanPostgres()
			assert.Equal(t, before, treeHashes(t, projectDir, "data/"), "a rescan of both leaves the folder byte-identical")
		})
	}
}

// PostgreSQL names are case-sensitive: tables that differ only by case, and names that cannot
// be folder names, follow the rules of the SQLite scan. Each is named on stderr and left out,
// the scan exits 0, and every other table of every schema is written under its own folder.
func TestScanJourneyPostgresNamesThatCannotBeFolders(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	field := []dbschema.FieldDef{pgField("id", dbschema.Int, false)}
	fake := &fakePgDatabase{relations: []pgRelation{
		{schema: "public", name: "Customer", fields: field, primaryKey: []dal.FieldName{"id"}},
		{schema: "public", name: "customer", fields: field, primaryKey: []dal.FieldName{"id"}},
		{schema: "public", name: "a/b", fields: field},
		{schema: "public", name: "con", fields: field},
		{schema: "Reports", name: "Sales", fields: field},
		{schema: "reports", name: "Other", fields: field},
		{schema: "x/y", name: "Hidden", fields: field},
	}}
	usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return fake })

	stderr, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)

	require.NoError(t, err, "what a scan leaves out never fails it")
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	assert.Len(t, lines, 5, stderr)
	for _, left := range []string{`table "a/b" of schema "public"`, `table "con" of schema "public"`, `table "customer" of schema "public"`, `schema "reports"`, `schema "x/y" is left out`} {
		assert.Contains(t, stderr, left)
	}
	_, _, tables, _ := webReaderTables(t, projectDir)
	assert.Equal(t, []string{"Reports.Sales", "public.Customer"}, tables, "the table that sorts first of two that differ by case is kept, in its own schema's folder")
}

func TestScanJourneyPostgresReadErrorsAreClassified(t *testing.T) {
	hint := "the PostgreSQL connection string is read from the environment variable " + journeyPgVar
	for _, tc := range []struct {
		name  string
		err   error
		phase string
		// want is the whole message, and code the exit code of the command.
		want string
		code int
	}{
		{
			name: "a read the server refuses is a failure of the read, with no word of the connection string and exit 1",
			err:  errors.New("lost connection to " + journeyPgURL("shop")),
			want: "failed to get dbCatalog metadata: the catalog could not be read (the server's own message is not shown: a driver can quote the connection string)",
			code: 1,
		},
		{
			name: "a connection that fails during the read is a connection failure: the adapter's sentence, the hint, exit 4",
			err:  &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork, Host: journeyPgHost, Port: journeyPgPort, Database: "shop"},
			want: "failed to get dbCatalog metadata: the server could not be reached; " + hint,
			code: 4,
		},
		{
			name: "a read that ends by the clock is a connection failure too",
			err:  fmt.Errorf("read the catalog: %w", context.DeadlineExceeded),
			want: "failed to get dbCatalog metadata: the attempt timed out; " + hint,
			code: 4,
		},
		{
			name:  "a pooled connection fails while reading a table",
			err:   &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork},
			phase: "description",
			want:  "failed to get dbCatalog metadata: the server could not be reached; " + hint,
			code:  4,
		},
		{
			name:  "a pooled connection fails while reading indexes",
			err:   &dalgo2postgres.ConnectionError{Kind: dalgo2postgres.FailureNetwork},
			phase: "indexes",
			want:  "failed to get dbCatalog metadata: the server could not be reached; " + hint,
			code:  4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "shop-project")
			var boom dbcopy.SchemaScanDB = &failingPgDatabase{fakePgDatabase: newJourneyPgDatabase(), err: tc.err}
			if tc.phase != "" {
				boom = &failedReadPgDatabase{fakePgDatabase: newJourneyPgDatabase(), err: tc.err, phase: tc.phase}
			}
			usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return boom })

			_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)

			require.Error(t, err)
			assert.EqualError(t, err, tc.want)
			var coder ExitCoder
			if tc.code == 1 {
				assert.False(t, errors.As(err, &coder), "a plain failure has no code of its own: it exits 1")
			} else if assert.ErrorAs(t, err, &coder) {
				assert.Equal(t, tc.code, coder.ExitCode())
			}
			for _, shown := range []string{journeyPgSecret, journeyPgUser, journeyPgHost, ":" + journeyPgPort, "lost connection"} {
				assert.NotContains(t, err.Error(), shown)
			}
			assert.NoDirExists(t, projectDir, "a scan that fails makes nothing")
		})
	}
}

// failingPgDatabase is a fake server whose listing of the tables fails.
type failingPgDatabase struct {
	*fakePgDatabase
	err error
}

func (f *failingPgDatabase) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return nil, f.err
}

func (f *failingPgDatabase) ListSchemas(context.Context) ([]string, error) { return nil, f.err }

// failedReadPgDatabase answers the listings and fails one call made by a scan worker.
type failedReadPgDatabase struct {
	*fakePgDatabase
	err   error
	phase string
}

func (f *failedReadPgDatabase) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if f.phase == "description" {
		return nil, f.err
	}
	return f.fakePgDatabase.DescribeCollection(ctx, ref)
}

func (f *failedReadPgDatabase) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	if f.phase == "indexes" {
		return nil, f.err
	}
	return f.fakePgDatabase.ListIndexes(ctx, ref)
}

// propertyPgModes are the ways the fake server of the property test answers a scan.
var propertyPgModes = []string{"the open fails", "the listing fails", "it works"}

// propertyPgServer is the fake server of TestProperty_NoCommandPathEchoesASourceSecret. It
// fails as a driver can: with the URL it was given, password included, in its words.
type propertyPgServer struct {
	mode   string
	opened []dbcopy.BackendRef
}

func (s *propertyPgServer) open(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
	s.opened = append(s.opened, ref)
	words := errors.New("dalgo2postgres: PingContext(" + fmt.Sprintf("%q", ref.Path) + "): lost connection to " + ref.Path)
	switch s.mode {
	case propertyPgModes[0]:
		return nil, words
	case propertyPgModes[1]:
		return &failingPgDatabase{fakePgDatabase: newJourneyPgDatabase(), err: words}, nil
	}
	return newJourneyPgDatabase(), nil
}

// assertReached fails t when the scans did not reach the fake server at least perCase times,
// or reached it for anything but a variable.
func (s *propertyPgServer) assertReached(t *testing.T, perCase int) {
	t.Helper()
	if len(s.opened) < perCase {
		t.Errorf("the scans of the property reached the fake server %d times: the property does not reach what comes after the open", len(s.opened))
	}
	for _, ref := range s.opened {
		if !strings.HasPrefix(ref.Raw, "env:") {
			t.Errorf("the fake server was asked for %q, which is not a variable", ref.Display())
		}
	}
}

// Two PostgreSQL databases of two hosts, scanned into one environment of one project, are both
// kept on the one server the project records (the driver), each resolving to the variable of its
// own descriptor.
func TestScanJourneyTwoPostgresDatabasesInOneEnvironment(t *testing.T) {
	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "company")
	opener := usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
	const otherVar = "DATATUG_JOURNEY_PG_CRM_URL"
	opener.serve(otherVar, "crm", func() dbcopy.SchemaScanDB {
		return &fakePgDatabase{relations: []pgRelation{{schema: "public", name: "Deal", fields: []dbschema.FieldDef{pgField("Id", dbschema.Int, false)}, primaryKey: []dal.FieldName{"Id"}}}}
	})

	for _, scan := range []struct{ variable, database string }{{journeyPgVar, "shop"}, {otherVar, "crm"}} {
		stderr, err := runScanCommand(t, pgScanArgs(projectDir, scan.variable, scan.database, "local")...)
		require.NoError(t, err)
		assert.Empty(t, stderr)
	}

	store, id := filestore.NewSingleProjectStore(projectDir, "")
	projStore := store.GetProjectStore(id)
	project, err := projStore.LoadProject(ctx)
	require.NoError(t, err)
	require.NoError(t, project.Validate())
	env := project.Environments.GetByID("local")
	require.NotNil(t, env)
	require.Len(t, env.DbServers, 1, "the project records the driver, so both are on one server")
	assert.Equal(t, datatug.ServerRef{Driver: "postgres"}, env.DbServers[0].ServerRef)
	assert.Equal(t, []string{"shop", "crm"}, env.DbServers[0].Catalogs)
	sources, err := api.ListSources(ctx, projStore, projectDir, "local")
	require.NoError(t, err)
	urls := map[string]string{}
	for _, source := range sources {
		urls[source.ID] = source.URL
	}
	assert.Equal(t, map[string]string{"shop": "env:" + journeyPgVar, "crm": "env:" + otherVar}, urls)
	assert.Equal(t, map[string]any{"dsnEnv": otherVar}, readJSONMap(t, filepath.Join(projectDir, "connections", "local", "crm.json")))
	assertNoSourceIn(t, projectDir)
	assert.Equal(t, 2, len(opener.opened))
}
