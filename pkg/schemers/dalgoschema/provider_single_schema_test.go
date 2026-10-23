package dalgoschema

import (
	"context"
	"testing"

	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A reader of one schema is asked about its tables by unqualified references: the schema the scan reports them
// under ("default" for MySQL, Oracle and SQL Server) is a name of DataTug's, which the server does not have.
func TestScanCatalog_SingleSchemaReaderGetsUnqualifiedReferences(t *testing.T) {
	reader := shopReader()
	catalog, err := schemer.NewScanner(NewSchemaProvider(reader, nil, "shop", "default")).ScanCatalog(context.Background(), "shop")
	require.NoError(t, err)
	require.Len(t, catalog.Schemas, 1)
	assert.Equal(t, "default", catalog.Schemas[0].ID)
	require.Len(t, catalog.Schemas[0].Tables, 3)
	require.NotEmpty(t, reader.refs)
	for _, ref := range reader.refs {
		assert.Empty(t, ref.Schema(), "the reference to %s names no schema", ref.Name())
	}
}

// A reader of several schemas is asked about a table of its first schema by a qualified reference, even when that
// schema is the provider's own.
func TestProviderRef_QualifiesForAReaderOfSeveralSchemas(t *testing.T) {
	p := NewSchemaProvider(newEverySchemaReader(), nil, "shop", "public").(*provider)
	assert.Equal(t, "public", p.ref("public", "Customer").Schema())
	assert.Equal(t, "sales", p.ref("sales", "Order").Schema())
	single := NewSchemaProvider(shopReader(), nil, "shop", "default").(*provider)
	assert.Empty(t, single.ref("default", "Customer").Schema())
	assert.Equal(t, "other", single.ref("other", "Customer").Schema())
	assert.Empty(t, single.ref("", "Customer").Schema())
}
