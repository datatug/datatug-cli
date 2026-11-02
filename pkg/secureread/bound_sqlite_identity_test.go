package secureread

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

type identityCountingDB struct {
	dal.DB
	records, recordsets int
}

func (db *identityCountingDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	db.records++
	return db.DB.ExecuteQueryToRecordsReader(ctx, query)
}
func (db *identityCountingDB) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	db.recordsets++
	return db.DB.ExecuteQueryToRecordsetReader(ctx, query, options...)
}

func identityTestLeaf(t *testing.T) (*boundSQLiteIdentityDB, *identityCountingDB) {
	t.Helper()
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	native, err := sql.Open("sqlite", f.target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = native.Exec(`CREATE TABLE "slash/name"(id INTEGER PRIMARY KEY); INSERT INTO "slash/name" VALUES(1); CREATE TABLE ""(id INTEGER); CREATE TABLE auto_ids(id INTEGER PRIMARY KEY AUTOINCREMENT); CREATE VIEW exposed_view AS SELECT * FROM B; CREATE VIRTUAL TABLE search_data USING fts5(value)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	db, closeDB, err := openBoundSQLiteIdentityDB(context.Background(), dbcopy.BackendRef{Scheme: "sqlite", Path: f.target})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeDB)
	leaf := db.(*boundSQLiteIdentityDB)
	counter := &identityCountingDB{DB: leaf.DB}
	leaf.DB = counter
	return leaf, counter
}

func identityQuery(name string) dal.StructuredQuery {
	return dal.From(dal.NewRootCollectionRef(name, "")).NewQuery().SelectIntoRecord(nil)
}

func TestBoundNativeIdentityExactOrdinarySourcesAndNoDataAdmission(t *testing.T) {
	leaf, calls := identityTestLeaf(t)
	ctx := context.Background()
	for _, query := range []dal.Query{identityQuery("A"), dal.From(dal.NewQualifiedRootCollectionRef("main", "A", "a")).NewQuery().SelectIntoRecord(nil), dal.From(dal.NewQualifiedRootCollectionRef("main", "slash/name", "s")).NewQuery().SelectIntoRecord(nil)} {
		reader, err := leaf.ExecuteQueryToRecordsReader(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		rows, _, err := collectRows(reader)
		if err != nil || len(rows) != 1 {
			t.Fatalf("ordinary exact source: %+v %v", rows, err)
		}
	}
	if calls.records != 3 {
		t.Fatal("native positive did not dispatch")
	}
	for _, name := range []string{"a", "missing", "exposed_view", "search_data", "search_data_data", "slash/name", "sqlite_schema", "sqlite_master", "sqlite_sequence"} {
		if reader, err := leaf.ExecuteQueryToRecordsReader(ctx, identityQuery(name)); reader != nil || !errors.Is(err, dal.ErrNotSupported) || calls.records != 3 {
			t.Fatalf("unverified %q reached native data: %v calls=%d", name, err, calls.records)
		}
	}
	for _, query := range []dal.Query{
		dal.NewTextQuery("SELECT * FROM A", nil),
		dal.From(dal.NewQualifiedRootCollectionRef("temp", "A", "")).NewQuery().SelectIntoRecord(nil),
		dal.From(dal.NewQualifiedRootCollectionRef("main", "a", "")).NewQuery().SelectIntoRecord(nil),
		dal.From(dal.NewDatabaseCollectionRef("other", "main", "A", "")).NewQuery().SelectIntoRecord(nil),
		dal.From(dal.NewCollectionGroupRef("A", "")).NewQuery().SelectIntoRecord(nil),
		dal.From(dal.NewQuerySource(identityQuery("A"), "nested")).NewQuery().SelectIntoRecord(nil),
		dal.From(dal.NewRootCollectionRef("A", "")).NewQuery().SelectColumns(dal.Column{Expression: dal.NewAggregate("UNKNOWN", false, dal.NewFieldRef("", "id"))}),
	} {
		if _, err := leaf.ExecuteQueryToRecordsReader(ctx, query); !errors.Is(err, dal.ErrNotSupported) || calls.records != 3 {
			t.Fatalf("unsupported source shape dispatched: %v calls=%d", err, calls.records)
		}
	}
	before := calls.records
	noData := context.WithValue(ctx, boundIdentityNoDataKey{}, true)
	if reader, err := leaf.ExecuteQueryToRecordsReader(noData, identityQuery("A")); reader != nil || !errors.Is(err, errAuthorized) || calls.records != before {
		t.Fatalf("metadata admission executed data: %v calls=%d", err, calls.records)
	}
	if reader, err := leaf.ExecuteQueryToRecordsetReader(noData, identityQuery("A")); reader != nil || !errors.Is(err, errAuthorized) || calls.recordsets != 0 {
		t.Fatalf("recordset admission executed data: %v calls=%d", err, calls.recordsets)
	}
	if reader, err := leaf.ExecuteQueryToRecordsetReader(ctx, identityQuery("a")); reader != nil || !errors.Is(err, dal.ErrNotSupported) || calls.recordsets != 0 {
		t.Fatalf("recordset alias dispatched: %v", err)
	}
	reader, err := leaf.ExecuteQueryToRecordsetReader(ctx, identityQuery("A"))
	if err != nil || reader == nil || calls.recordsets != 1 {
		t.Fatalf("exact recordset path: %v calls=%d", err, calls.recordsets)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := leaf.Get(ctx, record.NewRecord(record.NewKeyWithID("A", 1))); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal(err)
	}
	if err := leaf.GetMulti(ctx, []record.Record{record.NewRecord(record.NewKeyWithID("A", 1))}); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal(err)
	}
	if exists, err := leaf.Exists(ctx, record.NewKeyWithID("A", 1)); exists || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal(exists, err)
	}
	if err := leaf.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, identityQuery("A")); err == nil {
		t.Fatal("closed native catalog produced an identity")
	}
}

func TestBoundIdentityKeepsProviderAndContextPoliciesAboveNativeLeaf(t *testing.T) {
	leaf, calls := identityTestLeaf(t)
	options := accesspolicies.Options{Principal: &access.Principal{ID: "operator"}, Unrestricted: true}
	deny := access.MustPolicy("mandatory", access.Root(access.Deny(access.Query)))
	providerCalls := 0
	secured, err := access.SecureDB(leaf, access.WithDatabasePolicyProvider(func(context.Context) ([]access.Policy, error) { providerCalls++; return []access.Policy{deny}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accesspolicies.Run(context.Background(), secured, identityQuery("A"), options); err == nil || calls.records != 0 || providerCalls != 1 {
		t.Fatalf("database provider denial hidden: %v calls=%d provider=%d", err, calls.records, providerCalls)
	}
	ctx := access.WithPolicy(context.Background(), deny)
	if _, err := accesspolicies.Run(ctx, leaf, identityQuery("A"), options); err == nil || calls.records != 0 {
		t.Fatalf("context denial hidden: %v calls=%d", err, calls.records)
	}
	result, err := accesspolicies.Run(context.Background(), leaf, identityQuery("A"), options)
	if err != nil || calls.records != 1 {
		t.Fatalf("ordinary positive did not survive shared policies: %v calls=%d", err, calls.records)
	}
	if _, _, err := collectRows(result.Reader); err != nil {
		t.Fatal(err)
	}
}

type identityFaultRows struct {
	scan, iteration error
	read            bool
}

func (r *identityFaultRows) Next() bool {
	if r.read {
		return false
	}
	r.read = true
	return r.scan != nil
}
func (r *identityFaultRows) Scan(...any) error { return r.scan }
func (r *identityFaultRows) Err() error        { return r.iteration }
func (*identityFaultRows) Close() error        { return nil }

type cancelIdentityRows struct {
	boundIdentityRows
	cancel context.CancelFunc
}

func (r cancelIdentityRows) Err() error { err := r.boundIdentityRows.Err(); r.cancel(); return err }

func TestBoundIdentityCatalogFailuresAndCancellationNeverDispatch(t *testing.T) {
	leaf, calls := identityTestLeaf(t)
	original := queryBoundIdentityRows
	t.Cleanup(func() { queryBoundIdentityRows = original })
	fault := errors.New("owned catalog fault")
	queryBoundIdentityRows = func(context.Context, *sql.DB) (boundIdentityRows, error) { return nil, fault }
	if _, err := leaf.ExecuteQueryToRecordsReader(context.Background(), identityQuery("A")); !errors.Is(err, fault) || calls.records != 0 {
		t.Fatalf("catalog query fault dispatched data: %v calls=%d", err, calls.records)
	}
	for _, rows := range []*identityFaultRows{{scan: fault}, {iteration: fault}} {
		queryBoundIdentityRows = func(context.Context, *sql.DB) (boundIdentityRows, error) { return rows, nil }
		if _, err := leaf.ExecuteQueryToRecordsReader(context.Background(), identityQuery("A")); !errors.Is(err, fault) || calls.records != 0 {
			t.Fatalf("catalog fault dispatched data: %v calls=%d", err, calls.records)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queryBoundIdentityRows = func(ctx context.Context, db *sql.DB) (boundIdentityRows, error) {
		rows, err := original(ctx, db)
		return cancelIdentityRows{boundIdentityRows: rows, cancel: cancel}, err
	}
	if _, err := leaf.ExecuteQueryToRecordsReader(ctx, identityQuery("A")); !errors.Is(err, context.Canceled) || calls.records != 0 {
		t.Fatalf("post-facts cancellation dispatched data: %v calls=%d", err, calls.records)
	}
}

func TestBoundIdentityOriginalAdmissionRefusesUnsupportedAndOpenFailure(t *testing.T) {
	requireBindingPathPlatform(t)
	f := newBindingFixture(t)
	e := NewExecutor(f.load(t))
	nested := dal.From(dal.NewQuerySource(identityQuery("A"), "nested")).NewQuery().SelectIntoRecord(nil)
	if _, err := e.RunStructured(context.Background(), f.load(t).binding.url, nested, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal(err)
	}
	if err := e.Authorize(context.Background(), nested, nil); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatal(err)
	}
	old := openBoundSourceFn
	t.Cleanup(func() { openBoundSourceFn = old })
	fault := errors.New("owned opener failure")
	openBoundSourceFn = func(context.Context, dbcopy.BackendRef) (dal.DB, func(), error) { return nil, nil, fault }
	if err := e.Authorize(context.Background(), identityQuery("A"), nil); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	legacy, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	legacy.AllowOpaqueSQL = true
	metadata, err := NewExecutor(legacy).RunNativeSQL(context.Background(), dbcopy.LocalSourceURL("sqlite", f.target), "PRAGMA table_list")
	if err != nil || len(metadata.Rows) == 0 {
		t.Fatalf("explicit opaque grant could not read table_list: %v", err)
	}
	if db, closeDB, err := openBoundSQLiteIdentityDB(context.Background(), dbcopy.BackendRef{Scheme: "sqlite", Path: filepath.Join(t.TempDir(), "missing.sqlite")}); db != nil || closeDB != nil || err == nil {
		t.Fatal("missing native source accepted")
	}
}
