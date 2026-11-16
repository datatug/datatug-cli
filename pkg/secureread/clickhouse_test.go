package secureread

import (
	"context"
	"encoding/json"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestClickHouseCellsPreserveExactNativeValues(t *testing.T) {
	maximum := uint64(math.MaxUint64)
	integer := new(big.Int)
	integer.SetString("340282366920938463463374607431768211455", 10)
	amount := decimal.RequireFromString("12345678901234567890.123456789")
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for _, tc := range []struct {
		input any
		want  apicontract.TypedValue
	}{
		{maximum, apicontract.NewIntegerValue("18446744073709551615")}, {integer, apicontract.NewIntegerValue(integer.String())}, {amount, apicontract.NewDecimalValue(amount.String())}, {at, apicontract.NewDatetimeValue("2026-01-02T03:04:05.123456789Z")}, {(*string)(nil), apicontract.NewNullValue()}, {"safe", apicontract.NewStringValue("safe")}, {true, apicontract.NewBooleanValue(true)}, {1.25, apicontract.NewNumberValue(1.25)},
	} {
		got, compound, err := clickHouseCell(reflect.ValueOf(tc.input), 0)
		require.NoError(t, err)
		require.False(t, compound)
		require.Equal(t, tc.want, got)
	}
	value, compound, err := clickHouseCell(reflect.ValueOf([]any{maximum, amount, nil}), 0)
	require.NoError(t, err)
	require.True(t, compound)
	var nested []apicontract.TypedValue
	require.NoError(t, json.Unmarshal([]byte(value.Str), &nested))
	require.Equal(t, "18446744073709551615", nested[0].Str)
	require.Equal(t, amount.String(), nested[1].Str)
	require.Equal(t, apicontract.ValueTypeNull, nested[2].Type)
	value, compound, err = clickHouseCell(reflect.ValueOf(map[string]uint64{"maximum": maximum}), 0)
	require.NoError(t, err)
	require.True(t, compound)
	require.Contains(t, value.Str, "18446744073709551615")
	for _, bad := range []any{math.NaN(), math.Inf(1), func() {}, struct{ X int }{1}} {
		_, _, err := clickHouseCell(reflect.ValueOf(bad), 0)
		require.Error(t, err)
	}
	for _, bad := range []any{string([]byte{0xff}), []string{string([]byte{0xff})}, map[string]int{string([]byte{0xff}): 1}, map[string][]string{"safe": {string([]byte{0xff})}}} {
		_, _, err := clickHouseCell(reflect.ValueOf(bad), 0)
		require.Error(t, err, "invalid UTF-8 must never become replacement characters")
	}
	_, _, err = clickHouseCell(reflect.ValueOf("safe"), 17)
	require.Error(t, err)
	for native, want := range map[string]string{"UInt64": "integer", "Nullable(Decimal(38,9))": "decimal", "LowCardinality(String)": "string", "Date": "date", "DateTime64(9, 'UTC')": "datetime", "Float64": "number", "Bool": "boolean", "Array(UInt64)": "string"} {
		require.Equal(t, want, clickHouseContractType(native))
	}
}

func TestClickHousePolicyGuardsOriginalAndEffectiveASTBeforeIO(t *testing.T) {
	db, err := dalgo2clickhouse.NewDB(context.Background(), dalgo2clickhouse.Config{Address: "127.0.0.1:1", Database: "explore", Username: "reader", PlaintextLoopback: true, Tables: map[string][]string{"events": {"id"}}})
	require.NoError(t, err)
	defer dbcopy.CloseSource(db)
	base := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(3)
	executor := NewExecutor(Session{Unrestricted: true})
	_, err = executor.runThroughPolicies(context.Background(), db, base.WhereField("amount", dal.Equal, 2).SelectIntoRecordset(), nil)
	require.ErrorIs(t, err, dal.ErrNotSupported, "original field scope must fail before dialing")
	session := aliceSession(t, strings.ReplaceAll(ownerScopedPolicy, "customers", "events"))
	// This policy references ownerID, outside this connection's id-only grant.
	_, err = NewExecutor(session).runThroughPolicies(context.Background(), db, dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Limit(3).SelectColumns(dal.Column{Expression: dal.Field("id")}), nil)
	require.ErrorIs(t, err, dal.ErrNotSupported, "policy-effective unsupported field must fail before dialing")
}

func TestClickHouseNativeGrantAndBindingRefusalsBeforeIO(t *testing.T) {
	const source = "env:DATATUG_CLICKHOUSE_TEST"
	t.Setenv("DATATUG_CLICKHOUSE_TEST", "clickhouse+http://127.0.0.1:1/explore")
	t.Setenv(dbcopy.ClickHouseNativeSourcesEnv, "DATATUG_CLICKHOUSE_TEST")
	t.Setenv(dbcopy.ClickHouseUsernameEnv, "reader")
	t.Setenv(dbcopy.ClickHousePasswordEnv, "synthetic")
	t.Setenv(dbcopy.ClickHouseNativeSQLEnv, "true")
	t.Setenv(dbcopy.ClickHouseRelationsEnv, "")
	_, err := NewExecutor(Session{}).RunNativeSQL(context.Background(), source, "SELECT 1")
	require.ErrorIs(t, err, ErrOpaqueSQLNotGranted)
	session := aliceSession(t, ownerScopedPolicy)
	session.AllowOpaqueSQL = true
	_, err = NewExecutor(session).RunNativeSQL(context.Background(), source, "SELECT 1")
	require.ErrorIs(t, err, ErrAccessDenied)
	for _, args := range [][]dal.QueryArg{{{Name: "bad-name", Value: 1}}, {{Name: "same", Value: 1}, {Name: "same", Value: 2}}, {{Name: "value", Value: []int{1}}}} {
		_, err = NewExecutor(Session{Unrestricted: true}).RunNativeSQL(context.Background(), source, "SELECT {value:UInt64}", args...)
		require.ErrorIs(t, err, dal.ErrNotSupported)
	}
}

func TestClickHouseVariableDomainIsScalarExactAndDetached(t *testing.T) {
	integer := big.NewInt(7)
	amount := decimal.RequireFromString("12345678901234567890.123456789")
	values, err := clickHouseVariables(map[string]any{"integer": integer, "amount": amount, "json": json.Number("18446744073709551615")})
	require.NoError(t, err)
	integer.SetInt64(9)
	require.Equal(t, "7", values["integer"].(*big.Int).String())
	require.Equal(t, amount.String(), values["amount"].(decimal.Decimal).String())
	require.Equal(t, json.Number("18446744073709551615"), values["json"])
	for _, bad := range []any{[]int{1}, map[string]int{"id": 1}, json.Number("NaN"), json.Number("1e9999"), (*big.Int)(nil)} {
		_, err := clickHouseVariables(map[string]any{"value": bad})
		require.Error(t, err)
	}
	for _, bad := range []any{math.NaN(), math.Inf(1), float64(1 << 54), string([]byte{0xff})} {
		_, err := clickHouseVariables(map[string]any{"value": bad})
		require.Error(t, err)
	}
	for _, name := range []string{"", "bad-name", "currentUser", "principal.roles", "path.id"} {
		_, err := clickHouseVariables(map[string]any{name: 1})
		require.Error(t, err)
	}
}
