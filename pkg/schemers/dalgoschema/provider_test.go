package dalgoschema

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCounter answers RecordsCount from a fixed table.
type fakeCounter struct {
	counts map[string]*int
	errs   map[string]error
}

func (f fakeCounter) CountRecords(_ context.Context, schema, table string) (*int, error) {
	if err := f.errs[table]; err != nil {
		return nil, err
	}
	if schema != "public" {
		return nil, errors.New("counter got schema " + schema)
	}
	return f.counts[table], nil
}

func newShopProvider(reader *fakeReader, counter RecordsCounter) schemer.SchemaProvider {
	return NewSchemaProvider(reader, counter, "shop", "public")
}

func TestNewSchemaProvider_RequiresAReader(t *testing.T) {
	assert.PanicsWithValue(t, "reader cannot be nil", func() { NewSchemaProvider(nil, nil, "shop", "public") })
}

func TestScanCatalog_ReadsTablesColumnsKeysAndForeignKeysUnderExactNames(t *testing.T) {
	reader := shopReader()
	counter := fakeCounter{counts: map[string]*int{"Customer": intPtr(3), "Order": intPtr(5)}, errs: map[string]error{"OrderLine": errors.New("count refused")}}
	catalog, err := schemer.NewScanner(newShopProvider(reader, counter)).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)
	require.Len(t, catalog.Schemas, 1)
	schema := catalog.Schemas[0]
	assert.Equal(t, "public", schema.ID)
	assert.Empty(t, schema.Views)
	require.Len(t, schema.Tables, 3)

	customer, order, line := schema.Tables[0], schema.Tables[1], schema.Tables[2]
	for _, table := range schema.Tables {
		assert.Equal(t, "BASE TABLE", table.DbType, table.Name())
		assert.Equal(t, "public", table.Schema(), table.Name())
		assert.Equal(t, "shop", table.Catalog(), table.Name())
		assert.Equal(t, datatug.CollectionType("table"), table.Type(), table.Name())
		assert.NoError(t, table.Validate(), table.Name())
	}
	assert.Equal(t, []string{"Customer", "Order", "OrderLine"}, []string{customer.Name(), order.Name(), line.Name()})

	// Columns, in the order PostgreSQL reports them.
	require.Len(t, customer.Columns, 6)
	type column struct {
		name     string
		position int
		pk       int
		nullable bool
		dbType   string
	}
	var got []column
	for _, c := range customer.Columns {
		got = append(got, column{c.Name, c.OrdinalPosition, c.PrimaryKeyPosition, c.IsNullable, c.DbType})
	}
	assert.Equal(t, []column{
		{"CustomerId", 1, 1, false, "int"},
		{"Email", 2, 0, false, "string"},
		{"Name", 3, 0, true, "string"},
		{"Balance", 4, 0, true, "decimal"},
		{"Notes", 5, 0, true, "string"},
		{"Raw", 6, 0, true, "bytes"},
	}, got)
	require.NotNil(t, customer.Columns[1].CharMaxLength)
	assert.Equal(t, 255, *customer.Columns[1].CharMaxLength)
	assert.Nil(t, customer.Columns[2].CharMaxLength, "text has no length")
	require.NotNil(t, customer.Columns[5].CharMaxLength)
	assert.Equal(t, 16, *customer.Columns[5].CharMaxLength)

	// Primary keys come from the columns, in key order, not column order.
	require.NotNil(t, customer.PrimaryKey)
	assert.Equal(t, datatug.UniqueKey{Name: "PK_Customer", Columns: []string{"CustomerId"}}, *customer.PrimaryKey)
	require.NotNil(t, line.PrimaryKey)
	assert.Equal(t, []string{"OrderId", "LineNo"}, line.PrimaryKey.Columns)

	// Unique constraints become alternate keys, one per constraint.
	assert.Equal(t, []datatug.UniqueKey{
		{Name: "uq_customer_email", Columns: []string{"Email"}},
		{Name: "uq_customer_name_email", Columns: []string{"Name", "Email"}},
	}, customer.AlternateKeys)

	// Indexes with their columns.
	require.Len(t, customer.Indexes, 3)
	byName := map[string]*datatug.Index{}
	for _, index := range customer.Indexes {
		byName[index.Name] = index
	}
	assert.False(t, byName["idx_customer_name"].IsUnique)
	assert.True(t, byName["uq_customer_email"].IsUnique)
	assert.NotEmpty(t, byName["idx_customer_name"].Type)
	columnNames := func(index *datatug.Index) (names []string) {
		for _, c := range index.Columns {
			names = append(names, c.Name)
		}
		return names
	}
	assert.Equal(t, []string{"Name"}, columnNames(byName["idx_customer_name"]))
	assert.Equal(t, []string{"Name", "Email"}, columnNames(byName["uq_customer_name_email"]))
	assert.Empty(t, line.Indexes)

	// Foreign keys, with the name and the rules PostgreSQL reports. The key into
	// another schema is outside the scan and is left out.
	require.Len(t, order.ForeignKeys, 2)
	assert.Equal(t, "fk_order_customer", order.ForeignKeys[0].Name)
	assert.Equal(t, []string{"CustomerId"}, order.ForeignKeys[0].Columns)
	assert.Equal(t, "Customer", order.ForeignKeys[0].RefTable.Name())
	assert.Equal(t, "public", order.ForeignKeys[0].RefTable.Schema())
	assert.Equal(t, "CASCADE", order.ForeignKeys[0].DeleteRule)
	assert.Equal(t, "NO ACTION", order.ForeignKeys[0].UpdateRule)
	assert.Equal(t, "fk_order_billing", order.ForeignKeys[1].Name)
	assert.Equal(t, []string{"BillingCustomerId"}, order.ForeignKeys[1].Columns)
	assert.Equal(t, "SET NULL", order.ForeignKeys[1].DeleteRule)
	require.Len(t, line.ForeignKeys, 1)
	assert.Equal(t, "Order", line.ForeignKeys[0].RefTable.Name())

	// The referenced table lists the referrer once, with both of its keys.
	require.Len(t, customer.ReferencedBy, 1)
	assert.Equal(t, "Order", customer.ReferencedBy[0].Name())
	require.Len(t, customer.ReferencedBy[0].ForeignKeys, 2)
	assert.Equal(t, "fk_order_customer", customer.ReferencedBy[0].ForeignKeys[0].Name)
	assert.Equal(t, "fk_order_billing", customer.ReferencedBy[0].ForeignKeys[1].Name)
	require.Len(t, order.ReferencedBy, 1)
	assert.Equal(t, "OrderLine", order.ReferencedBy[0].Name())

	// Record counts, and a count that fails leaves the table without one.
	require.NotNil(t, customer.RecordsCount)
	assert.Equal(t, 3, *customer.RecordsCount)
	require.NotNil(t, order.RecordsCount)
	assert.Equal(t, 5, *order.RecordsCount)
	assert.Nil(t, line.RecordsCount)

	// One read of each kind per table, however many provider methods asked.
	assert.Equal(t, 1, reader.callCount("ListCollections"))
	assert.Equal(t, 3, reader.callCount("DescribeCollection"))
	assert.Equal(t, 3, reader.callCount("ListIndexes"))
	assert.Equal(t, 3, reader.callCount("ListConstraints"))
}

