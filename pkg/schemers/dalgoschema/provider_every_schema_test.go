package dalgoschema

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// everySchemaReader is a multiSchemaReader that also lists its schemas, and the collections
// and views of each by name, as the PostgreSQL reader does (it is a SchemaLister). It counts
// the calls of the listings, and can be made to fail any of them.
type everySchemaReader struct {
	*multiSchemaReader
	schemas []string
	errs    map[string]error // method name -> error

	mu    sync.Mutex
	calls map[string]int
	asked []string // "<method> <schema>" of each schema listing
}

var _ SchemaLister = (*everySchemaReader)(nil)

func (e *everySchemaReader) note(method, schema string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.calls == nil {
		e.calls = map[string]int{}
	}
	e.calls[method]++
	e.asked = append(e.asked, method+" "+schema)
	return e.errs[method]
}

// ListCollections and ListViews are the reader's own for its configured schema. A provider
// that can read every schema must not call them: that is only the schema public.
func (e *everySchemaReader) ListCollections(ctx context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	_ = e.note("ListCollections", "")
	return e.multiSchemaReader.ListCollections(ctx, parent)
}

func (e *everySchemaReader) ListViews(ctx context.Context) ([]dal.CollectionRef, error) {
	_ = e.note("ListViews", "")
	return e.multiSchemaReader.ListViews(ctx)
}

func (e *everySchemaReader) ListSchemas(context.Context) ([]string, error) {
	if err := e.note("ListSchemas", ""); err != nil {
		return nil, err
	}
	return e.schemas, nil
}

func (e *everySchemaReader) ListSchemaCollections(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	if err := e.note("ListSchemaCollections", schema); err != nil {
		return nil, err
	}
	return inSchema(e.listed, schema), nil
}

func (e *everySchemaReader) ListSchemaViews(_ context.Context, schema string) ([]dal.CollectionRef, error) {
	if err := e.note("ListSchemaViews", schema); err != nil {
		return nil, err
	}
	return inSchema(e.views, schema), nil
}

func inSchema(refs []dal.CollectionRef, schema string) []dal.CollectionRef {
	var out []dal.CollectionRef
	for _, ref := range refs {
		if ref.Schema() == schema {
			out = append(out, ref)
		}
	}
	return out
}

func (e *everySchemaReader) count(method string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[method]
}

// newEverySchemaReader is shopAndSalesReader with a third schema that has nothing in it, and
// the schemas listed as the server lists them: in name order.
func newEverySchemaReader() *everySchemaReader {
	return &everySchemaReader{multiSchemaReader: shopAndSalesReader(), schemas: []string{"empty", "public", "sales"}}
}

// A reader that can list its schemas is read in every one of them: the collections and the
// views of each come from the reader's own listing of that schema, the reader's configured
// schema is not asked for, and the schemas are listed once.
func TestScanCatalog_ReadsEverySchemaTheReaderLists(t *testing.T) {
	reader := newEverySchemaReader()

	catalog, err := schemer.NewScanner(NewSchemaProvider(reader, nil, "shop", "public")).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)

	require.Len(t, catalog.Schemas, 2, "a schema with nothing in it has no collection to put in the scan")
	names := func(items []*datatug.CollectionInfo) (out []string) {
		for _, item := range items {
			out = append(out, item.Name())
		}
		return out
	}
	public, sales := catalog.Schemas.GetByID("public"), catalog.Schemas.GetByID("sales")
	require.NotNil(t, public)
	require.NotNil(t, sales)
	assert.ElementsMatch(t, []string{"Customer", "Region"}, names(public.Tables))
	assert.Empty(t, public.Views)
	assert.ElementsMatch(t, []string{"Customer", "Invoice"}, names(sales.Tables))
	assert.Equal(t, []string{"OpenInvoices"}, names(sales.Views))

	assert.Equal(t, 1, reader.count("ListSchemas"), "the schemas are listed once, for the collections and for the views")
	assert.Equal(t, 0, reader.count("ListCollections"), "the reader's own schema is not asked for when every schema is")
	assert.Equal(t, 0, reader.count("ListViews"))
	assert.ElementsMatch(t, []string{
		"ListSchemas ",
		"ListSchemaCollections empty", "ListSchemaCollections public", "ListSchemaCollections sales",
		"ListSchemaViews empty", "ListSchemaViews public", "ListSchemaViews sales",
	}, reader.asked)
	assert.ElementsMatch(t, []string{"public.Customer", "public.Region", "sales.Customer", "sales.Invoice", "sales.OpenInvoices"}, reader.described)
}

