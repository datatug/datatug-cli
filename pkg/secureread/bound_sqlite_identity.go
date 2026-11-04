package secureread

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/dalgo2sqlite"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// This is the native leaf, constructed before any DALgo policy layers. Every
// policy therefore still executes and may narrow the query before this check.
// It verifies source spelling/kind, not permissions or physical DDL lineage.
type boundSQLiteIdentityDB struct {
	dal.DB
	dbschema.SchemaReader
	raw     *sql.DB
	listing dbschema.SchemaReader
	ref     dbcopy.BackendRef
}

type boundIdentityNoDataKey struct{}

type boundIdentityRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// Private IO seam for native catalog interruption/failure controls.
var queryBoundIdentityRows = func(ctx context.Context, raw *sql.DB) (boundIdentityRows, error) {
	return raw.QueryContext(ctx, "SELECT name, type FROM pragma_table_list WHERE schema = 'main'")
}

func openBoundSQLiteIdentityDB(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, func(), error) {
	if ref.Scheme != "sqlite" || ref.Path == "" {
		return nil, nil, unsupportedBoundIdentity()
	}
	db, raw, closeDB, err := openReadOnlySQLiteDB(ctx, ref.Path)
	if err != nil {
		return nil, nil, ref.OpenFailure(err)
	}
	// NewSchemaReader only rejects nil; a successful open above owns a nonnil DB.
	schema, _ := dalgo2sqlite.NewSchemaReader(raw)
	listing, _ := dalgo2sqlite.NewLenientSchemaReader(raw)
	return &boundSQLiteIdentityDB{DB: db, SchemaReader: schema, listing: listing, raw: raw, ref: ref}, closeDB, nil
}

func (db *boundSQLiteIdentityDB) Get(context.Context, record.Record) error {
	return dal.ErrNotSupported
}
func (db *boundSQLiteIdentityDB) GetMulti(context.Context, []record.Record) error {
	return dal.ErrNotSupported
}
func (db *boundSQLiteIdentityDB) Exists(context.Context, *record.Key) (bool, error) {
	return false, dal.ErrNotSupported
}

// Transactions cannot bypass this leaf's source-identity check through an
// embedded adapter transaction handle. This profile supports direct reads only.
func (db *boundSQLiteIdentityDB) RunReadonlyTransaction(context.Context, dal.ROTxWorker, ...dal.TransactionOption) error {
	return dal.ErrNotSupported
}
func (db *boundSQLiteIdentityDB) RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error {
	return dal.ErrNotSupported
}

func (db *boundSQLiteIdentityDB) Close() error { return db.raw.Close() }

func (db *boundSQLiteIdentityDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	if err := db.verifyQuery(ctx, query); err != nil {
		return nil, err
	}
	if ctx.Value(boundIdentityNoDataKey{}) != nil {
		return nil, errAuthorized
	}
	return db.DB.ExecuteQueryToRecordsReader(ctx, query)
}
func (db *boundSQLiteIdentityDB) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	if err := db.verifyQuery(ctx, query); err != nil {
		return nil, err
	}
	if ctx.Value(boundIdentityNoDataKey{}) != nil {
		return nil, errAuthorized
	}
	return db.DB.ExecuteQueryToRecordsetReader(ctx, query, options...)
}

func unsupportedBoundIdentity() error {
	return fmt.Errorf("%w: configured source requires a canonical ordinary main-table identity: %w", ErrSourceBinding, dal.ErrNotSupported)
}

func (db *boundSQLiteIdentityDB) verifyQuery(ctx context.Context, query dal.Query) error {
	structured, ok := query.(dal.StructuredQuery)
	if !ok || dal.HasSubquery(structured) {
		return unsupportedBoundIdentity()
	}
	resources := access.ResourcesForQuery(query)
	for _, resource := range resources {
		if table, tabled := resource.Table(); tabled {
			if table.Database != "" || table.Schema != "main" {
				return unsupportedBoundIdentity()
			}
		} else if resource.Kind() != access.PathResource {
			return unsupportedBoundIdentity()
		}
	}
	// The same dedicated readonly connection supplies facts and executes data.
	// No views, virtual/shadow objects, attached namespaces or stored scan names
	// can stand in for an ordinary main table. Filesystem-owner DDL/ABA remains
	// outside this profile; this is not a stronger native-span certification.
	collections, err := db.listing.ListCollections(ctx, nil)
	if err != nil {
		return db.ref.OpenFailure(err)
	}
	listed := make(map[string]bool, len(collections))
	for _, collection := range collections {
		listed[collection.Name()] = true
	}
	rows, err := queryBoundIdentityRows(ctx, db.raw)
	if err != nil {
		return db.ref.OpenFailure(err)
	}
	defer func() { _ = rows.Close() }()
	names, paths := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return db.ref.OpenFailure(err)
		}
		if kind != "table" || !listed[name] {
			continue
		}
		names[name] = true
		// Resource.String is unambiguous for one root component without '/'.
		// A literal slash name may only use an explicit qualified table resource.
		if !strings.Contains(name, "/") {
			paths[access.CollectionResourceFor(nil, name).String()] = true
		}
	}
	if err := rows.Err(); err != nil {
		return db.ref.OpenFailure(err)
	}
	for _, resource := range resources {
		if table, tabled := resource.Table(); tabled {
			if !names[table.Name] {
				return unsupportedBoundIdentity()
			}
		} else if !paths[resource.String()] {
			return unsupportedBoundIdentity()
		}
	}
	return ctx.Err()
}