func TestScanCatalog_FailsOnAReaderError(t *testing.T) {
	boom := errors.New("catalog unreachable")
	reader := shopReader()
	reader.errs = map[string]error{"ListCollections": boom}
	catalog, err := schemer.NewScanner(newShopProvider(reader, nil)).ScanCatalog(context.Background(), "shop")
	assert.ErrorIs(t, err, boom)
	assert.NotNil(t, catalog)
}

func TestProvider_IsNotBulk(t *testing.T) {
	assert.False(t, newShopProvider(shopReader(), nil).IsBulkProvider())
}

func TestGetCollections(t *testing.T) {
	reader := shopReader()
	provider := newShopProvider(reader, nil)
	collections, err := provider.GetCollections(context.Background(), schemer.NewSchemaKey("shop", ""))
	require.NoError(t, err)
	var names []string
	for {
		collection, err := collections.NextCollection()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		names = append(names, collection.Name())
	}
	assert.Equal(t, []string{"Customer", "Order", "OrderLine"}, names)
	_, err = collections.NextCollection()
	assert.ErrorIs(t, err, io.EOF, "a finished reader keeps answering EOF")

	reader.errs = map[string]error{"ListCollections": errors.New("boom")}
	_, err = provider.GetCollections(context.Background(), nil)
	assert.ErrorContains(t, err, "list collections")
	assert.ErrorContains(t, err, "boom")
}

