package dbcopy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/dalgo2postgres"
	dalrecord "github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/datatug/datatug-cli/internal/pgstandin"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testHint is the hint of the source the tests of this file call the adapter for.
const testHint = "the PostgreSQL connection string is read from the --db flag"

// lostSentence is the sentence of a failure of pgx's that the adapter did not classify, with the hint of the source.
const lostSentence = "the connection to the PostgreSQL server was lost and could not be made again; " + testHint

// pgxConnectError is the error pgx writes when a connection cannot be made, built by pgx itself: its text names the
// user and its value holds the whole configuration, the password included. dialErr is what the dial failed with.
func pgxConnectError(t *testing.T, dialErr error) error {
	t.Helper()
	config, err := pgconn.ParseConfig("postgres://" + markerUser + ":" + markerPassword + "@127.0.0.1:5432/shop?sslmode=disable&x=" + markerQuery)
	require.NoError(t, err)
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) { return nil, dialErr }
	_, err = pgconn.ConnectConfig(context.Background(), config)
	var connectErr *pgconn.ConnectError
	require.True(t, errors.As(err, &connectErr), "pgx's own connect error")
	require.Contains(t, err.Error(), markerUser, "and its text names the user")
	return err
}

// The second road of every error of a call into the adapter: an error of pgx's that the adapter did not classify (it
// classifies every failure of a connection, so none gets here from a conforming adapter) and that holds pgx's connect
// error or its configuration error becomes one fixed sentence that names nothing and holds nothing of its cause, with
// the hint of the source; any other error is not touched.
func TestGuardPostgresError(t *testing.T) {
	t.Parallel()
	plain := errors.New("an error of DataTug's or DALgo's own")
	assert.NoError(t, guardPostgresError(testHint, nil))
	assert.Same(t, plain, guardPostgresError(testHint, plain), "an error that is not a connection error passes through as it is")
	assert.ErrorIs(t, guardPostgresError(testHint, dal.ErrNoMoreRecords), dal.ErrNoMoreRecords)
	statement := &pgconn.PgError{Severity: "ERROR", Code: "42P01", Message: `relation "orders" does not exist`}
	assert.Same(t, statement, guardPostgresError(testHint, statement), "a statement's error is the next task's (the hints for a wrong name)")

	for name, tc := range map[string]struct {
		cause     error
		wantText  string
		wantState string
		timedOut  bool
		canceled  bool
	}{
		"a refused dial":          {pgxConnectError(t, errors.New("connection refused")), lostSentence, "", false, false},
		"a dial that is late":     {pgxConnectError(t, context.DeadlineExceeded), lostSentence, "", true, false},
		"a dial that is canceled": {pgxConnectError(t, context.Canceled), lostSentence, "", false, true},
		"a server that refuses the password": {
			pgxConnectError(t, &pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: `password authentication failed for user "` + markerUser + `"`}),
			"the connection to the PostgreSQL server was lost and could not be made again (SQLSTATE 28P01); " + testHint, "28P01", false, false},
		"a server code that is not a code": {
			pgxConnectError(t, &pgconn.PgError{Severity: "FATAL", Code: "role " + markerUser, Message: markerPassword}),
			lostSentence, "", false, false},
		"wrapped by the caller":     {fmt.Errorf("list collections: %w", pgxConnectError(t, errors.New("down"))), lostSentence, "", false, false},
		"joined with another error": {errors.Join(plain, pgxConnectError(t, errors.New("down"))), lostSentence, "", false, false},
		"a connection string pgx cannot parse": {func() error {
			_, err := pgconn.ParseConfig("postgres://" + markerUser + ":" + markerPassword + "@h:notaport/db?x=" + markerQuery)
			return err
		}(), lostSentence, "", false, false},
	} {
		got := guardPostgresError(testHint, tc.cause)
		require.Error(t, got, name)
		assert.Equal(t, tc.wantText, got.Error(), name)
		assertNoMarkers(t, name, got)
		assert.Nil(t, errors.Unwrap(got), name+": nothing of the cause is left in the chain")
		var sqlState interface{ SQLState() string }
		require.True(t, errors.As(got, &sqlState), name)
		assert.Equal(t, tc.wantState, sqlState.SQLState(), name)
		assert.Equal(t, tc.timedOut, errors.Is(got, context.DeadlineExceeded), name+": a deadline stays a deadline")
		assert.Equal(t, tc.canceled, errors.Is(got, context.Canceled), name+": a cancellation stays one")
		assert.NotErrorIs(t, got, plain, name)
	}
}

