package queryresults

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// ReasonVersion is a result written in a format version this CLI does not know.
const ReasonVersion = "unsupported-version"

// Loaded is a complete result read back from its files.
type Loaded struct {
	Anonymous bool
	Meta      Meta
	Recordset apicontract.Recordset
	// MetaJSON and DataJSON are the files as they were read.
	MetaJSON, DataJSON []byte
}

// Load reads a complete, unexpired result: it locates it with the same checks
// as Locate, then reads meta.json and data.json, refusing a data.json whose
// size is not the one meta.json records and a format version it does not know.
func (s *Store) Load(id string) (Loaded, error) {
	locator, err := s.Locate(id)
	if err != nil {
		return Loaded{}, err
	}
	metaJSON, err := readFile(locator.Meta, MaxMetaBytes)
	if err != nil || len(metaJSON) > MaxMetaBytes {
		return Loaded{}, fail(ReasonIncomplete)
	}
	var loaded Loaded
	if json.Unmarshal(metaJSON, &loaded.Meta) != nil {
		return Loaded{}, fail(ReasonIncomplete)
	}
	if loaded.Meta.Format != MetaFormat || loaded.Meta.Version != Version {
		return Loaded{}, fail(ReasonVersion)
	}
	dataJSON, err := readFile(locator.Data, MaxDataBytes)
	if err != nil || int64(len(dataJSON)) != loaded.Meta.DataBytes {
		return Loaded{}, fail(ReasonIncomplete)
	}
	var data struct {
		Anonymous bool                  `json:"anonymous"`
		Format    string                `json:"format"`
		Version   int                   `json:"version"`
		Recordset apicontract.Recordset `json:"recordset"`
	}
	if json.Unmarshal(dataJSON, &data) != nil {
		return Loaded{}, fail(ReasonIncomplete)
	}
	if data.Format != DataFormat || data.Version != Version {
		return Loaded{}, fail(ReasonVersion)
	}
	if data.Anonymous != loaded.Meta.Anonymous {
		return Loaded{}, fail(ReasonIncomplete)
	}
	valid := wellFormed(data.Recordset, loaded.Meta.Query.Parameters)
	if data.Anonymous {
		valid = utf8.Valid(dataJSON) && wellFormedAnonymous(data.Recordset, loaded.Meta)
	}
	if !valid {
		return Loaded{}, fail(ReasonIncomplete)
	}
	loaded.Recordset, loaded.MetaJSON, loaded.DataJSON = data.Recordset, metaJSON, dataJSON
	loaded.Anonymous = data.Anonymous
	return loaded, nil
}

func wellFormedAnonymous(rs apicontract.Recordset, meta Meta) bool {
	if len(rs.Columns) == 0 || len(rs.Columns) != len(meta.Columns) || rs.Validate() != nil {
		return false
	}
	for i, col := range rs.Columns {
		info := meta.Columns[i]
		if !utf8.ValidString(col.Name) || col.Name != info.Name || col.Type != info.Type || info.Internal || info.PK != nil || info.DeclaredType == nil || *info.DeclaredType == "" || !utf8.ValidString(*info.DeclaredType) || (info.Encoding != "" && info.Encoding != EncodingJSON) {
			return false
		}
	}
	for _, row := range rs.Rows {
		for _, cell := range row {
			if !utf8.ValidString(cell.Str) {
				return false
			}
		}
	}
	for _, value := range meta.Query.Parameters {
		if value.Validate() != nil {
			return false
		}
	}
	return true
}

// wellFormed is whether a recordset and the parameters have the shape this
// package writes: the key column first, every row as long as the columns,
// and every parameter a scalar or a set.
func wellFormed(recordset apicontract.Recordset, parameters map[string]apicontract.TypedValueOrSet) bool {
	if len(recordset.Columns) == 0 || recordset.Columns[0].Name != KeyColumn {
		return false
	}
	for _, row := range recordset.Rows {
		if len(row) != len(recordset.Columns) {
			return false
		}
	}
	for _, value := range parameters {
		if (value.Scalar == nil) == (value.Set == nil) {
			return false
		}
	}
	return true
}
