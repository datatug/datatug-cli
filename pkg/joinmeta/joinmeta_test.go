package joinmeta

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/dtql"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func openSQLite(t *testing.T, path string) dal.DB {
	t.Helper()
	ref, err := dbcopy.Parse("sqlite://" + path)
	require.NoError(t, err)
	db, err := ref.OpenProtected(context.Background())
	require.NoError(t, err)
	return db
}

func chinook(t *testing.T) dal.DB {
	t.Helper()
	path, err := filepath.Abs("../dbcopy/testdata/chinook.db")
	require.NoError(t, err)
	return openSQLite(t, path)
}

func TestLoadForeignKeySnapshotOnChinook(t *testing.T) {
	snapshot, err := LoadForeignKeySnapshot(context.Background(), chinook(t), "chinook")
	require.NoError(t, err)
	assert.Equal(t, "chinook", snapshot.Source)
	assert.Equal(t, []string{"InvoiceLineId", "InvoiceId", "TrackId", "UnitPrice", "Quantity"}, snapshot.Columns["main.invoiceline"])
	var invoiceToCustomer *ForeignKey
	for i, key := range snapshot.Keys {
		if key.FromRelation == "Invoice" && key.ToRelation == "Customer" {
			invoiceToCustomer = &snapshot.Keys[i]
		}
	}
	require.NotNil(t, invoiceToCustomer)
	assert.Equal(t, []string{"CustomerId"}, invoiceToCustomer.FromFields)
	assert.Equal(t, []string{"CustomerId"}, invoiceToCustomer.ToFields)
	assert.NotEmpty(t, invoiceToCustomer.ConstraintID)
	assert.Equal(t, "table", snapshot.Kind("", "Invoice"))
	assert.Equal(t, "unknown", snapshot.Kind("", "Nope"))
	assert.Equal(t, "main.invoice", RelationKey("", "Invoice"))
	assert.Equal(t, "public.invoice", RelationKey("public", "Invoice"))
}