// postgresCalls is every call of the adapter that needs a connection or reports one that failed, as the table below
// runs them against the adapter itself and against the shared handle that guards it.
type postgresCalls interface {
	RunReadonlyTransaction(context.Context, dal.ROTxWorker, ...dal.TransactionOption) error
	RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error
	Get(context.Context, dalrecord.Record) error
	GetMulti(context.Context, []dalrecord.Record) error
	Exists(context.Context, *dalrecord.Key) (bool, error)
	ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error)
	ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error)
	CanExecuteJoin(context.Context, dal.StructuredQuery) error
	JoinFields(context.Context, dal.RecordsetSource) ([]string, error)
	Set(context.Context, dalrecord.Record) error
	SetMulti(context.Context, []dalrecord.Record) error
	Insert(context.Context, dalrecord.Record, ...dal.InsertOption) error
	Upsert(context.Context, dalrecord.Record) error
	Delete(context.Context, *dalrecord.Key) error
	DeleteMulti(context.Context, []*dalrecord.Key) error
	Update(context.Context, *dalrecord.Key, []update.Update, ...dal.Precondition) error
	UpdateMulti(context.Context, []*dalrecord.Key, []update.Update, ...dal.Precondition) error
	ListCollections(context.Context, *dalrecord.Key) ([]dal.CollectionRef, error)
	ListSchemas(context.Context) ([]string, error)
	ListSchemaCollections(context.Context, string) ([]dal.CollectionRef, error)
	ListViews(context.Context) ([]dal.CollectionRef, error)
	ListSchemaViews(context.Context, string) ([]dal.CollectionRef, error)
	DescribeCollection(context.Context, *dal.CollectionRef) (*dbschema.CollectionDef, error)
	ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error)
	ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error)
	ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error)
	NonDeterministicTextColumns(context.Context, *dal.CollectionRef) ([]string, error)
	CreateCollection(context.Context, dbschema.CollectionDef, ...ddl.Option) error
	DropCollection(context.Context, string, ...ddl.Option) error
	AlterCollection(context.Context, string, ...ddl.AlterOp) error
}

var (
	_ postgresCalls = (*dalgo2postgres.Database)(nil)
	_ postgresCalls = (*sharedPostgres)(nil)
)