func TestGetColumns(t *testing.T) {
	ctx := context.Background()
	reader := shopReader()
	provider := newShopProvider(reader, nil)
	ref := dal.NewRootCollectionRef("Order", "")

	columns, err := provider.GetColumns(ctx, "shop", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	require.Len(t, columns, 6)
	assert.Equal(t, "public", columns[0].SchemaName)
	assert.Equal(t, "Order", columns[0].TableName)
	assert.Equal(t, "OrderId", columns[0].Name)

	// A name filter keeps the position the column has in the table.
	filtered, err := provider.GetColumns(ctx, "shop", schemer.ColumnsFilter{CollectionRef: &ref, ColNameRegex: regexp.MustCompile(`^Customer`)})
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	assert.Equal(t, "CustomerId", filtered[0].Name)
	assert.Equal(t, 2, filtered[0].OrdinalPosition)

	columnsReader, err := provider.GetColumnsReader(ctx, "shop", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)
	all, err := schemer.ReadColumns(ctx, columnsReader)
	require.NoError(t, err)
	assert.Len(t, all, 6)
	_, err = columnsReader.NextColumn()
	assert.ErrorIs(t, err, io.EOF)

	_, err = provider.GetColumns(ctx, "shop", schemer.ColumnsFilter{})
	assert.ErrorContains(t, err, "collection reference is required")
	_, err = provider.GetColumnsReader(ctx, "shop", schemer.ColumnsFilter{})
	assert.ErrorContains(t, err, "collection reference is required")

	missing := dal.NewRootCollectionRef("Nope", "")
	_, err = provider.GetColumnsReader(ctx, "shop", schemer.ColumnsFilter{CollectionRef: &missing})
	assert.ErrorContains(t, err, "describe Nope")
	assert.ErrorContains(t, err, "not found")
}

func TestGetColumns_AReferenceThatNamesItsSchemaIsReadFromThere(t *testing.T) {
	reader := shopReader()
	provider := newShopProvider(reader, nil)
	qualified := dal.NewQualifiedRootCollectionRef("archive", "Order", "")
	columns, err := provider.GetColumns(context.Background(), "shop", schemer.ColumnsFilter{CollectionRef: &qualified})
	require.NoError(t, err)
	assert.Equal(t, "archive", columns[0].SchemaName)
	assert.Equal(t, "archive", reader.refs[0].Schema())
}

func TestIndexes(t *testing.T) {
	ctx := context.Background()
	reader := shopReader()
	provider := newShopProvider(reader, nil)

	indexes, err := provider.GetIndexes(ctx, "shop", "public", "Customer")
	require.NoError(t, err)
	var names []string
	for {
		index, err := indexes.NextIndex()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		names = append(names, index.Name)
		assert.Equal(t, "Customer", index.TableName)
		assert.Equal(t, "public", index.SchemaName)
	}
	assert.Equal(t, []string{"idx_customer_name", "uq_customer_email", "uq_customer_name_email"}, names)

	columns, err := provider.GetIndexColumns(ctx, "shop", "public", "Customer", "uq_customer_name_email")
	require.NoError(t, err)
	var columnNames []string
	for {
		column, err := columns.NextIndexColumn()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		assert.Equal(t, "uq_customer_name_email", column.IndexName)
		columnNames = append(columnNames, column.Name)
	}
	assert.Equal(t, []string{"Name", "Email"}, columnNames)

	_, err = provider.GetIndexColumns(ctx, "shop", "public", "Customer", "no_such_index")
	assert.ErrorContains(t, err, `index "no_such_index" not found on Customer`)

	reader.errs = map[string]error{"ListIndexes": errors.New("boom")}
	_, err = provider.GetIndexes(ctx, "shop", "public", "Order")
	assert.ErrorContains(t, err, "list indexes of Order")
	_, err = provider.GetIndexColumns(ctx, "shop", "public", "Order", "idx_order_customer")
	assert.ErrorContains(t, err, "list indexes of Order")
}

func TestGetConstraints(t *testing.T) {
	ctx := context.Background()
	reader := shopReader()
	provider := newShopProvider(reader, nil)

	read := func(table string) (constraints []*schemer.Constraint) {
		t.Helper()
		constraintsReader, err := provider.GetConstraints(ctx, "shop", "public", table)
		require.NoError(t, err)
		for {
			constraint, err := constraintsReader.NextConstraint()
			if errors.Is(err, io.EOF) {
				_, err = constraintsReader.NextConstraint()
				require.ErrorIs(t, err, io.EOF)
				return constraints
			}
			require.NoError(t, err)
			constraints = append(constraints, constraint)
		}
	}

	// The primary key is left to the columns: the scanner would otherwise add
	// every key column twice.
	var customer []string
	for _, c := range read("Customer") {
		customer = append(customer, c.Type+" "+c.Name+" "+c.ColumnName)
	}
	assert.Equal(t, []string{
		"UNIQUE uq_customer_email Email",
		"UNIQUE uq_customer_name_email Name",
		"UNIQUE uq_customer_name_email Email",
	}, customer)

	order := read("Order")
	require.Len(t, order, 2)
	first := order[0]
	assert.Equal(t, "FOREIGN KEY", first.Type)
	assert.Equal(t, "fk_order_customer", first.Name)
	assert.Equal(t, "CustomerId", first.ColumnName)
	assert.Equal(t, schemer.TableRef{SchemaName: "public", TableName: "Order"}, first.TableRef)
	assert.Equal(t, "shop", first.RefTableCatalog)
	assert.Equal(t, "public", first.RefTableSchema)
	assert.Equal(t, "Customer", first.RefTableName)
	assert.Equal(t, "CustomerId", first.RefColName)
	assert.Equal(t, "NO ACTION", first.UpdateRule)
	assert.Equal(t, "CASCADE", first.DeleteRule)

	// Cached reads: the three constraint lists above cost one describe and one
	// index listing per table.
	assert.Equal(t, 2, reader.callCount("DescribeCollection"))
	assert.Equal(t, 2, reader.callCount("ListIndexes"))
}

func TestGetConstraints_Errors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("describe fails", func(t *testing.T) {
		reader := shopReader()
		reader.errs = map[string]error{"DescribeCollection": boom}
		_, err := newShopProvider(reader, nil).GetConstraints(ctx, "shop", "public", "Order")
		assert.ErrorIs(t, err, boom)
	})
	t.Run("constraint listing is optional", func(t *testing.T) {
		reader := shopReader()
		reader.errs = map[string]error{"ListConstraints": &dbschema.NotSupportedError{Op: "ListConstraints"}}
		constraints, err := newShopProvider(reader, nil).GetConstraints(ctx, "shop", "public", "Order")
		require.NoError(t, err)
		_, err = constraints.NextConstraint()
		assert.NoError(t, err, "the foreign keys still come from the description")
	})
	t.Run("constraint listing fails", func(t *testing.T) {
		reader := shopReader()
		reader.errs = map[string]error{"ListConstraints": boom}
		_, err := newShopProvider(reader, nil).GetConstraints(ctx, "shop", "public", "Order")
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "list constraints of Order")
	})
	t.Run("index listing fails", func(t *testing.T) {
		reader := shopReader()
		reader.errs = map[string]error{"ListIndexes": boom}
		_, err := newShopProvider(reader, nil).GetConstraints(ctx, "shop", "public", "Order")
		assert.ErrorIs(t, err, boom)
	})
	t.Run("a key with fewer referenced columns than columns still reads", func(t *testing.T) {
		reader := shopReader()
		reader.defs["Order"].ForeignKeys = []dbschema.ForeignKeyDef{
			{Name: "fk_short", Fields: fields("CustomerId", "BillingCustomerId"), ReferencedCollection: "Customer", ReferencedFields: fields("CustomerId")},
		}
		constraintsReader, err := newShopProvider(reader, nil).GetConstraints(ctx, "shop", "public", "Order")
		require.NoError(t, err)
		first, err := constraintsReader.NextConstraint()
		require.NoError(t, err)
		assert.Equal(t, "CustomerId", first.RefColName)
		second, err := constraintsReader.NextConstraint()
		require.NoError(t, err)
		assert.Empty(t, second.RefColName)
	})
}

