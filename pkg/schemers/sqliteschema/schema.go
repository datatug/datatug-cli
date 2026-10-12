package sqliteschema

import (
	"context"
	"database/sql"
	"io"

	"github.com/dal-go/record"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/schemer"
)

// NewSchemaProvider creates a new SchemaProvider for SQLite. It says nothing of
// what it leaves out of a catalog: see NewSchemaProviderWithWarnings.
func NewSchemaProvider(getSqliteDB func() (*sql.DB, error)) schemer.SchemaProvider {
	return NewSchemaProviderWithWarnings(getSqliteDB, nil)
}

// NewSchemaProviderWithWarnings creates a new SchemaProvider for SQLite that names,
// on warnings (one line each, nil says nothing), what it leaves out of the catalog
// and why, so that the scan of a file with a mistake in it reads the rest: a table or
// view with no name, a view whose definition does not work (it names a table that was
// dropped), and a foreign key to a table that is not in the file.
func NewSchemaProviderWithWarnings(getSqliteDB func() (*sql.DB, error), warnings io.Writer) schemer.SchemaProvider {
	if getSqliteDB == nil {
		panic("getSqliteDB cannot be nil")
	}
	return schemaProvider{
		getSqliteDB: getSqliteDB,
		columnsProvider: columnsProvider{
			getSqliteDB: getSqliteDB,
		},
		warnings: warnings,
	}
}

var _ schemer.SchemaProvider = (*schemaProvider)(nil)

type schemaProvider struct {
	columnsProvider
	getSqliteDB func() (*sql.DB, error)
	warnings    io.Writer // nil says nothing
}

func (s schemaProvider) GetCollections(_ context.Context, parent *record.Key) (schemer.CollectionsReader, error) {
	_ = parent
	db, err := s.getSqliteDB()
	if err != nil {
		return nil, err
	}
	reader, err := getCollections(db, collectionsFilter{warnings: s.warnings})
	if err != nil {
		return nil, err
	}
	// The collections are read to the end before any view is asked for its columns:
	// the rows of the first query are closed by then, so the second kind of query
	// never has to wait for a connection that the first one holds.
	var collections []*datatug.CollectionInfo
	for {
		collection, err := reader.NextCollection()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		collections = append(collections, collection)
	}
	return &sliceCollectionsReader{collections: s.withoutBrokenViews(db, collections)}, nil
}

// withoutBrokenViews is collections without the views whose columns SQLite cannot
// tell: a view that names a table, or a column, that is not there any more is legal
// in the file, and fails PRAGMA table_info, which would fail the scan of the whole
// file. Each one left out is named.
func (s schemaProvider) withoutBrokenViews(db *sql.DB, collections []*datatug.CollectionInfo) []*datatug.CollectionInfo {
	kept := collections[:0:0]
	for _, collection := range collections {
		if collection.DbType == "VIEW" && !s.viewHasColumns(db, collection.Name()) {
			continue
		}
		kept = append(kept, collection)
	}
	return kept
}

// viewHasColumns is whether SQLite can tell the columns of the view, and names the
// view, with the reason, when it cannot.
func (s schemaProvider) viewHasColumns(db *sql.DB, view string) bool {
	rows, err := db.Query(pragmaSQL("table_info", view))
	if err != nil {
		warnf(s.warnings, "warning: view %q of schema %q is left out of the project: its definition cannot be read: %q\n", view, mainSchema, err.Error())
		return false
	}
	_ = rows.Close()
	return true
}

// sliceCollectionsReader reads collections that are already read.
type sliceCollectionsReader struct {
	collections []*datatug.CollectionInfo
	i           int
}

func (r *sliceCollectionsReader) NextCollection() (*datatug.CollectionInfo, error) {
	if r.i >= len(r.collections) {
		return nil, io.EOF
	}
	collection := r.collections[r.i]
	r.i++
	return collection, nil
}

func (schemaProvider) IsBulkProvider() bool {
	// SQLite has no INFORMATION_SCHEMA; metadata is read per-table via PRAGMA,
	// so use the scanner's per-table (non-bulk) path.
	return false
}