// postgresCallTable runs each call with arguments that get as far as the connection.
func postgresCallTable() map[string]func(postgresCalls) error {
	ctx := context.Background()
	key := dalrecord.NewKeyWithID("orders", "1")
	newRecord := func() dalrecord.Record {
		return dalrecord.NewRecordWithData(dalrecord.NewKeyWithID("orders", "1"), map[string]any{"status": "new"})
	}
	ref := dal.NewRootCollectionRef("orders", "")
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("orders", ""))).SelectColumns()
	return map[string]func(postgresCalls) error{
		"RunReadonlyTransaction": func(c postgresCalls) error {
			return c.RunReadonlyTransaction(ctx, func(context.Context, dal.ReadTransaction) error { return nil })
		},
		"RunReadwriteTransaction": func(c postgresCalls) error {
			return c.RunReadwriteTransaction(ctx, func(context.Context, dal.ReadwriteTransaction) error { return nil })
		},
		"Get":      func(c postgresCalls) error { return c.Get(ctx, newRecord()) },
		"GetMulti": func(c postgresCalls) error { return c.GetMulti(ctx, []dalrecord.Record{newRecord()}) },
		"Exists":   func(c postgresCalls) error { _, err := c.Exists(ctx, key); return err },
		"ExecuteQueryToRecordsReader": func(c postgresCalls) error {
			_, err := c.ExecuteQueryToRecordsReader(ctx, query)
			return err
		},
		"ExecuteQueryToRecordsetReader": func(c postgresCalls) error {
			_, err := c.ExecuteQueryToRecordsetReader(ctx, query)
			return err
		},
		"CanExecuteJoin": func(c postgresCalls) error { return c.CanExecuteJoin(ctx, query) },
		"JoinFields":     func(c postgresCalls) error { _, err := c.JoinFields(ctx, ref); return err },
		"Set":            func(c postgresCalls) error { return c.Set(ctx, newRecord()) },
		"SetMulti":       func(c postgresCalls) error { return c.SetMulti(ctx, []dalrecord.Record{newRecord()}) },
		"Insert":         func(c postgresCalls) error { return c.Insert(ctx, newRecord()) },
		"Upsert":         func(c postgresCalls) error { return c.Upsert(ctx, newRecord()) },
		"Delete":         func(c postgresCalls) error { return c.Delete(ctx, key) },
		"DeleteMulti":    func(c postgresCalls) error { return c.DeleteMulti(ctx, []*dalrecord.Key{key}) },
		"Update": func(c postgresCalls) error {
			return c.Update(ctx, key, []update.Update{update.ByFieldName("status", "x")})
		},
		"UpdateMulti": func(c postgresCalls) error {
			return c.UpdateMulti(ctx, []*dalrecord.Key{key}, []update.Update{update.ByFieldName("status", "x")})
		},
		"ListCollections":             func(c postgresCalls) error { _, err := c.ListCollections(ctx, nil); return err },
		"ListSchemas":                 func(c postgresCalls) error { _, err := c.ListSchemas(ctx); return err },
		"ListSchemaCollections":       func(c postgresCalls) error { _, err := c.ListSchemaCollections(ctx, "sales"); return err },
		"ListViews":                   func(c postgresCalls) error { _, err := c.ListViews(ctx); return err },
		"ListSchemaViews":             func(c postgresCalls) error { _, err := c.ListSchemaViews(ctx, "sales"); return err },
		"DescribeCollection":          func(c postgresCalls) error { _, err := c.DescribeCollection(ctx, &ref); return err },
		"ListIndexes":                 func(c postgresCalls) error { _, err := c.ListIndexes(ctx, &ref); return err },
		"ListConstraints":             func(c postgresCalls) error { _, err := c.ListConstraints(ctx, &ref); return err },
		"ListReferrers":               func(c postgresCalls) error { _, err := c.ListReferrers(ctx, &ref); return err },
		"NonDeterministicTextColumns": func(c postgresCalls) error { _, err := c.NonDeterministicTextColumns(ctx, &ref); return err },
		"CreateCollection": func(c postgresCalls) error {
			return c.CreateCollection(ctx, dbschema.CollectionDef{Name: "orders", Fields: []dbschema.FieldDef{{Name: "id", Type: dbschema.String}}, PrimaryKey: []dal.FieldName{"id"}})
		},
		"DropCollection":  func(c postgresCalls) error { return c.DropCollection(ctx, "orders") },
		"AlterCollection": func(c postgresCalls) error { return c.AlterCollection(ctx, "orders", ddl.DropIndex("by_status")) },
	}
}

// A database whose server went away, behind the shared handle: every call of the adapter that fails for want of a
// connection answers the adapter's fixed sentence and the hint of the source, whichever road it takes (a read, a
// transaction, a schema read, a write, a DDL statement). The same call on the adapter itself answers its connection
// error, which is what the guard turns into that text (a call that answers anything else, a failure of the guard's
// own road, would not be found by this table: so each call is run on both, and the table is not vacuous).
func TestSharedPostgres_EveryCallThatNeedsAConnectionAnswersAFixedSentence(t *testing.T) {
	t.Parallel()
	for name, call := range postgresCallTable() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := pgstandin.Unreachable(t, markerUser, markerPassword, "orders")

			raw := call(db)
			if name == "CanExecuteJoin" {
				// It asks what the dialect runs on the server, not the server: it answers without a connection.
				assert.Equal(t, raw, call(&sharedPostgres{Database: db, hint: testHint}), "the answer of a call that needs no connection is the adapter's")
				return
			}
			require.Error(t, raw)
			var adapterErr *dalgo2postgres.ConnectionError
			require.ErrorAs(t, raw, &adapterErr, "the adapter classifies a connection that fails at this call")
			assertNoMarkers(t, name, raw)

			guarded := call(&sharedPostgres{Database: db, hint: testHint})
			require.Error(t, guarded)
			assert.Equal(t, postgresFailureText(adapterSentence(adapterErr), testHint), guarded.Error())
			assert.NotEmpty(t, adapterSentence(adapterErr))
			assertNoMarkers(t, name, guarded)
			assert.Nil(t, errors.Unwrap(guarded))
			assert.NotNil(t, UnavailableSource(guarded), "a route answers it as the source being unavailable")
		})
	}
}

