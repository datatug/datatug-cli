package secureread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

// The pure validator is the actual released SDK compiler. Only its result
// transport is replaced, so unsupported ASTs cannot be accepted by a fake.
type clickHouseQueryDB struct {
	dal.DB
	validator dalgo2clickhouse.StructuredQueryValidator
	reader    dal.RecordsetReader
	failure   error
	queries   []dal.Query
	closes    int
}

func (d *clickHouseQueryDB) ValidateStructuredQuery(q dal.Query) error {
	return d.validator.ValidateStructuredQuery(q)
}
func (d *clickHouseQueryDB) ExecuteQueryToRecordsetReader(_ context.Context, q dal.Query, _ ...recordset.Option) (dal.RecordsetReader, error) {
	d.queries = append(d.queries, q)
	return d.reader, d.failure
}
func (d *clickHouseQueryDB) Close() error { d.closes++; dbcopy.CloseSource(d.DB); return nil }

func clickHouseQueryDatabase(t *testing.T, reader dal.RecordsetReader) *clickHouseQueryDB {
	t.Helper()
	db, err := dalgo2clickhouse.NewDB(context.Background(), dalgo2clickhouse.Config{Address: "127.0.0.1:1", Database: "explore", Username: "reader", PlaintextLoopback: true, Tables: map[string][]string{"events": {"id", "amount", "ownerID"}}})
	require.NoError(t, err)
	validator, ok := dal.As[dalgo2clickhouse.StructuredQueryValidator](db)
	require.True(t, ok)
	t.Cleanup(func() { dbcopy.CloseSource(db) })
	return &clickHouseQueryDB{DB: db, validator: validator, reader: reader}
}

func TestClickHouseStructuredExecutionRetainsPolicyQueryAndOwnsReaders(t *testing.T) {
	old := openSourceFn
	t.Cleanup(func() { openSourceFn = old })
	query := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(3).SelectColumns(dal.Column{Expression: dal.Field("id")})
	reader := &clickHouseResultReader{set: clickHouseResultSet(t, []string{"id"}, []string{"UInt64"}, []any{uint64(7)})}
	db := clickHouseQueryDatabase(t, reader)
	openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
		return db, dbcopy.SourceCloser(db), nil
	}
	session := aliceSession(t, strings.ReplaceAll(ownerScopedPolicy, "customers", "events"))
	result, err := NewExecutor(session).RunStructured(context.Background(), "env:DATATUG_CH_EXECUTION", query, nil)
	require.NoError(t, err)
	require.Equal(t, "events", result.Collection)
	require.Len(t, result.AnonymousRecordset.Rows, 1)
	require.Equal(t, 1, db.closes)
	require.Equal(t, 1, reader.closes)
	require.Len(t, db.queries, 1)
	prepared := db.queries[0].(dal.StructuredQuery)
	require.Contains(t, fmt.Sprint(prepared.Where()), "ownerID")
	require.NoError(t, db.ValidateStructuredQuery(prepared))
	require.NotEmpty(t, result.Limitations, "the policy predicate must remain visible to the caller")
	for _, tc := range []struct {
		name    string
		failure error
		reader  dal.RecordsetReader
		want    error
	}{
		{"leaf failure", errors.New("leaf refused"), nil, nil},
		{"invalid result", nil, nil, dalgo2clickhouse.ErrBackend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := clickHouseQueryDatabase(t, tc.reader)
			d.failure = tc.failure
			openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
				return d, dbcopy.SourceCloser(d), nil
			}
			result, err := NewExecutor(Session{Unrestricted: true}).RunStructured(context.Background(), "env:DATATUG_CH_EXECUTION", query, nil)
			want := tc.want
			if tc.failure != nil {
				want = tc.failure
			}
			require.ErrorIs(t, err, want)
			require.Nil(t, result.AnonymousRecordset)
			require.Equal(t, 1, d.closes)
		})
	}
}

type clickHouseNativeDB struct {
	*clickHouseQueryDB
	text        string
	args        []any
	nativeCalls int
}

func (d *clickHouseNativeDB) QueryNative(_ context.Context, text string, args ...any) (dal.RecordsetReader, error) {
	d.nativeCalls++
	d.text = text
	d.args = append([]any(nil), args...)
	return d.reader, d.failure
}

