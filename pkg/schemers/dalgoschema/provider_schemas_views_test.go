package dalgoschema

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multiSchemaReader is a reader that lists its collections through references that name
// their schema, as a reader of a server with more than one schema does, and that can tell
// the views among them (it is a ViewLister).
type multiSchemaReader struct {
	mu        sync.Mutex
	listed    []dal.CollectionRef
	views     []dal.CollectionRef
	viewsErr  error
	defs      map[string]*dbschema.CollectionDef // by "schema.name"
	described []string
}

var (
	_ dbschema.SchemaReader = (*multiSchemaReader)(nil)
	_ ViewLister            = (*multiSchemaReader)(nil)
)

func (m *multiSchemaReader) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return m.listed, nil
}

func (m *multiSchemaReader) ListViews(context.Context) ([]dal.CollectionRef, error) {
	return m.views, m.viewsErr
}

func (m *multiSchemaReader) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name := ref.Schema() + "." + ref.Name()
	m.described = append(m.described, name)
	def, ok := m.defs[name]
	if !ok {
		return nil, fmt.Errorf("collection %q not found", name)
	}
	return def, nil
}

func (*multiSchemaReader) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}

func (*multiSchemaReader) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints"}
}

func (*multiSchemaReader) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers"}
}

// shopAndSalesReader holds a Customer in schema public and another, with other columns, in
// schema sales, which also has an Invoice that references the sales Customer, a view of
// the invoices, and a key to a Region that only schema public has.
func shopAndSalesReader() *multiSchemaReader {
	qualified := func(schema, name string) dal.CollectionRef {
		return dal.NewQualifiedRootCollectionRef(schema, name, "")
	}
	return &multiSchemaReader{
		listed: []dal.CollectionRef{qualified("public", "Customer"), qualified("public", "Region"), qualified("sales", "Customer"), qualified("sales", "Invoice"), qualified("sales", "OpenInvoices")},
		views:  []dal.CollectionRef{qualified("sales", "OpenInvoices")},
		defs: map[string]*dbschema.CollectionDef{
			"public.Customer": {Name: "Customer", Fields: []dbschema.FieldDef{{Name: "CustomerId", Type: dbschema.Int}}, PrimaryKey: fields("CustomerId")},
			"public.Region":   {Name: "Region", Fields: []dbschema.FieldDef{{Name: "RegionId", Type: dbschema.Int}}, PrimaryKey: fields("RegionId")},
			"sales.Customer":  {Name: "Customer", Fields: []dbschema.FieldDef{{Name: "Id", Type: dbschema.Int}, {Name: "Company", Type: dbschema.String}}, PrimaryKey: fields("Id")},
			"sales.Invoice": {Name: "Invoice",
				Fields:     []dbschema.FieldDef{{Name: "InvoiceId", Type: dbschema.Int}, {Name: "CustomerId", Type: dbschema.Int}, {Name: "RegionId", Type: dbschema.Int}},
				PrimaryKey: fields("InvoiceId"),
				ForeignKeys: []dbschema.ForeignKeyDef{
					{Name: "fk_invoice_customer", Fields: fields("CustomerId"), ReferencedCollection: "Customer", ReferencedFields: fields("Id")},
					{Name: "fk_invoice_region", Fields: fields("RegionId"), ReferencedCollection: "Region", ReferencedFields: fields("RegionId")},
				}},
			"sales.OpenInvoices": {Name: "OpenInvoices", Fields: []dbschema.FieldDef{{Name: "InvoiceId", Type: dbschema.Int, Nullable: true}}},
		},
	}
}