func TestLoadForeignKeySnapshotViewsAndImplicitTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.db")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE Parent (a INTEGER, b INTEGER, PRIMARY KEY (a, b));
CREATE TABLE Child (id INTEGER PRIMARY KEY, pa INTEGER, pb INTEGER, FOREIGN KEY (pa, pb) REFERENCES Parent);
CREATE VIEW ParentView AS SELECT a FROM Parent;`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	snapshot, err := LoadForeignKeySnapshot(context.Background(), openSQLite(t, path), "v")
	require.NoError(t, err)
	assert.Equal(t, "view", snapshot.Kind("", "ParentView"))
	require.Len(t, snapshot.Keys, 1)
	assert.Equal(t, []string{"pa", "pb"}, snapshot.Keys[0].FromFields)
	assert.Equal(t, []string{"a", "b"}, snapshot.Keys[0].ToFields)
}

func TestLoadForeignKeySnapshotFailures(t *testing.T) {
	ctx := context.Background()
	_, err := LoadForeignKeySnapshot(ctx, nil, "x")
	assert.Error(t, err)
	db := chinook(t)
	_, err = LoadForeignKeySnapshot(ctx, db, "")
	assert.Error(t, err)

	boom := errors.New("boom")
	swap := func(list func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error), describe func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error)) {
		listCollections, describeCollection = list, describe
		t.Cleanup(func() { listCollections, describeCollection = dbschema.ListCollections, dbschema.DescribeCollection })
	}
	swap(func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) { return nil, boom }, dbschema.DescribeCollection)
	_, err = LoadForeignKeySnapshot(ctx, db, "x")
	assert.ErrorIs(t, err, boom)

	many := make([]dal.CollectionRef, MaxCollections+1)
	swap(func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) { return many, nil }, dbschema.DescribeCollection)
	_, err = LoadForeignKeySnapshot(ctx, db, "x")
	assert.ErrorIs(t, err, ErrTooManyCollections)

	two := []dal.CollectionRef{dal.NewRootCollectionRef("B", ""), dal.NewRootCollectionRef("A", "")}
	swap(func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) { return two, nil },
		func(context.Context, dal.DB, *dal.CollectionRef) (*dbschema.CollectionDef, error) { return nil, boom })
	_, err = LoadForeignKeySnapshot(ctx, db, "x")
	assert.ErrorIs(t, err, boom)

	// an unnamed key gets a stable id, and a key whose target has no matching
	// primary key is refused rather than guessed
	defs := map[string]*dbschema.CollectionDef{
		"A": {Name: "A", Fields: []dbschema.FieldDef{{Name: "id"}}, PrimaryKey: []dal.FieldName{"id"}},
		"B": {Name: "B", Fields: []dbschema.FieldDef{{Name: "a_id"}}, ForeignKeys: []dbschema.ForeignKeyDef{{Fields: []dal.FieldName{"a_id"}, ReferencedCollection: "A"}}},
	}
	swap(func(context.Context, dal.DB, *record.Key) ([]dal.CollectionRef, error) { return two, nil },
		func(_ context.Context, _ dal.DB, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
			return defs[ref.Name()], nil
		})
	snapshot, err := LoadForeignKeySnapshot(ctx, db, "x")
	require.NoError(t, err)
	assert.Equal(t, ForeignKey{ConstraintID: "sqlite:B:fk0", Schema: "main", FromRelation: "B", FromFields: []string{"a_id"}, ToSchema: "main", ToRelation: "A", ToFields: []string{"id"}}, snapshot.Keys[0])
	snapshot, err = LoadForeignKeySnapshot(ctx, otherEngine{db}, "x")
	require.NoError(t, err)
	assert.Equal(t, ForeignKey{ConstraintID: "B:fk:0", FromRelation: "B", FromFields: []string{"a_id"}, ToRelation: "A", ToFields: []string{"id"}}, snapshot.Keys[0])
	defs["B"].ForeignKeys[0].ReferencedCollection = "Missing"
	_, err = LoadForeignKeySnapshot(ctx, db, "x")
	assert.ErrorContains(t, err, "no resolvable target primary key")
}

// otherEngine is a database whose adapter is not SQLite's.
type otherEngine struct{ dal.DB }

func (otherEngine) Adapter() dal.Adapter { return dal.NewAdapter("other", "0") }

type noAdapter struct{ dal.DB }

func (noAdapter) Adapter() dal.Adapter { return nil }

// A SQLite key keeps the ID of the PRAGMA reader that chat sessions stored; a
// constraint name of another engine is qualified by its table, as PostgreSQL
// only keeps names unique per table.
func TestConstraintID(t *testing.T) {
	assert.Equal(t, "sqlite:Invoice:0", constraintID(true, "main", "Invoice", "Invoice_fk_0", 3))
	assert.Equal(t, "sqlite:Invoice:fk3", constraintID(true, "main", "Invoice", "Invoice_fk_x", 3))
	assert.Equal(t, "sqlite:Invoice:fk3", constraintID(true, "main", "Invoice", "Invoice_fk_", 3))
	assert.Equal(t, "public.orders:fk_customer", constraintID(false, "public", "orders", "fk_customer", 0))
	assert.NotEqual(t, constraintID(false, "public", "orders", "fk_customer", 0), constraintID(false, "public", "invoices", "fk_customer", 0))
	assert.Equal(t, "orders:fk:2", constraintID(false, "", "orders", "", 2))
	assert.False(t, isSQLite(noAdapter{chinook(t)}))
	assert.True(t, isSQLite(chinook(t)))
}

func TestRelationInstances(t *testing.T) {
	assert.Nil(t, RelationInstances(nil))
	query, err := dtql.Deserialize([]byte(`from:
  database: orders
  schema: s
  name: Invoice
  alias: i
  joins:
    - from: {database: people, name: Customer, alias: c}
      on: [{left: {field: CustomerId, source: i}, op: '==', right: {field: CustomerId, source: c}}]
`))
	require.NoError(t, err)
	assert.Equal(t, []RelationInstance{
		{ID: "root", Schema: "s", Relation: "Invoice", Alias: "i", Database: "orders"},
		{ID: "root/0", Relation: "Customer", Alias: "c", Database: "people"},
	}, RelationInstances(query.From()))
}

func TestRelationInstancesOfAJoinedCollection(t *testing.T) {
	from := dal.From(dal.NewRootCollectionRef("A", "a")).Join(dal.NewJoinedSource(dal.NewRootCollectionRef("B", "b"), dal.JoinInner,
		dal.NewComparison(dal.NewFieldRef("a", "id"), dal.Equal, dal.NewFieldRef("b", "aid"))))
	assert.Equal(t, []RelationInstance{{ID: "root", Relation: "A", Alias: "a"}, {ID: "root/0", Relation: "B", Alias: "b"}}, RelationInstances(from))
}
