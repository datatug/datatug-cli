package dalgoschema

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A column's default is the text of its expression, as the reader reports it: the PostgreSQL
// reader answers a DefaultLiteral whose value is the SQL text, and the provider keeps it as
// it is. An identity column has none (the reader marks it AutoIncrement), and a generated one
// is the text the reader built for it.
func TestGetColumns_RecordsTheDefaultOfEachColumnAsTheReaderReportsIt(t *testing.T) {
	literal := func(text string) dbschema.DefaultExpr { return dbschema.DefaultLiteral{Value: text} }
	reader := &multiSchemaReader{
		listed: []dal.CollectionRef{dal.NewRootCollectionRef("Order Items", "")},
		defs: map[string]*dbschema.CollectionDef{".Order Items": {Name: "Order Items", Fields: []dbschema.FieldDef{
			{Name: "Id", Type: dbschema.Int, AutoIncrement: true},
			{Name: "Status", Type: dbschema.String, Default: literal(`'new'::text`)},
			{Name: "Made", Type: dbschema.Time, Default: literal(`now()`)},
			{Name: "Hits", Type: dbschema.Int, Default: literal(`nextval('sales.hits_seq'::regclass)`)},
			{Name: "Qty", Type: dbschema.Int, Default: literal(`1`)},
			{Name: "Double", Type: dbschema.Int, Nullable: true, Default: literal(`GENERATED ALWAYS AS (("Qty" * 2))`)},
			{Name: "Note", Type: dbschema.String, Nullable: true},
			{Name: "Count", Type: dbschema.Int, Default: dbschema.DefaultLiteral{Value: 7}},
			{Name: "Flag", Type: dbschema.Bool, Default: dbschema.DefaultLiteral{Value: false}},
			{Name: "Nothing", Type: dbschema.String, Nullable: true, Default: dbschema.DefaultLiteral{}},
			{Name: "Stamp", Type: dbschema.Time, Default: dbschema.DefaultCurrentTimestamp{}},
			{Name: "Pointer", Type: dbschema.Int, Nullable: true, Default: &dbschema.DefaultLiteral{Value: 1}},
		}}},
	}
	ref := dal.NewRootCollectionRef("Order Items", "")

	columns, err := NewSchemaProvider(reader, nil, "shop", "").GetColumns(context.Background(), "shop", schemer.ColumnsFilter{CollectionRef: &ref})
	require.NoError(t, err)

	got := map[string]string{}
	for _, column := range columns {
		if column.Default != nil {
			got[column.Name] = *column.Default
		}
	}
	assert.Equal(t, map[string]string{
		"Status":  `'new'::text`,
		"Made":    `now()`,
		"Hits":    `nextval('sales.hits_seq'::regclass)`,
		"Qty":     `1`,
		"Double":  `GENERATED ALWAYS AS (("Qty" * 2))`,
		"Count":   `7`,
		"Flag":    `false`,
		"Nothing": `NULL`,
		"Stamp":   `CURRENT_TIMESTAMP`,
	}, got, "a column with no default, and an identity column, have none")
	assert.Nil(t, columns[0].Default, "an identity column has no default")
	assert.Nil(t, columns[6].Default, "a column with no default has none")
	assert.Nil(t, columns[11].Default, "a default of a kind the reader does not answer is not recorded as an empty one")
}
