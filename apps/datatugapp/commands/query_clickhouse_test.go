package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestClickHouseVariablesKeepNumericLexemes(t *testing.T) {
	values, err := parseClickHouseVariables([]string{"id=18446744073709551615", "amount=12345678901234567890.123456789", "text='123.4'", "ok=true", "quoted='1e9999'", "exponent=1e+2"})
	require.NoError(t, err)
	require.Equal(t, json.Number("18446744073709551615"), values["id"])
	require.Equal(t, json.Number("12345678901234567890.123456789"), values["amount"])
	require.Equal(t, "123.4", values["text"])
	require.Equal(t, true, values["ok"])
	require.Equal(t, "1e9999", values["quoted"])
	require.Equal(t, json.Number("1e+2"), values["exponent"])
	for _, tc := range []struct {
		name  string
		pairs []string
	}{
		{"duplicate name", []string{"id=1", "id=2"}},
		{"nonfinite YAML float", []string{"amount=.inf"}},
		{"overflow inferred as string", []string{"amount=1e9999"}},
		{"hex integer", []string{"id=0x12"}},
		{"integer separator", []string{"id=1_000"}},
		{"reserved name", []string{"currentUser=mallory"}},
		{"nested sequence", []string{"nested=[1,2]"}},
		{"coefficient exceeds 256 bits", []string{"amount=" + strings.Repeat("9", 78)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClickHouseVariables(tc.pairs)
			require.Error(t, err, "pairs: %q", tc.pairs)
		})
	}
}

func TestClickHouseAnonymousOutputUsesOrdinals(t *testing.T) {
	result := secureread.Result{AnonymousRecordset: &apicontract.Recordset{Columns: []apicontract.Column{{Name: "same", Type: "integer"}, {Name: "same", Type: "decimal"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewIntegerValue("18446744073709551615"), apicontract.NewDecimalValue("12345678901234567890.123456789")}}}, OrdinalColumns: []secureread.OrdinalColumn{{NativeType: "UInt64"}, {NativeType: "Decimal(38,9)"}}}
	for _, format := range []string{"json", "jsonl", "yaml", "csv", "grid"} {
		var out bytes.Buffer
		require.NoError(t, writeAnonymousResult(&out, format, result))
		require.NotContains(t, out.String(), "$key")
		require.Contains(t, out.String(), "18446744073709551615")
		require.Contains(t, out.String(), "12345678901234567890.123456789")
	}
	var out bytes.Buffer
	require.Error(t, writeAnonymousResult(&out, "unknown", result))
	require.Error(t, writeAnonymousResult(&out, "json", secureread.Result{}))
	result.AnonymousRecordset.Columns[0].Name = string([]byte{0xff})
	out.Reset()
	require.Error(t, writeAnonymousResult(&out, "json", result))
	require.Empty(t, out.String())
}

type clickHouseFailingCSVWriter struct {
	err   error
	calls int
}

func (w *clickHouseFailingCSVWriter) Write([]byte) (int, error) {
	w.calls++
	return 0, w.err
}

func TestClickHouseAnonymousCSVPropagatesDestinationFailure(t *testing.T) {
	brokenPipe := errors.New("CSV destination closed")
	destination := &clickHouseFailingCSVWriter{err: brokenPipe}
	result := secureread.Result{
		AnonymousRecordset: &apicontract.Recordset{
			Columns: []apicontract.Column{{Name: "id", Type: "integer"}},
			Rows:    [][]apicontract.TypedValue{{apicontract.NewIntegerValue("18446744073709551615")}},
		},
		OrdinalColumns: []secureread.OrdinalColumn{{NativeType: "UInt64"}},
	}
	err := writeAnonymousResult(destination, "csv", result)
	require.ErrorIs(t, err, brokenPipe)
	require.Positive(t, destination.calls, "the valid result must reach the destination writer")
}
