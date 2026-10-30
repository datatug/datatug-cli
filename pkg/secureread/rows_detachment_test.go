package secureread

import (
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"testing"
)

type reusedMutableReader struct {
	dal.RecordsReader
	at     int
	data   map[string]any
	bytes  []byte
	nested map[string]any
}

func (r *reusedMutableReader) Next() (record.Record, error) {
	if r.at == 2 {
		return nil, dal.ErrNoMoreRecords
	}
	r.bytes[0] = byte('a' + r.at)
	r.nested["integer"] = int64(9007199254740993 + int64(r.at))
	r.at++
	return record.NewRecordWithData(record.NewKeyWithID("items", r.at), r.data), nil
}
func (r *reusedMutableReader) Close() error {
	r.bytes[0] = 'z'
	r.nested["integer"] = int64(0)
	return nil
}
func TestCollectRowsDetachesReusedDriverCellsBeforeNextAndClose(t *testing.T) {
	buffer := []byte("a")
	nested := map[string]any{"integer": int64(0)}
	reader := &reusedMutableReader{bytes: buffer, nested: nested, data: map[string]any{"binary": buffer, "object": nested, "array": []any{nested}}}
	rows, _, err := collectRows(reader)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range rows {
		if binary, ok := row.Data["binary"].([]byte); !ok || string(binary) != string(rune('a'+i)) {
			t.Fatalf("row %d buffer reused: %#v", i, row.Data["binary"])
		}
		expected := int64(9007199254740993 + int64(i))
		if row.Data["object"].(map[string]any)["integer"] != expected || row.Data["array"].([]any)[0].(map[string]any)["integer"] != expected {
			t.Fatalf("row %d nested mutable cells reused: %#v", i, row.Data)
		}
	}
}
