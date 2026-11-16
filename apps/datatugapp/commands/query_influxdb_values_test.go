package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo2influxdb"
)

func TestInfluxLosslessScalarContract(t *testing.T) {
	for _, value := range []string{"0", "-0", "9007199254740993.000", "-123456789012345678901234567890", "1.2300E+004", "1e4096", "1e-4096"} {
		raw, err := json.Marshal(influxNumberJSON{Kind: "number", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		native, err := influxInputScalar(raw)
		if err != nil || native != json.Number(value) {
			t.Fatal("tagged numeric input lost its lexeme")
		}
		output, err := influxWireScalar(native)
		if err != nil || output != (influxNumberJSON{Kind: "number", Value: value}) {
			t.Fatal("tagged numeric output lost its lexeme")
		}
	}
	for _, value := range []string{"", "+1", ".5", "01", "NaN", "1e4097", "1e-4097", "1e9999999999999999999", strings.Repeat("1", 129)} {
		raw, err := json.Marshal(influxNumberJSON{Kind: "number", Value: value})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := influxInputScalar(raw); err == nil {
			t.Fatal("invalid JSON numeric lexeme admitted")
		}
		if _, err := influxWireScalar(json.Number(value)); err == nil {
			t.Fatal("invalid native number delivered")
		}
	}
	for _, raw := range []string{"", "[1]", "1", `{"kind":"string","value":"1"}`, `{"kind":"number","value":"1","extra":true}`, `{"kind":"number","kind":"number","value":"1"}`, `{"kind":"number","value":1}`} {
		if _, err := influxInputScalar(json.RawMessage(raw)); err == nil {
			t.Fatal("non-scalar/untagged input admitted")
		}
	}
	for _, test := range []struct {
		raw  string
		want any
	}{{"null", nil}, {"true", true}, {"false", false}, {`"astral-😀"`, "astral-😀"}} {
		value, err := influxInputScalar(json.RawMessage(test.raw))
		if err != nil || !reflect.DeepEqual(value, test.want) {
			t.Fatal("native scalar input changed")
		}
		wire, err := influxWireScalar(value)
		if err != nil || !reflect.DeepEqual(wire, test.want) {
			t.Fatal("native scalar output changed")
		}
	}
	for _, value := range []any{float64(9007199254740992), []any{1}, map[string]any{"kind": "number", "value": "1"}, string([]byte{0xff})} {
		if _, err := influxWireScalar(value); err == nil {
			t.Fatal("unknown/coerced native value delivered")
		}
	}
}

// The production constructor is still the actual public native client. This
// private decoded-result boundary lets the consumer prove that a changed or
// malformed provider result cannot leak a partial JSON prefix or lose a tag.
type influxBoundaryClient struct {
	get    func(context.Context, string, string) (*dalgo2influxdb.Row, error)
	exists func(context.Context, string, string) (bool, error)
	many   func(context.Context, string, []string) ([]*dalgo2influxdb.Row, error)
	query  func(context.Context, dalgo2influxdb.Query) (dalgo2influxdb.Page, error)
}

func (c influxBoundaryClient) Get(ctx context.Context, collection, key string) (*dalgo2influxdb.Row, error) {
	return c.get(ctx, collection, key)
}
func (c influxBoundaryClient) Exists(ctx context.Context, collection, key string) (bool, error) {
	return c.exists(ctx, collection, key)
}
func (c influxBoundaryClient) GetMany(ctx context.Context, collection string, keys []string) ([]*dalgo2influxdb.Row, error) {
	return c.many(ctx, collection, keys)
}
func (c influxBoundaryClient) Query(ctx context.Context, q dalgo2influxdb.Query) (dalgo2influxdb.Page, error) {
	return c.query(ctx, q)
}

func influxBoundaryAdmission(operation string) admittedInfluxRead {
	key := "a"
	return admittedInfluxRead{request: influxNativeRequest{Operation: operation, Collection: "events", Key: &key, Keys: []string{"a"}}}
}
func TestInfluxDecodedBoundaryRefusesMalformedRowsAndCursor(t *testing.T) {
	for _, row := range []*dalgo2influxdb.Row{
		{Key: string([]byte{0xff})},
		{Key: "a", Data: map[string]any{string([]byte{0xff}): true}},
		{Key: "a", Data: map[string]any{"amount": float64(9007199254740992)}},
		{Key: "a", Data: map[string]any{"amount": json.Number("01")}},
		{Key: "a", Data: map[string]any{"nested": map[string]any{"secret": "private-cause"}}},
		{Key: "a", Data: func() map[string]any {
			m := map[string]any{}
			for i := range 129 {
				m[strings.Repeat("x", i)] = nil
			}
			return m
		}()},
	} {
		client := influxBoundaryClient{get: func(context.Context, string, string) (*dalgo2influxdb.Row, error) { return row, nil }}
		var output bytes.Buffer
		if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("get")); err == nil || output.Len() != 0 || strings.Contains(err.Error(), "private-cause") {
			t.Fatal("malformed provider row delivered a prefix or secret")
		}
		client.many = func(context.Context, string, []string) ([]*dalgo2influxdb.Row, error) {
			return []*dalgo2influxdb.Row{row}, nil
		}
		if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("getMany")); err == nil || output.Len() != 0 {
			t.Fatal("malformed multi row delivered")
		}
		client.query = func(context.Context, dalgo2influxdb.Query) (dalgo2influxdb.Page, error) {
			return dalgo2influxdb.Page{Rows: []dalgo2influxdb.Row{*row}}, nil
		}
		if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("query")); err == nil || output.Len() != 0 {
			t.Fatal("malformed page row delivered")
		}
	}
	client := influxBoundaryClient{many: func(context.Context, string, []string) ([]*dalgo2influxdb.Row, error) { return nil, nil }}
	var output bytes.Buffer
	if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("getMany")); err == nil || output.Len() != 0 {
		t.Fatal("partial getMany success delivered")
	}
	client.query = func(context.Context, dalgo2influxdb.Query) (dalgo2influxdb.Page, error) {
		return dalgo2influxdb.Page{NextCursor: []any{json.Number("01")}}, nil
	}
	if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("query")); err == nil || output.Len() != 0 {
		t.Fatal("invalid typed cursor delivered")
	}
	client.query = func(context.Context, dalgo2influxdb.Query) (dalgo2influxdb.Page, error) {
		return dalgo2influxdb.Page{}, nil
	}
	if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("query")); err != nil || output.String() != `{"rows":[]}`+"\n" {
		t.Fatal("empty page was not an explicit empty array")
	}
}

