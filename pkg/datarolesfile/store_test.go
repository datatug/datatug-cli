package datarolesfile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

var at = time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)

func suggestion(collection string) dataroles.Classification {
	return dataroles.Classification{Target: dataroles.Target{Family: "factory", Collection: collection}, Role: dataroles.RoleReference,
		Origin: dataroles.OriginHeuristic, State: dataroles.StateSuggested, At: at}
}

func TestStoreUpdateAndLoad(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	doc, err := store.Load()
	require.NoError(t, err)
	require.Equal(t, dataroles.Format, doc.Format)
	require.Empty(t, doc.Classifications)

	doc, err = store.Update(context.Background(), func(doc *dataroles.Document) error {
		doc.Merge([]dataroles.Classification{suggestion("countries")})
		return nil
	})
	require.NoError(t, err)
	require.Len(t, doc.Classifications, 1)
	loaded, err := store.Load()
	require.NoError(t, err)
	require.Equal(t, doc, loaded)
	require.FileExists(t, filepath.Join(store.Dir, dataroles.FileName))

	require.NoError(t, os.WriteFile(filepath.Join(store.Dir, dataroles.FileName), []byte(`{"format":"`+dataroles.Format+`"}`), 0o644))
	loaded, err = store.Load()
	require.NoError(t, err)
	require.NotNil(t, loaded.Classifications)
}

func TestStoreRefusals(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	ctx := context.Background()
	boom := errors.New("boom")
	_, err := store.Update(ctx, func(*dataroles.Document) error { return boom })
	require.ErrorIs(t, err, boom)
	_, err = store.Update(ctx, func(doc *dataroles.Document) error {
		doc.Classifications = append(doc.Classifications, dataroles.Classification{})
		return nil
	})
	require.Error(t, err, "an invalid document is never written")

	path := filepath.Join(store.Dir, dataroles.FileName)
	require.NoError(t, os.WriteFile(path, []byte("{"), 0o644))
	_, err = store.Load()
	require.Error(t, err)
	_, err = store.Update(ctx, func(*dataroles.Document) error { return nil })
	require.Error(t, err)
	require.NoError(t, os.WriteFile(path, []byte(`{"format":"other"}`), 0o644))
	_, err = store.Load()
	require.ErrorContains(t, err, "format")
	require.NoError(t, os.Remove(path))

	restore := func() {
		readFile, writeFile, rename = os.ReadFile, os.WriteFile, os.Rename
		tryLock = func(l *flock.Flock, ctx context.Context) (bool, error) {
			return l.TryLockContext(ctx, 20*time.Millisecond)
		}
	}
	t.Cleanup(restore)
	readFile = func(string) ([]byte, error) { return nil, boom }
	_, err = store.Load()
	require.ErrorIs(t, err, boom)
	restore()
	tryLock = func(*flock.Flock, context.Context) (bool, error) { return false, nil }
	_, err = store.Update(ctx, func(*dataroles.Document) error { return nil })
	require.ErrorContains(t, err, "lock")
	restore()
	writeFile = func(string, []byte, fs.FileMode) error { return boom }
	_, err = store.Update(ctx, func(*dataroles.Document) error { return nil })
	require.ErrorIs(t, err, boom)
	restore()
	rename = func(string, string) error { return boom }
	_, err = store.Update(ctx, func(*dataroles.Document) error { return nil })
	require.ErrorIs(t, err, boom)
	restore()
	previousMarshal := marshal
	t.Cleanup(func() { marshal = previousMarshal })
	marshal = func(any) ([]byte, error) { return nil, boom }
	_, err = store.Update(ctx, func(*dataroles.Document) error { return nil })
	require.ErrorIs(t, err, boom)
}

func TestCollections(t *testing.T) {
	key := func(name string) datatug.DBCollectionKey { return datatug.NewTableKey(name, "main", "", nil) }
	schema := &api.CatalogSchema{Relations: []api.CatalogRelation{
		{Schema: "main", Name: "products", DbType: "BASE TABLE", Columns: []api.CatalogColumn{{Name: "id", DbType: "INTEGER", PrimaryKeyPosition: 1}, {Name: "sku", DbType: "TEXT"}}},
		{Schema: "main", Name: "orders", DbType: "BASE TABLE", Columns: []api.CatalogColumn{{Name: "id", PrimaryKeyPosition: 1}, {Name: "product_sku"}}},
		{Schema: "main", Name: "product_view", DbType: "VIEW"},
	}}
	collections := Collections(schema,
		[]datatug.TableForeignKeys{{Table: key("orders"), ForeignKeys: datatug.ForeignKeys{{Name: "fk", Columns: []string{"product_sku"}, RefTable: key("products")}}}},
		[]datatug.TableIndexes{{Table: key("products"), Indexes: []*datatug.Index{
			nil,
			{Name: "u", IsUnique: true, Columns: []*datatug.IndexColumn{{Name: "sku"}}},
			{Name: "plain", Columns: []*datatug.IndexColumn{{Name: "id"}}},
			{Name: "pair", IsUnique: true, Columns: []*datatug.IndexColumn{{Name: "id"}, {Name: "sku"}}},
		}}})
	require.Equal(t, []dataroles.Collection{
		{Name: "orders", Columns: []dataroles.Column{{Name: "id", PrimaryKey: true}, {Name: "product_sku"}}, References: 1},
		{Name: "products", Columns: []dataroles.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "sku", Type: "TEXT", Unique: true}}, ReferencedBy: 1},
	}, collections)
}