func TestGetForeignKeys(t *testing.T) {
	ctx := context.Background()
	reader := shopReader()
	provider := newShopProvider(reader, nil)

	keys, err := provider.GetForeignKeys(ctx, "public", "Order")
	require.NoError(t, err)
	require.Len(t, keys, 3)
	assert.Equal(t, schemer.ForeignKey{
		Name: "fk_order_customer",
		From: schemer.FKAnchor{Name: "Order", Columns: []string{"CustomerId"}},
		To:   schemer.FKAnchor{Name: "Customer", Columns: []string{"CustomerId"}},
	}, keys[0])
	assert.Equal(t, "audit.Trail", keys[2].To.Name, "a key into another schema names the schema")

	keysReader, err := provider.GetForeignKeysReader(ctx, "public", "OrderLine")
	require.NoError(t, err)
	key, err := keysReader.NextForeignKey()
	require.NoError(t, err)
	assert.Equal(t, "fk_line_order", key.Name)
	_, err = keysReader.NextForeignKey()
	assert.ErrorIs(t, err, io.EOF)

	none, err := provider.GetForeignKeys(ctx, "public", "Customer")
	require.NoError(t, err)
	assert.Empty(t, none)

	reader.errs = map[string]error{"DescribeCollection": errors.New("boom")}
	_, err = newShopProvider(reader, nil).GetForeignKeysReader(ctx, "public", "Order")
	assert.ErrorContains(t, err, "boom")
	_, err = newShopProvider(reader, nil).GetForeignKeys(ctx, "public", "Order")
	assert.ErrorContains(t, err, "boom")
}

