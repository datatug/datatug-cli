package queryresults

import (
	"bytes"
	"encoding/json"
	"errors"
)

// errRecordData is the one error RecordData gives: it quotes no value.
var errRecordData = errors.New("a record's fields cannot be read")

// RecordData is the fields of a record's data with their driver values: a
// map is used as it is, and any other shape is read through its JSON form
// with numbers kept as exact text.
func RecordData(data any) (map[string]any, error) {
	switch x := data.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return x, nil
	case *map[string]any:
		if x == nil {
			return map[string]any{}, nil
		}
		return *x, nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, errRecordData
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var out map[string]any
	if decoder.Decode(&out) != nil {
		return nil, errRecordData
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