// A table of the schema public is read as it is when the reader lists only that schema: the
// same collections, in the same schema, under the same names.
func TestGetCollections_TheSchemaPublicIsTheSameWhetherOrNotEverySchemaIsRead(t *testing.T) {
	read := func(reader dbschema.SchemaReader) (out []string) {
		collections, err := NewSchemaProvider(reader, nil, "shop", "public").GetCollections(context.Background(), nil)
		require.NoError(t, err)
		for {
			collection, err := collections.NextCollection()
			if err != nil {
				return out
			}
			out = append(out, collection.Schema()+"."+collection.Name()+" "+collection.DbType)
		}
	}
	onlyPublic := &multiSchemaReader{
		listed: []dal.CollectionRef{dal.NewRootCollectionRef("Customer", ""), dal.NewRootCollectionRef("Report", "")},
		views:  []dal.CollectionRef{dal.NewRootCollectionRef("Report", "")},
	}
	everything := &everySchemaReader{
		multiSchemaReader: &multiSchemaReader{
			listed: []dal.CollectionRef{dal.NewQualifiedRootCollectionRef("public", "Customer", ""), dal.NewQualifiedRootCollectionRef("public", "Report", "")},
			views:  []dal.CollectionRef{dal.NewQualifiedRootCollectionRef("public", "Report", "")},
		},
		schemas: []string{"public"},
	}

	assert.Equal(t, []string{"public.Customer BASE TABLE", "public.Report VIEW"}, read(onlyPublic))
	assert.Equal(t, read(onlyPublic), read(everything))
}

// A failure to list the schemas, or the collections or views of one, is the scan's failure,
// and names what was being listed.
func TestGetCollections_FailsWhenASchemaListingFails(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   string
	}{
		{"ListSchemas", "list schemas"},
		{"ListSchemaCollections", `list collections of schema "empty"`},
		{"ListSchemaViews", `list views of schema "empty"`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			reader := newEverySchemaReader()
			boom := errors.New("boom")
			reader.errs = map[string]error{tc.method: boom}

			_, err := NewSchemaProvider(reader, nil, "shop", "public").GetCollections(context.Background(), nil)

			require.ErrorIs(t, err, boom)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

// A foreign key is decided against what the reader listed in the schema of the table that
// has it, now that every schema is listed: a key to a table of the same schema is kept, a key
// into another schema is left out as before.
func TestGetConstraints_ChecksAKeyAgainstTheListingOfItsOwnSchema(t *testing.T) {
	reader := newEverySchemaReader()
	provider := NewSchemaProvider(reader, nil, "shop", "public")
	_, err := provider.GetCollections(context.Background(), nil)
	require.NoError(t, err)

	constraints, err := provider.GetConstraints(context.Background(), "shop", "sales", "Invoice")
	require.NoError(t, err)
	var kept []string
	for {
		constraint, err := constraints.NextConstraint()
		if err != nil {
			break
		}
		kept = append(kept, constraint.Name+" -> "+constraint.RefTableSchema+"."+constraint.RefTableName)
	}
	assert.Equal(t, []string{"fk_invoice_customer -> sales.Customer"}, kept, "the Region that only schema public has is not in the schema of the table")
}

// A reader that can list its schemas is asked for a schema that is not the provider's own only
// by name, and a reader that cannot is read as before.
func TestScanCatalog_AReaderThatCannotListItsSchemasIsReadInItsOwn(t *testing.T) {
	reader := shopAndSalesReader()

	_, ok := any(reader).(SchemaLister)
	assert.False(t, ok, "the reader under test lists no schema")
	catalog, err := schemer.NewScanner(NewSchemaProvider(reader, nil, "shop", "public")).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)
	assert.Len(t, catalog.Schemas, 2, "the references name their schemas, as before")
}