func TestInfluxDecodedBoundaryErrorsFinalCancellationAndWriterSemantics(t *testing.T) {
	private := errors.New("private-token URL native cause")
	client := influxBoundaryClient{
		get:    func(context.Context, string, string) (*dalgo2influxdb.Row, error) { return nil, private },
		exists: func(context.Context, string, string) (bool, error) { return false, private },
		many:   func(context.Context, string, []string) ([]*dalgo2influxdb.Row, error) { return nil, private },
		query: func(context.Context, dalgo2influxdb.Query) (dalgo2influxdb.Page, error) {
			return dalgo2influxdb.Page{}, private
		},
	}
	for _, operation := range []string{"get", "exists", "getMany", "query", "delete"} {
		var output bytes.Buffer
		if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission(operation)); err == nil || strings.Contains(err.Error(), "private-token") || output.Len() != 0 {
			t.Fatal("native failure was not redacted atomically")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.get = func(context.Context, string, string) (*dalgo2influxdb.Row, error) {
		cancel()
		return &dalgo2influxdb.Row{Key: "a", Data: map[string]any{"amount": json.Number("1")}}, nil
	}
	var output bytes.Buffer
	if err := deliverInfluxRead(ctx, &output, client, influxBoundaryAdmission("get")); err == nil || !strings.Contains(err.Error(), "cancelled") || output.Len() != 0 {
		t.Fatal("late successful native result crossed cancellation")
	}
	ctx, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	entered := false
	client.get = func(ctx context.Context, _ string, _ string) (*dalgo2influxdb.Row, error) {
		entered = true
		<-ctx.Done()
		return nil, nil
	}
	if err := deliverInfluxRead(ctx, &output, client, influxBoundaryAdmission("get")); err == nil || !entered || !strings.Contains(err.Error(), "deadline exceeded") || output.Len() != 0 {
		t.Fatal("late missing result crossed deadline")
	}
	client.get = func(context.Context, string, string) (*dalgo2influxdb.Row, error) {
		return &dalgo2influxdb.Row{Key: "a", Data: map[string]any{"label": strings.Repeat("x", 8<<20)}}, nil
	}
	if err := deliverInfluxRead(context.Background(), &output, client, influxBoundaryAdmission("get")); err == nil || output.Len() != 0 {
		t.Fatal("encoded output cap was bypassed")
	}
	client.get = func(context.Context, string, string) (*dalgo2influxdb.Row, error) { return nil, nil }
	for _, writer := range []struct {
		writer io.Writer
		want   error
	}{{dynamoPartialWriter{}, io.ErrShortWrite}, {dynamoPartialWriter{err: io.ErrClosedPipe}, io.ErrClosedPipe}} {
		if err := deliverInfluxRead(context.Background(), writer.writer, client, influxBoundaryAdmission("get")); !errors.Is(err, writer.want) {
			t.Fatal("ordinary writer error changed")
		}
	}
}