func TestGetReferrers_OneEntryPerForeignKey(t *testing.T) {
	ctx := context.Background()
	reader := shopReader()
	// Two more keys from one table, on the same column: legal in PostgreSQL.
	reader.defs["Twin"] = &dbschema.CollectionDef{Name: "Twin", ForeignKeys: []dbschema.ForeignKeyDef{
		{Name: "fk_twin_a", Fields: fields("OrderId"), ReferencedCollection: "Order", ReferencedFields: fields("OrderId")},
		{Name: "fk_twin_b", Fields: fields("OrderId"), ReferencedCollection: "Order", ReferencedFields: fields("OrderId")},
	}}
	reader.referrers["Order"] = append(reader.referrers["Order"],
		dbschema.Referrer{Collection: dal.NewRootCollectionRef("Twin", ""), Fields: fields("OrderId")},
		dbschema.Referrer{Collection: dal.NewRootCollectionRef("Twin", ""), Fields: fields("OrderId")},
	)
	provider := newShopProvider(reader, nil)

	customer, err := provider.GetReferrers(ctx, "public", "Customer")
	require.NoError(t, err)
	assert.Equal(t, []schemer.ForeignKey{
		{Name: "fk_order_billing", From: schemer.FKAnchor{Name: "Order", Columns: []string{"BillingCustomerId"}}, To: schemer.FKAnchor{Name: "Customer", Columns: []string{"CustomerId"}}},
		{Name: "fk_order_customer", From: schemer.FKAnchor{Name: "Order", Columns: []string{"CustomerId"}}, To: schemer.FKAnchor{Name: "Customer", Columns: []string{"CustomerId"}}},
	}, customer, "two keys from one table are two entries, not one merged entry")

	order, err := provider.GetReferrers(ctx, "public", "Order")
	require.NoError(t, err)
	var names []string
	for _, key := range order {
		names = append(names, key.Name+"<-"+key.From.Name)
	}
	assert.Equal(t, []string{"fk_line_order<-OrderLine", "fk_twin_a<-Twin", "fk_twin_b<-Twin"}, names, "identical keys are told apart")
}

func TestGetReferrers_BestEffortAndErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("not supported means none", func(t *testing.T) {
		reader := shopReader()
		reader.errs = map[string]error{"ListReferrers": &dbschema.NotSupportedError{Op: "ListReferrers"}}
		referrers, err := newShopProvider(reader, nil).GetReferrers(ctx, "public", "Customer")
		assert.NoError(t, err)
		assert.Empty(t, referrers)
	})
	t.Run("listing fails", func(t *testing.T) {
		reader := shopReader()
		reader.errs = map[string]error{"ListReferrers": boom}
		_, err := newShopProvider(reader, nil).GetReferrers(ctx, "public", "Customer")
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "list referrers of Customer")
	})
	t.Run("a referrer that cannot be described", func(t *testing.T) {
		reader := shopReader()
		reader.referrers["Customer"] = []dbschema.Referrer{{Collection: dal.NewRootCollectionRef("Ghost", ""), Fields: fields("CustomerId")}}
		_, err := newShopProvider(reader, nil).GetReferrers(ctx, "public", "Customer")
		assert.ErrorContains(t, err, "describe Ghost")
	})
	t.Run("a referrer whose key is not found keeps what the reader said", func(t *testing.T) {
		reader := shopReader()
		reader.referrers["Customer"] = []dbschema.Referrer{{Collection: dal.NewRootCollectionRef("Order", ""), Fields: fields("Total")}}
		referrers, err := newShopProvider(reader, nil).GetReferrers(ctx, "public", "Customer")
		require.NoError(t, err)
		assert.Equal(t, []schemer.ForeignKey{
			{From: schemer.FKAnchor{Name: "Order", Columns: []string{"Total"}}, To: schemer.FKAnchor{Name: "Customer"}},
		}, referrers)
	})
}

