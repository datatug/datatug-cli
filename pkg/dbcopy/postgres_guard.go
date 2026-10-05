package dbcopy

import (
	"context"
	"errors"
	"regexp"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/ddl"
	"github.com/dal-go/dalgo/recordset"
	dalrecord "github.com/dal-go/record"
	"github.com/dal-go/record/update"
	"github.com/jackc/pgx/v5/pgconn"
)

// What the shared handle of a PostgreSQL source answers when a call of the adapter fails for want of a connection.
//
// The adapter builds a fixed sentence for a connection it cannot open (see openPostgres), but once a source is open,
// every call is a call of its pool, and a pool that has to make a connection again (the server was restarted or is
// unreachable, the password was changed, the connection limit was reached) fails with what pgx writes: "failed to
// connect to `user=... database=...`: ...", and for a server that answers with an error, the text of that error, which
// can name the role. The error holds the whole configuration, the password included. A person's terminal, a client's
// answer, the agent log and the chat's prompt all print what a read returns, so the handle that every read passes
// answers one sentence instead, and keeps nothing of the cause.

// sqlStateShape is what a SQLSTATE code is: five upper-case letters and digits. Anything else a server sends in its
// place is not shown.
var sqlStateShape = regexp.MustCompile(`^[0-9A-Z]{5}$`)

// connectionLostSentence is the one sentence of a call that failed for want of a connection.
const connectionLostSentence = "the connection to the PostgreSQL server was lost and could not be made again"

// postgresConnectionError is what a call of the adapter answers when pgx could not make a connection: a fixed
// sentence, the SQLSTATE the server answered with when it answered one, and nothing of the cause (no Unwrap): not the
// user, not the database, not the address, not the configuration. It keeps one thing of the cause, whether the
// connection attempt ended because its context ended, so that errors.Is still tells a deadline from any other
// failure to the callers that map one to a timeout.
type postgresConnectionError struct {
	sqlState string
	canceled bool
	timedOut bool
}

// Error returns the fixed sentence, and the SQLSTATE when there is one.
func (e *postgresConnectionError) Error() string {
	if e.sqlState == "" {
		return connectionLostSentence
	}
	return connectionLostSentence + " (SQLSTATE " + e.sqlState + ")"
}

// SQLState returns the SQLSTATE the server answered with, or "" when it answered none.
func (e *postgresConnectionError) SQLState() string { return e.sqlState }

// Is reports the two context errors of the attempt that failed, and nothing else.
func (e *postgresConnectionError) Is(target error) bool {
	return (e.canceled && target == context.Canceled) || (e.timedOut && target == context.DeadlineExceeded) //nolint:errorlint // the sentinels themselves
}

// guardPostgresError is the one road every error of a call into the adapter takes. An error whose chain holds pgx's
// connect error or its configuration error becomes a *postgresConnectionError; any other error (DataTug's own, DALgo's,
// the adapter's built ones, the error of a statement) is returned as it is. The error of a statement is not touched on
// purpose: it is the server's answer to what the person asked, and the hints for it (a name of the wrong case) are the
// next task's.
func guardPostgresError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *pgconn.ConnectError
	var configErr *pgconn.ParseConfigError
	if !errors.As(err, &connectErr) && !errors.As(err, &configErr) {
		return err
	}
	guarded := &postgresConnectionError{
		canceled: errors.Is(err, context.Canceled),
		timedOut: errors.Is(err, context.DeadlineExceeded),
	}
	var serverErr *pgconn.PgError
	if errors.As(err, &serverErr) && sqlStateShape.MatchString(serverErr.Code) {
		guarded.sqlState = serverErr.Code
	}
	return guarded
}

// guarded is guardPostgresError for a call that answers a value and an error.
func guarded[T any](value T, err error) (T, error) { return value, guardPostgresError(err) }

// The calls of the adapter that can fail for want of a connection, each answering through guardPostgresError. A call
// that is not here fails as the adapter fails it: TestSharedPostgres_GuardsEveryErrorReturningCallOfTheAdapter names
// any call that a new release of the adapter adds.

func (s *sharedPostgres) RunReadonlyTransaction(ctx context.Context, f dal.ROTxWorker, opts ...dal.TransactionOption) error {
	return guardPostgresError(s.Database.RunReadonlyTransaction(ctx, f, opts...))
}

func (s *sharedPostgres) RunReadwriteTransaction(ctx context.Context, f dal.RWTxWorker, opts ...dal.TransactionOption) error {
	return guardPostgresError(s.Database.RunReadwriteTransaction(ctx, f, opts...))
}

func (s *sharedPostgres) Get(ctx context.Context, record dalrecord.Record) error {
	return guardPostgresError(s.Database.Get(ctx, record))
}

func (s *sharedPostgres) GetMulti(ctx context.Context, records []dalrecord.Record) error {
	return guardPostgresError(s.Database.GetMulti(ctx, records))
}

func (s *sharedPostgres) Exists(ctx context.Context, key *dalrecord.Key) (bool, error) {
	return guarded(s.Database.Exists(ctx, key))
}

// ExecuteQueryToRecordsReader answers a reader whose Next is guarded too.
func (s *sharedPostgres) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	reader, err := s.Database.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return nil, guardPostgresError(err)
	}
	return guardedRecordsReader{reader}, nil
}

