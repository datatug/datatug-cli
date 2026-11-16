package queryresults

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestBuildAnonymousPreservesDuplicateColumnsAndRows(t *testing.T) {
	rs := apicontract.Recordset{Columns: []apicontract.Column{{Name: "same", Type: "integer"}, {Name: "same", Type: "decimal"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewDecimalValue("12345678901234567890.123456789")}, {apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewDecimalValue("12345678901234567890.123456789")}}}
	data, err := BuildAnonymous("test", rs, []string{"UInt64", "Decimal(38,9)"}, []string{"", ""}, 10)
	require.NoError(t, err)
	require.Equal(t, 2, data.Rows.Kept)
	require.NotContains(t, string(data.Bytes), "$key")
	require.Equal(t, data.Columns[0].Name, data.Columns[1].Name)
	var got struct {
		Recordset apicontract.Recordset `json:"recordset"`
	}
	require.NoError(t, json.Unmarshal(data.Bytes, &got))
	require.Equal(t, rs, got.Recordset)
	data, err = BuildAnonymous("test", rs, []string{"UInt64", "Decimal(38,9)"}, []string{"", ""}, 1)
	require.NoError(t, err)
	require.True(t, data.Rows.HasMore)
	require.Equal(t, TruncatedRows, data.Rows.TruncatedBy)
	_, err = BuildAnonymous("test", rs, nil, nil, 10)
	require.Error(t, err)
	rs.Rows[0] = []apicontract.TypedValue{apicontract.NewStringValue(strings.Repeat("a", MaxCellBytes+1)), apicontract.NewNullValue()}
	_, err = BuildAnonymous("test", rs, []string{"UInt64", "Decimal(38,9)"}, []string{"", ""}, 10)
	require.Error(t, err)
}

func TestAnonymousStoreLoadRoundtripAndDiscriminator(t *testing.T) {
	store := newStore(t)
	rs := apicontract.Recordset{Columns: []apicontract.Column{{Name: "same", Type: "integer"}, {Name: "same", Type: "integer"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewIntegerValue("18446744073709551615")}, {apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewIntegerValue("18446744073709551615")}}}
	data, err := BuildAnonymous("anon", rs, []string{"UInt64", "UInt64"}, []string{"", ""}, 10)
	require.NoError(t, err)
	result, err := store.Create("anon")
	require.NoError(t, err)
	meta := NewMeta("anon", "test", data)
	require.NoError(t, result.Write(data.Bytes, meta.Encode()))
	loaded, err := store.Load("anon")
	require.NoError(t, err)
	require.True(t, loaded.Anonymous)
	require.Equal(t, rs, loaded.Recordset)
	require.NotContains(t, string(loaded.DataJSON), "$key")
	meta.Anonymous = false
	require.NoError(t, result.Write(data.Bytes, meta.Encode()))
	_, err = store.Load("anon")
	require.Error(t, err, "mismatched data/meta discriminator must fail closed")
	rs.Columns[0].Name = string([]byte{0xff})
	_, err = BuildAnonymous("bad", rs, []string{"UInt64", "UInt64"}, []string{"", ""}, 10)
	require.Error(t, err)
}