func TestClickHouseNativeExecutionPreservesBindingsAndClosesEveryOwnedSource(t *testing.T) {
	old := openClickHouseNativeFn
	t.Cleanup(func() { openClickHouseNativeFn = old })
	t.Setenv("DATATUG_CH_NATIVE_EXECUTION", "clickhouse+http://127.0.0.1:1/explore")
	const source = "env:DATATUG_CH_NATIVE_EXECUTION"
	const text = "SELECT {id:UInt64} AS same,{id:UInt64} AS same"
	args := []dal.QueryArg{{Name: "id", Value: json.Number("18446744073709551615")}}
	reader := &clickHouseResultReader{set: clickHouseResultSet(t, []string{"same", "same"}, []string{"UInt64", "UInt64"}, []any{uint64(7), uint64(7)})}
	db := &clickHouseNativeDB{clickHouseQueryDB: clickHouseQueryDatabase(t, reader)}
	opens := 0
	openClickHouseNativeFn = func(_ context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		opens++
		require.Equal(t, source, ref.Raw)
		return db, nil
	}
	_, err := NewExecutor(Session{}).RunNativeSQL(context.Background(), source, text, args...)
	require.ErrorIs(t, err, ErrOpaqueSQLNotGranted)
	require.Zero(t, opens)
	result, err := NewExecutor(Session{Unrestricted: true}).RunNativeSQL(context.Background(), source, text, args...)
	require.NoError(t, err)
	require.Equal(t, text, db.text)
	require.Equal(t, []any{sql.Named("id", args[0].Value)}, db.args)
	require.Equal(t, 1, db.closes)
	require.Equal(t, 1, reader.closes)
	require.Len(t, result.AnonymousRecordset.Columns, 2)
	require.Equal(t, result.AnonymousRecordset.Rows[0][0], result.AnonymousRecordset.Rows[0][1])
	require.Equal(t, LimitationNativeSQL, result.Limitations[0].Kind)
	marker := errors.New("native leaf failed")
	for _, tc := range []struct {
		name                    string
		missingCapability, deny bool
		openError, leafError    error
		want                    error
	}{
		{"open error", false, false, marker, nil, marker},
		{"missing native capability", true, false, nil, nil, ErrNativeSQLUnsupported},
		{"policy refusal", false, true, nil, nil, ErrAccessDenied},
		{"leaf error", false, false, nil, marker, marker},
		{"missing result", false, false, nil, nil, dalgo2clickhouse.ErrBackend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := clickHouseQueryDatabase(t, nil)
			base.failure = tc.leafError
			native := &clickHouseNativeDB{clickHouseQueryDB: base}
			openClickHouseNativeFn = func(context.Context, dbcopy.BackendRef) (dal.DB, error) {
				if tc.openError != nil {
					return nil, tc.openError
				}
				if tc.missingCapability {
					return base, nil
				}
				return native, nil
			}
			session := Session{Unrestricted: true}
			if tc.deny {
				session = aliceSession(t, ownerScopedPolicy)
				session.AllowOpaqueSQL = true
			}
			result, err := NewExecutor(session).RunNativeSQL(context.Background(), source, text, args...)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, result.AnonymousRecordset)
			require.NotContains(t, err.Error(), text)
			if tc.openError != nil {
				require.Zero(t, base.closes)
			} else {
				require.Equal(t, 1, base.closes)
			}
			if tc.deny || tc.missingCapability {
				require.Zero(t, native.nativeCalls)
			}
		})
	}
}

func TestClickHouseNativeIdentityGuardRejectsSQLOrBindingRewrites(t *testing.T) {
	const text = "SELECT {value:String}"
	args := []dal.QueryArg{{Name: "value", Value: "literal"}}
	guard := clickHouseNativeQueryGuard(text, args)
	require.NoError(t, guard(dal.NewTextQuery(text, nil, args...)))
	for _, changed := range []dal.Query{
		dal.NewTextQuery(text+" SETTINGS readonly=0", nil, args...),
		dal.NewTextQuery(text, nil, dal.QueryArg{Name: "other", Value: "literal"}),
		dal.NewTextQuery(text, nil, dal.QueryArg{Name: "value", Value: "changed"}),
		dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(1).SelectIntoRecordset(),
	} {
		err := guard(changed)
		require.ErrorIs(t, err, dal.ErrNotSupported)
		require.NotContains(t, err.Error(), text)
	}
	for _, original := range []error{context.Canceled, context.DeadlineExceeded, dalgo2clickhouse.ErrLimit} {
		err := clickHousePreparationError(fmt.Errorf("private response: %w", original))
		require.ErrorIs(t, err, original)
		require.NotContains(t, err.Error(), "private response")
	}
	// The separate native opener retains its real default configuration gate.
	_, err := openClickHouseNativeFn(context.Background(), dbcopy.BackendRef{Scheme: dbcopy.ClickHouseScheme})
	require.Error(t, err)
}