// ExecuteQueryToRecordsetReader answers a reader whose Next is guarded too.
func (s *sharedPostgres) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, opts ...recordset.Option) (dal.RecordsetReader, error) {
	reader, err := s.Database.ExecuteQueryToRecordsetReader(ctx, query, opts...)
	if err != nil {
		return nil, guardPostgresError(err)
	}
	return guardedRecordsetReader{reader}, nil
}

func (s *sharedPostgres) CanExecuteJoin(ctx context.Context, q dal.StructuredQuery) error {
	return guardPostgresError(s.Database.CanExecuteJoin(ctx, q))
}

func (s *sharedPostgres) JoinFields(ctx context.Context, source dal.RecordsetSource) ([]string, error) {
	return guarded(s.Database.JoinFields(ctx, source))
}

func (s *sharedPostgres) Set(ctx context.Context, record dalrecord.Record) error {
	return guardPostgresError(s.Database.Set(ctx, record))
}

func (s *sharedPostgres) SetMulti(ctx context.Context, records []dalrecord.Record) error {
	return guardPostgresError(s.Database.SetMulti(ctx, records))
}

func (s *sharedPostgres) Insert(ctx context.Context, record dalrecord.Record, opts ...dal.InsertOption) error {
	return guardPostgresError(s.Database.Insert(ctx, record, opts...))
}

func (s *sharedPostgres) Upsert(ctx context.Context, record dalrecord.Record) error {
	return guardPostgresError(s.Database.Upsert(ctx, record))
}

func (s *sharedPostgres) Delete(ctx context.Context, key *dalrecord.Key) error {
	return guardPostgresError(s.Database.Delete(ctx, key))
}

func (s *sharedPostgres) DeleteMulti(ctx context.Context, keys []*dalrecord.Key) error {
	return guardPostgresError(s.Database.DeleteMulti(ctx, keys))
}

func (s *sharedPostgres) Update(ctx context.Context, key *dalrecord.Key, updates []update.Update, preconditions ...dal.Precondition) error {
	return guardPostgresError(s.Database.Update(ctx, key, updates, preconditions...))
}

func (s *sharedPostgres) UpdateMulti(ctx context.Context, keys []*dalrecord.Key, updates []update.Update, preconditions ...dal.Precondition) error {
	return guardPostgresError(s.Database.UpdateMulti(ctx, keys, updates, preconditions...))
}

func (s *sharedPostgres) ListCollections(ctx context.Context, parent *dalrecord.Key) ([]dal.CollectionRef, error) {
	return guarded(s.Database.ListCollections(ctx, parent))
}

func (s *sharedPostgres) ListSchemas(ctx context.Context) ([]string, error) {
	return guarded(s.Database.ListSchemas(ctx))
}

func (s *sharedPostgres) ListSchemaCollections(ctx context.Context, schema string) ([]dal.CollectionRef, error) {
	return guarded(s.Database.ListSchemaCollections(ctx, schema))
}

func (s *sharedPostgres) ListViews(ctx context.Context) ([]dal.CollectionRef, error) {
	return guarded(s.Database.ListViews(ctx))
}

func (s *sharedPostgres) ListSchemaViews(ctx context.Context, schema string) ([]dal.CollectionRef, error) {
	return guarded(s.Database.ListSchemaViews(ctx, schema))
}

func (s *sharedPostgres) DescribeCollection(ctx context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	return guarded(s.Database.DescribeCollection(ctx, ref))
}

func (s *sharedPostgres) ListIndexes(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return guarded(s.Database.ListIndexes(ctx, ref))
}

func (s *sharedPostgres) ListConstraints(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return guarded(s.Database.ListConstraints(ctx, ref))
}

func (s *sharedPostgres) ListReferrers(ctx context.Context, ref *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return guarded(s.Database.ListReferrers(ctx, ref))
}

func (s *sharedPostgres) NonDeterministicTextColumns(ctx context.Context, ref *dal.CollectionRef) ([]string, error) {
	return guarded(s.Database.NonDeterministicTextColumns(ctx, ref))
}

func (s *sharedPostgres) CreateCollection(ctx context.Context, c dbschema.CollectionDef, opts ...ddl.Option) error {
	return guardPostgresError(s.Database.CreateCollection(ctx, c, opts...))
}

func (s *sharedPostgres) DropCollection(ctx context.Context, name string, opts ...ddl.Option) error {
	return guardPostgresError(s.Database.DropCollection(ctx, name, opts...))
}

func (s *sharedPostgres) AlterCollection(ctx context.Context, name string, ops ...ddl.AlterOp) error {
	return guardPostgresError(s.Database.AlterCollection(ctx, name, ops...))
}

// guardedRecordsReader is a reader of records whose Next answers through guardPostgresError.
type guardedRecordsReader struct{ dal.RecordsReader }

// Next is the reader's own, with its error guarded.
func (r guardedRecordsReader) Next() (dalrecord.Record, error) {
	return guarded(r.RecordsReader.Next())
}

// guardedRecordsetReader is a reader of a recordset whose Next answers through guardPostgresError.
type guardedRecordsetReader struct{ dal.RecordsetReader }

// Next is the reader's own, with its error guarded.
func (r guardedRecordsetReader) Next() (row recordset.Row, rs recordset.Recordset, err error) {
	row, rs, err = r.RecordsetReader.Next()
	return row, rs, guardPostgresError(err)
}