func TestRecordsCount(t *testing.T) {
	ctx := context.Background()
	counter := fakeCounter{counts: map[string]*int{"Customer": intPtr(42)}, errs: map[string]error{"Order": errors.New("refused")}}
	provider := newShopProvider(shopReader(), counter)

	count, err := provider.RecordsCount(ctx, "shop", "public", "Customer")
	require.NoError(t, err)
	require.NotNil(t, count)
	assert.Equal(t, 42, *count)

	_, err = provider.RecordsCount(ctx, "shop", "public", "Order")
	assert.ErrorContains(t, err, "refused")

	count, err = newShopProvider(shopReader(), nil).RecordsCount(ctx, "shop", "public", "Customer")
	assert.NoError(t, err)
	assert.Nil(t, count, "with no counter the count is unknown, not zero")
}

func TestDescribeIsSharedBetweenBareAndQualifiedReferences(t *testing.T) {
	// A read through a bare reference and one that names the provider's own
	// schema share one cache entry.
	reader := shopReader()
	provider := newShopProvider(reader, nil)
	bare := dal.NewRootCollectionRef("Order", "")
	_, err := provider.GetColumns(context.Background(), "shop", schemer.ColumnsFilter{CollectionRef: &bare})
	require.NoError(t, err)
	_, err = provider.GetForeignKeys(context.Background(), "public", "Order")
	require.NoError(t, err)
	assert.Equal(t, 1, reader.callCount("DescribeCollection"))
}

// flightCounter is a RecordsCounter whose every count is held in flight.
type flightCounter struct{ flight *flightRecorder }

func (c flightCounter) CountRecords(context.Context, string, string) (*int, error) {
	defer c.flight.hold()()
	return intPtr(1), nil
}

// manyTablesReader describes n tables of one column each, no keys.
func manyTablesReader(n int) *fakeReader {
	reader := &fakeReader{defs: map[string]*dbschema.CollectionDef{}}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("Table%03d", i)
		reader.collections = append(reader.collections, name)
		reader.defs[name] = &dbschema.CollectionDef{Name: name, Fields: []dbschema.FieldDef{{Name: "Id", Type: dbschema.Int}}, PrimaryKey: fields("Id")}
	}
	return reader
}

// The core scanner starts a goroutine per table for its description, its
// indexes and its count, and none of them is limited. Left alone, a schema of
// a hundred tables would open hundreds of connections to the scanned server at
// once, and PostgreSQL refuses clients past max_connections (100 by default).
func TestScanCatalog_BoundsTheReadsInFlight(t *testing.T) {
	const tables = 100
	flight := &flightRecorder{delay: 2 * time.Millisecond}
	reader := manyTablesReader(tables)
	reader.flight = flight

	catalog, err := schemer.NewScanner(NewSchemaProvider(reader, flightCounter{flight}, "shop", "public")).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)
	require.Len(t, catalog.Schemas, 1)
	assert.Len(t, catalog.Schemas[0].Tables, tables)
	for _, table := range catalog.Schemas[0].Tables {
		require.NotNil(t, table.RecordsCount, table.Name())
	}

	assert.Equal(t, tables, reader.callCount("DescribeCollection"))
	assert.Equal(t, tables, reader.callCount("ListIndexes"))
	assert.Equal(t, tables, reader.callCount("ListConstraints"))
	peak := flight.peakInFlight()
	assert.Equal(t, 4, maxConcurrentReads, "a handful of connections: a few reads in parallel, never one per table")
	assert.LessOrEqual(t, peak, maxConcurrentReads, "reads and counts together never exceed the bound")
	assert.Greater(t, peak, 1, "the bound limits the reads, it does not serialize them")
}

