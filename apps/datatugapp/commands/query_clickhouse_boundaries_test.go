package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func clickHouseCommandResult() secureread.Result {
	row := []apicontract.TypedValue{apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewDecimalValue("12345678901234567890.123456789")}
	return secureread.Result{AnonymousRecordset: &apicontract.Recordset{Columns: []apicontract.Column{{Name: "same", Type: "integer"}, {Name: "same", Type: "decimal"}}, Rows: [][]apicontract.TypedValue{row, append([]apicontract.TypedValue(nil), row...)}}, OrdinalColumns: []secureread.OrdinalColumn{{NativeType: "UInt64"}, {NativeType: "Decimal(38,9)"}}}
}

func TestClickHouseBindingScalarAndMetadataBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pairs []string
	}{
		{"binding count", make([]string, 257)},
		{"missing equals", []string{"id"}},
		{"value byte budget", []string{"text=" + strings.Repeat("a", 4097)}},
		{"malformed YAML", []string{"text=["}},
		{"unsupported scalar tag", []string{"text=!!binary SGk="}},
		{"numeric text budget", []string{"id=" + strings.Repeat("9", 257)}},
		{"negative exponent budget", []string{"id=1e-77"}},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := parseClickHouseVariables(tc.pairs); require.Error(t, err) })
	}
	values, err := parseClickHouseVariables([]string{"empty=", "missing=null", "enabled=false", "date=2026-10-30", "word=hello", "id=18446744073709551615", "decimal=12345678901234567890.123456789", "exponent=1e2"})
	require.NoError(t, err)
	require.Equal(t, "", values["empty"])
	require.Nil(t, values["missing"])
	require.Equal(t, false, values["enabled"])
	require.Equal(t, "2026-10-30", values["date"])
	typed := clickHouseTypedParameters(values)
	require.Equal(t, apicontract.ScalarValue(apicontract.NewIntegerValue("18446744073709551615")), typed["id"])
	require.Equal(t, apicontract.ScalarValue(apicontract.NewDecimalValue("12345678901234567890.123456789")), typed["decimal"])
	require.Equal(t, apicontract.ScalarValue(apicontract.NewDecimalValue("100")), typed["exponent"])
	require.Equal(t, apicontract.ScalarValue(apicontract.NewNullValue()), typed["missing"])
	require.Nil(t, clickHouseTypedParameters(nil))
	// Defensive metadata conversion must not invent a rounded value for an
	// invalid numeric input supplied by an internal caller.
	require.Empty(t, clickHouseTypedParameters(map[string]any{"bad": json.Number("bad")}))
	require.Empty(t, clickHouseTypedParameters(map[string]any{"large": json.Number("1e77")}))
}

type clickHouseOutputFailure struct {
	allow int
	calls int
	err   error
}

func (w *clickHouseOutputFailure) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.allow {
		return 0, w.err
	}
	return len(p), nil
}

func TestClickHouseAnonymousOutputRefusesInvalidCarrierBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*secureread.Result)
		want   string
	}{
		{"missing ordinal", func(r *secureread.Result) { r.OrdinalColumns = nil }, "ordinal metadata"},
		{"malformed row", func(r *secureread.Result) { r.AnonymousRecordset.Rows[0] = nil }, "invalid anonymous recordset"},
		{"invalid cell UTF8", func(r *secureread.Result) {
			r.AnonymousRecordset.Rows[0][1] = apicontract.NewStringValue(string([]byte{0xff}))
		}, "cell encoding"},
		{"invalid native UTF8", func(r *secureread.Result) { r.OrdinalColumns[0].NativeType = string([]byte{0xff}) }, "metadata encoding"},
		{"invalid encoding UTF8", func(r *secureread.Result) { r.OrdinalColumns[0].Encoding = string([]byte{0xff}) }, "metadata encoding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := clickHouseCommandResult()
			tc.mutate(&r)
			var out bytes.Buffer
			require.ErrorContains(t, writeAnonymousResult(&out, "json", r), tc.want)
			require.Empty(t, out.String())
		})
	}
}

func TestClickHouseAnonymousOutputPreservesWriterFailures(t *testing.T) {
	brokenPipe := errors.New("synthetic output closed")
	for _, tc := range []struct {
		format string
		allow  int
	}{{"json", 0}, {"yaml", 0}, {"jsonl", 0}, {"jsonl", 1}, {"csv", 0}, {"grid", 0}} {
		t.Run(tc.format+strings.Repeat("-after-write", tc.allow), func(t *testing.T) {
			w := &clickHouseOutputFailure{allow: tc.allow, err: brokenPipe}
			err := writeAnonymousResult(w, tc.format, clickHouseCommandResult())
			if tc.format == "yaml" {
				require.Error(t, err)
			} else {
				require.ErrorIs(t, err, brokenPipe)
			}
			require.Greater(t, w.calls, tc.allow)
		})
	}
	result := secureread.Result{AnonymousRecordset: &apicontract.Recordset{Columns: []apicontract.Column{{Name: "null", Type: "string"}, {Name: "bool", Type: "boolean"}, {Name: "number", Type: "number"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewNullValue(), apicontract.NewBooleanValue(false), apicontract.NewNumberValue(1.25)}}}, OrdinalColumns: []secureread.OrdinalColumn{{NativeType: "Nullable(String)"}, {NativeType: "Bool"}, {NativeType: "Float64"}}}
	var out bytes.Buffer
	require.NoError(t, writeAnonymousResult(&out, "csv", result))
	require.Equal(t, "null,bool,number\nnull,false,1.25\n", out.String())
	require.NoError(t, writeAnonymousResult(io.Discard, "grid", result))
	// A one-column line can flush immediately, while multi-column output
	// above exercises the final buffered flush; both must retain failures.
	result.AnonymousRecordset.Columns = result.AnonymousRecordset.Columns[:1]
	result.AnonymousRecordset.Rows[0] = result.AnonymousRecordset.Rows[0][:1]
	result.OrdinalColumns = result.OrdinalColumns[:1]
	require.ErrorIs(t, writeAnonymousResult(&clickHouseOutputFailure{err: brokenPipe}, "grid", result), brokenPipe)
}

func TestClickHouseAnonymousGridKeepsExactEscapedBytes(t *testing.T) {
	result := secureread.Result{
		AnonymousRecordset: &apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "same", Type: "string"}, {Name: "same", Type: "string"}},
			Rows:    [][]apicontract.TypedValue{{apicontract.NewStringValue("a\tb"), apicontract.NewStringValue("\n\"")}},
		},
		OrdinalColumns: []secureread.OrdinalColumn{{NativeType: "String"}, {NativeType: "String"}},
	}
	var out bytes.Buffer
	require.NoError(t, writeAnonymousResult(&out, "grid", result))
	require.Equal(t, "\"same\"  \"same\"\n\"a\\tb\"  \"\\n\\\"\"\n", out.String())
}