// A collection is in the schema its reference names, with the columns that schema's
// table has: two tables of one name in two schemas are two tables, a view is a view, and
// a foreign key is to the table of its own schema that the reader listed.
func TestScanCatalog_ReadsEachCollectionInTheSchemaItsReferenceNames(t *testing.T) {
	reader := shopAndSalesReader()
	var logged strings.Builder
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })

	catalog, err := schemer.NewScanner(NewSchemaProvider(reader, nil, "shop", "public")).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)

	require.Len(t, catalog.Schemas, 2)
	public, sales := catalog.Schemas.GetByID("public"), catalog.Schemas.GetByID("sales")
	require.NotNil(t, public)
	require.NotNil(t, sales)
	var publicTables, salesTables, salesViews []string
	for _, table := range public.Tables {
		publicTables = append(publicTables, table.Name())
	}
	for _, table := range sales.Tables {
		salesTables = append(salesTables, table.Name())
	}
	for _, view := range sales.Views {
		salesViews = append(salesViews, view.Name())
	}
	assert.ElementsMatch(t, []string{"Customer", "Region"}, publicTables)
	assert.ElementsMatch(t, []string{"Customer", "Invoice"}, salesTables)
	assert.Equal(t, []string{"OpenInvoices"}, salesViews)
	assert.Empty(t, public.Views)

	columnsOf := func(schema string) map[string][]string {
		out := map[string][]string{}
		for _, collection := range append(catalog.Schemas.GetByID(schema).Tables, catalog.Schemas.GetByID(schema).Views...) {
			for _, column := range collection.Columns {
				out[collection.Name()] = append(out[collection.Name()], column.Name)
			}
		}
		return out
	}
	assert.Equal(t, []string{"CustomerId"}, columnsOf("public")["Customer"], "the Customer of public has its own columns")
	assert.Equal(t, []string{"Id", "Company"}, columnsOf("sales")["Customer"], "the Customer of sales has its own")
	assert.Equal(t, []string{"InvoiceId"}, columnsOf("sales")["OpenInvoices"])
	assert.ElementsMatch(t, []string{"public.Customer", "public.Region", "sales.Customer", "sales.Invoice", "sales.OpenInvoices"}, reader.described)

	for _, table := range sales.Tables {
		if table.Name() != "Invoice" {
			continue
		}
		require.Len(t, table.ForeignKeys, 1, "the key to the Region that only schema public has is left out")
		assert.Equal(t, "fk_invoice_customer", table.ForeignKeys[0].Name)
		assert.Equal(t, "sales", table.ForeignKeys[0].RefTable.Schema(), "the key is to the Customer of the table's own schema")
	}
	assert.Contains(t, logged.String(), "fk_invoice_region")
	assert.Contains(t, logged.String(), "did not list")
}

// A view is not counted: counting the rows of a view runs its query.
func TestScanCatalog_DoesNotCountTheRecordsOfAView(t *testing.T) {
	reader := shopAndSalesReader()
	counted := map[string]bool{}
	var mu sync.Mutex
	counter := countFunc(func(schema, table string) (*int, error) {
		mu.Lock()
		defer mu.Unlock()
		counted[schema+"."+table] = true
		return intPtr(7), nil
	})

	catalog, err := schemer.NewScanner(NewSchemaProvider(reader, counter, "shop", "public")).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)

	assert.Equal(t, map[string]bool{"public.Customer": true, "public.Region": true, "sales.Customer": true, "sales.Invoice": true}, counted)
	assert.Nil(t, catalog.Schemas.GetByID("sales").Views[0].RecordsCount)
}

// countFunc is a RecordsCounter that is a function.
type countFunc func(schema, table string) (*int, error)

func (f countFunc) CountRecords(_ context.Context, schema, table string) (*int, error) {
	return f(schema, table)
}

func TestGetCollections_FailsWhenTheReaderCannotListItsViews(t *testing.T) {
	reader := shopAndSalesReader()
	reader.viewsErr = errors.New("boom")

	_, err := NewSchemaProvider(reader, nil, "shop", "public").GetCollections(context.Background(), nil)

	assert.ErrorContains(t, err, "list views")
	assert.ErrorIs(t, err, reader.viewsErr)
}

// A reader that is no ViewLister reports every collection as a table, in the provider's schema.
func TestGetCollections_AReaderThatCannotTellViewsReportsOnlyTables(t *testing.T) {
	collections, err := newShopProvider(shopReader(), nil).GetCollections(context.Background(), nil)
	require.NoError(t, err)
	for {
		collection, err := collections.NextCollection()
		if errors.Is(err, io.EOF) {
			return
		}
		require.NoError(t, err)
		assert.Equal(t, "BASE TABLE", collection.DbType, collection.Name())
		assert.Equal(t, "public", collection.Schema(), collection.Name())
		assert.Empty(t, collection.Ref.Schema(), "a reference that names no schema stays as it was")
	}
}