// The reader lists tables by the privileges of the role but reads foreign keys
// from the catalogs, which hold every key. A least-privilege role therefore
// meets a key to a table it was not told about, and the core scanner fails a
// whole scan on such a key.
func TestScanCatalog_LeavesOutAForeignKeyToATableTheReaderDidNotList(t *testing.T) {
	reader := shopReader()
	reader.defs["OrderLine"].ForeignKeys = append(reader.defs["OrderLine"].ForeignKeys, dbschema.ForeignKeyDef{
		Name: "fk_line_hidden", Fields: fields("Sku"), ReferencedCollection: "Hidden", ReferencedFields: fields("Sku"),
	})
	var logged strings.Builder
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })

	catalog, err := schemer.NewScanner(newShopProvider(reader, nil)).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)
	line := catalog.Schemas[0].Tables[2]
	require.Equal(t, "OrderLine", line.Name())
	require.Len(t, line.ForeignKeys, 1, "the key to the table that was not listed is left out")
	assert.Equal(t, "fk_line_order", line.ForeignKeys[0].Name)
	assert.Contains(t, logged.String(), "fk_line_hidden")
	assert.Contains(t, logged.String(), "Hidden")
	assert.Contains(t, logged.String(), "did not list")
}

func TestGetConstraints_ListsTheCollectionsItselfWhenNobodyDid(t *testing.T) {
	ctx := context.Background()
	reader := shopReader()
	reader.defs["Order"].ForeignKeys = []dbschema.ForeignKeyDef{
		{Name: "fk_hidden", Fields: fields("CustomerId"), ReferencedCollection: "Hidden", ReferencedFields: fields("Id")},
	}
	provider := newShopProvider(reader, nil)

	constraints, err := provider.GetConstraints(ctx, "shop", "public", "Order")
	require.NoError(t, err)
	_, err = constraints.NextConstraint()
	assert.ErrorIs(t, err, io.EOF, "the only key points at a table that is not listed")
	_, err = provider.GetConstraints(ctx, "shop", "public", "OrderLine")
	require.NoError(t, err)
	assert.Equal(t, 1, reader.callCount("ListCollections"), "the listing is read once and shared")

	reader = shopReader()
	reader.errs = map[string]error{"ListCollections": errors.New("boom")}
	_, err = newShopProvider(reader, nil).GetConstraints(ctx, "shop", "public", "Order")
	assert.ErrorContains(t, err, "list collections")
	assert.ErrorContains(t, err, "boom")
}

func TestLimited_StopsWaitingForASlotWhenTheContextEnds(t *testing.T) {
	slots := make(chan struct{}, 1)
	var observed []error
	observe := func(err error) { observed = append(observed, err) }
	slots <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	got, err := limited(ctx, slots, func() (int, error) { called = true; return 7, nil }, observe)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, got)
	assert.False(t, called, "nothing is read once the context has ended")
	assert.Len(t, slots, 1, "a call that never got a slot gives none back")
	assert.Equal(t, []error{context.Canceled}, observed, "the observer sees the interrupted read")

	<-slots
	got, err = limited(context.Background(), slots, func() (int, error) { return 7, nil }, observe)
	assert.NoError(t, err)
	assert.Equal(t, 7, got)
	assert.Empty(t, slots, "the slot is back after the call")
	assert.Len(t, observed, 1, "a successful read is not observed as a failure")

	boom := errors.New("boom")
	_, err = limited(context.Background(), slots, func() (int, error) { return 0, boom }, observe)
	assert.ErrorIs(t, err, boom)
	assert.Empty(t, slots, "a failed call gives its slot back too")
	assert.Equal(t, []error{context.Canceled, boom}, observed, "the observer gets the unflattened failure")
}

func TestProvider_AReadStopsWaitingWhenTheScanIsCancelled(t *testing.T) {
	reader := shopReader()
	provider := newShopProvider(reader, fakeCounter{}).(*provider)
	for range maxConcurrentReads {
		provider.slots <- struct{}{} // every slot is busy
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.GetCollections(ctx, nil)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = provider.RecordsCount(ctx, "shop", "public", "Customer")
	assert.ErrorIs(t, err, context.Canceled)
	_, err = provider.GetForeignKeys(ctx, "public", "Order")
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, reader.callCount("ListCollections")+reader.callCount("DescribeCollection"), "nothing reached the reader")
}
