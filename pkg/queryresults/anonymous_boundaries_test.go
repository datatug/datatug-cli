package queryresults

import (
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func anonymousBoundaryRecordset() apicontract.Recordset {
	return apicontract.Recordset{Columns: []apicontract.Column{{Name: "value", Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue("exact")}}}
}

func TestBuildAnonymousRejectsMalformedRowsAndInvalidText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*apicontract.Recordset)
		want   string
	}{
		{"short row", func(rs *apicontract.Recordset) { rs.Rows[0] = nil }, "invalid anonymous recordset"},
		{"long row", func(rs *apicontract.Recordset) { rs.Rows[0] = append(rs.Rows[0], apicontract.NewNullValue()) }, "invalid anonymous recordset"},
		{"invalid scalar", func(rs *apicontract.Recordset) { rs.Rows[0][0] = apicontract.TypedValue{} }, "invalid anonymous recordset"},
		{"invalid UTF8 scalar", func(rs *apicontract.Recordset) { rs.Rows[0][0] = apicontract.NewStringValue(string([]byte{0xff})) }, "invalid anonymous cell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := anonymousBoundaryRecordset()
			tc.mutate(&rs)
			data, err := BuildAnonymous("bad", rs, []string{"String"}, []string{""}, 10)
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, data.Bytes, "invalid data must not be writable")
		})
	}
	// Each individual cell fits; the aggregate must still fail closed.
	rs := anonymousBoundaryRecordset()
	rs.Rows = make([][]apicontract.TypedValue, 100)
	for i := range rs.Rows {
		rs.Rows[i] = []apicontract.TypedValue{apicontract.NewStringValue(strings.Repeat("x", 60000))}
	}
	data, err := BuildAnonymous("large", rs, []string{"String"}, []string{""}, 100)
	require.ErrorContains(t, err, "recordset exceeds budget")
	require.Empty(t, data.Bytes)
}

func TestAnonymousReopenValidatesOrdinalMetadataAndParameters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*apicontract.Recordset, *Meta)
	}{
		{"no columns", func(rs *apicontract.Recordset, _ *Meta) { rs.Columns = nil }},
		{"metadata width", func(_ *apicontract.Recordset, meta *Meta) { meta.Columns = nil }},
		{"row width", func(rs *apicontract.Recordset, _ *Meta) { rs.Rows[0] = nil }},
		{"column identity", func(_ *apicontract.Recordset, meta *Meta) { meta.Columns[0].Name = "other" }},
		{"column type", func(_ *apicontract.Recordset, meta *Meta) { meta.Columns[0].Type = "integer" }},
		{"internal column", func(_ *apicontract.Recordset, meta *Meta) { meta.Columns[0].Internal = true }},
		{"missing declared type", func(_ *apicontract.Recordset, meta *Meta) { meta.Columns[0].DeclaredType = nil }},
		{"invalid encoding", func(_ *apicontract.Recordset, meta *Meta) { meta.Columns[0].Encoding = "unrecognized" }},
		{"invalid text", func(rs *apicontract.Recordset, _ *Meta) { rs.Rows[0][0].Str = string([]byte{0xff}) }},
		{"invalid parameter", func(_ *apicontract.Recordset, meta *Meta) {
			meta.Query.Parameters["id"] = apicontract.TypedValueOrSet{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := anonymousBoundaryRecordset()
			data, err := BuildAnonymous("reopen", rs, []string{"String"}, []string{""}, 10)
			require.NoError(t, err)
			meta := NewMeta("reopen", "test", data)
			meta.Query.Parameters = map[string]apicontract.TypedValueOrSet{"id": apicontract.ScalarValue(apicontract.NewIntegerValue("18446744073709551615"))}
			require.True(t, wellFormedAnonymous(rs, meta), "exact parameters are valid before mutation")
			tc.mutate(&rs, &meta)
			require.False(t, wellFormedAnonymous(rs, meta))
		})
	}
}

func TestAnonymousLoadRejectsTamperedMetadataWithoutExposingValues(t *testing.T) {
	store := newStore(t)
	rs := anonymousBoundaryRecordset()
	data, err := BuildAnonymous("private-result", rs, []string{"String"}, []string{""}, 10)
	require.NoError(t, err)
	result, err := store.Create("private-result")
	require.NoError(t, err)
	meta := NewMeta("private-result", "test", data)
	meta.Query.Parameters = map[string]apicontract.TypedValueOrSet{"id": apicontract.ScalarValue(apicontract.NewIntegerValue("18446744073709551615"))}
	require.NoError(t, result.Write(data.Bytes, meta.Encode()))
	loaded, err := store.Load("private-result")
	require.NoError(t, err)
	require.Equal(t, rs, loaded.Recordset)
	require.True(t, loaded.Anonymous)
	require.Equal(t, meta.Query.Parameters, loaded.Meta.Query.Parameters)
	require.JSONEq(t, `{"resultFiles":{"version":1},"rowsKept":1,"hasMore":false,"columns":1}`, string(LocatorLine(data)))
	meta.Columns[0].Name = "different"
	require.NoError(t, result.Write(data.Bytes, meta.Encode()))
	_, err = store.Load("private-result")
	require.Error(t, err)
	require.Equal(t, ReasonIncomplete, ReasonOf(err))
	require.NotContains(t, err.Error(), "private-result")
	require.NotContains(t, err.Error(), "exact")
}
