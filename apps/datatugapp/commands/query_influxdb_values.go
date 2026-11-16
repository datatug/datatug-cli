package commands

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo2influxdb"
)

var influxJSONNumber = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

type influxNumberJSON struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type influxWireRowJSON struct {
	Key  string         `json:"key"`
	Data map[string]any `json:"data"`
}
type influxWirePage struct {
	Rows       []any `json:"rows"`
	NextCursor []any `json:"nextCursor,omitempty"`
}

func influxNumber(value string) bool {
	if len(value) > 128 || !influxJSONNumber.MatchString(value) {
		return false
	}
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		n, err := strconv.ParseInt(value[i+1:], 10, 32)
		if err != nil || n < -4096 || n > 4096 {
			return false
		}
	}
	return true
}

// A tagged number is deliberately not an ordinary JSON numeric literal: callers
// can parse/save nextCursor in JavaScript and submit the exact lexeme unchanged.
func influxInputScalar(raw json.RawMessage) (any, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return nil, errDynamoInput
	}
	switch value.(type) {
	case nil, string, bool:
		return value, nil
	case map[string]any:
		var number influxNumberJSON
		if err := decodeDynamoDocument(raw, &number); err == nil && number.Kind == "number" && influxNumber(number.Value) {
			return json.Number(number.Value), nil
		}
	}
	return nil, errDynamoInput
}

func influxWireScalar(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool:
		return value, nil
	case string:
		if utf8.ValidString(v) {
			return v, nil
		}
	case json.Number:
		if influxNumber(string(v)) {
			return influxNumberJSON{Kind: "number", Value: string(v)}, nil
		}
	}
	return nil, errDynamoResponse
}
func influxWireRow(row *dalgo2influxdb.Row) (any, error) {
	if row == nil {
		return nil, nil
	}
	if !utf8.ValidString(row.Key) || len(row.Data) > 128 {
		return nil, errDynamoResponse
	}
	result := influxWireRowJSON{Key: row.Key, Data: make(map[string]any, len(row.Data))}
	for name, value := range row.Data {
		if !utf8.ValidString(name) {
			return nil, errDynamoResponse
		}
		encoded, err := influxWireScalar(value)
		if err != nil {
			return nil, err
		}
		result.Data[name] = encoded
	}
	return result, nil
}
