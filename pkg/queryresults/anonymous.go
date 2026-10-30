package queryresults

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// BuildAnonymous retains ordered typed cells without introducing a key or
// using label-based lookups. Size overflow is an error, never cell truncation.
func BuildAnonymous(id string, rs apicontract.Recordset, native, encodings []string, limit int) (Data, error) {
	if limit < MinRows || limit > MaxRows || len(rs.Columns) == 0 || len(rs.Columns) > MaxMetaColumns || len(native) != len(rs.Columns) || len(encodings) != len(rs.Columns) {
		return Data{}, fmt.Errorf("invalid anonymous recordset bounds")
	}
	rows := Rows{Kept: len(rs.Rows), TruncatedBy: TruncatedNone}
	if len(rs.Rows) > limit {
		rs.Rows = rs.Rows[:limit]
		rows.Kept = limit
		rows.HasMore = true
		rows.TruncatedBy = TruncatedRows
	}
	if err := rs.Validate(); err != nil {
		return Data{}, fmt.Errorf("invalid anonymous recordset")
	}
	columns := make([]Column, len(rs.Columns))
	for i, col := range rs.Columns {
		typ := native[i]
		if !utf8.ValidString(col.Name) || typ == "" || !utf8.ValidString(typ) || (encodings[i] != "" && encodings[i] != EncodingJSON) {
			return Data{}, fmt.Errorf("invalid anonymous column encoding")
		}
		columns[i] = Column{Name: col.Name, Type: col.Type, DeclaredType: &typ, Encoding: encodings[i]}
	}
	for _, row := range rs.Rows {
		if len(row) != len(rs.Columns) {
			return Data{}, fmt.Errorf("invalid anonymous row width")
		}
		for _, cell := range row {
			if err := cell.Validate(); err != nil || !utf8.ValidString(cell.Str) {
				return Data{}, fmt.Errorf("invalid anonymous cell")
			}
			b, err := json.Marshal(cell)
			if err != nil || len(b) > MaxCellBytes {
				return Data{}, fmt.Errorf("anonymous cell exceeds budget")
			}
		}
	}
	encoded, err := json.Marshal(struct {
		Format    string                `json:"format"`
		Version   int                   `json:"version"`
		ID        string                `json:"id"`
		Anonymous bool                  `json:"anonymous"`
		Recordset apicontract.Recordset `json:"recordset"`
	}{DataFormat, Version, id, true, rs})
	if err != nil || len(encoded) > MaxDataBytes {
		return Data{}, fmt.Errorf("anonymous recordset exceeds budget")
	}
	return Data{Anonymous: true, Bytes: encoded, Columns: columns, Rows: rows}, nil
}
