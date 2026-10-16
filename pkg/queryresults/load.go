package queryresults

import (
	"encoding/json"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// ReasonVersion is a result written in a format version this CLI does not know.
const ReasonVersion = "unsupported-version"

// Loaded is a complete result read back from its files.
type Loaded struct {
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
	loaded.Recordset, loaded.MetaJSON, loaded.DataJSON = data.Recordset, metaJSON, dataJSON
	return loaded, nil
}