// A new release of the adapter may add a call that reports a connection that failed. The shared handle must guard
// it too, so a call the adapter has that the table above does not run fails this test, naming it.
func TestSharedPostgres_GuardsEveryErrorReturningCallOfTheAdapter(t *testing.T) {
	t.Parallel()
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	exempt := map[string]string{
		"Close":        "the shared handle's own: a no-op",
		"UpdateRecord": "answers dal.ErrNotImplementedYet: it never reaches a server",
	}
	table := postgresCallTable()
	var unguarded []string
	adapter := reflect.TypeOf(&dalgo2postgres.Database{})
	for i := 0; i < adapter.NumMethod(); i++ {
		method := adapter.Method(i)
		results := method.Type.NumOut()
		if results == 0 || method.Type.Out(results-1) != errorType {
			continue
		}
		if _, ok := exempt[method.Name]; ok {
			continue
		}
		if _, ok := table[method.Name]; !ok {
			unguarded = append(unguarded, method.Name)
		}
	}
	sort.Strings(unguarded)
	assert.Empty(t, unguarded, "the adapter has calls that return an error and that the guard does not cover")
}

// The readers a read returns are guarded too: an error of Next takes the same road, and the end of a result is still
// the end of a result.
func TestSharedPostgres_ReadersAreGuardedToo(t *testing.T) {
	t.Parallel()
	connectErr := pgxConnectError(t, errors.New("down"))
	endless := errors.New("not touched")

	records := guardedRecordsReader{&fakeRecordsReader{err: connectErr}, testHint}
	_, err := records.Next()
	require.Error(t, err)
	assertNoMarkers(t, "records", err)
	assert.Equal(t, lostSentence, err.Error())
	_, err = guardedRecordsReader{&fakeRecordsReader{err: dal.ErrNoMoreRecords}, testHint}.Next()
	assert.ErrorIs(t, err, dal.ErrNoMoreRecords)
	_, err = guardedRecordsReader{&fakeRecordsReader{err: endless}, testHint}.Next()
	assert.Same(t, endless, err)

	sets := guardedRecordsetReader{&fakeRecordsetReader{err: connectErr}, testHint}
	_, _, err = sets.Next()
	require.Error(t, err)
	assertNoMarkers(t, "recordset", err)
	_, _, err = guardedRecordsetReader{&fakeRecordsetReader{}, testHint}.Next()
	assert.NoError(t, err)
	assert.NoError(t, sets.Close(), "what the reader does besides Next is the reader's own")
}

// The calls that open a reader answer a guarded reader, and the failure of the call itself is guarded.
func TestSharedPostgres_TheCallsThatOpenAReaderAnswerAGuardedReader(t *testing.T) {
	t.Parallel()
	shared := &sharedPostgres{Database: &dalgo2postgres.Database{DB: readerDB{err: pgxConnectError(t, errors.New("down"))}}, hint: testHint}
	ctx := context.Background()
	query := dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef("orders", ""))).SelectColumns()

	records, err := shared.ExecuteQueryToRecordsReader(ctx, query)
	require.NoError(t, err)
	_, err = records.Next()
	require.Error(t, err)
	assertNoMarkers(t, "records", err)

	sets, err := shared.ExecuteQueryToRecordsetReader(ctx, query)
	require.NoError(t, err)
	_, _, err = sets.Next()
	require.Error(t, err)
	assertNoMarkers(t, "recordset", err)
}

// fakeRecordsReader answers every Next with err.
type fakeRecordsReader struct {
	dal.RecordsReader
	err error
}

func (r *fakeRecordsReader) Next() (dalrecord.Record, error) { return nil, r.err }

// fakeRecordsetReader answers every Next with err (nil: a row of nothing).
type fakeRecordsetReader struct {
	dal.RecordsetReader
	err error
}

func (r *fakeRecordsetReader) Next() (recordset.Row, recordset.Recordset, error) {
	return nil, nil, r.err
}
func (r *fakeRecordsetReader) Close() error { return nil }

// readerDB is the part of dal.DB under the adapter that opens readers, answering readers whose Next fails the way a
// reader's does when its connection is gone.
type readerDB struct {
	dal.DB
	err error
}

func (d readerDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return &fakeRecordsReader{err: d.err}, nil
}

func (d readerDB) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return &fakeRecordsetReader{err: d.err}, nil
}
