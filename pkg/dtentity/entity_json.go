// Package dtentity pretty-prints entities using the canonical Core named-table codec.
package dtentity

import (
	"encoding/json"

	"github.com/datatug/datatug-core/pkg/datatug"
)

var (
	jsonMarshal   = json.Marshal
	jsonUnmarshal = json.Unmarshal
)

// TableKeyDoc is the legacy display projection. Entity persistence uses Core's
// complete TableKeys codec; this projection must never replace entity.Tables.
// Deprecated: use datatug.TableKeys for entity JSON.
type TableKeyDoc struct {
	Name    string `json:"name"`
	Schema  string `json:"schema,omitempty"`
	Catalog string `json:"catalog,omitempty"`
}

// UnmarshalEntity delegates named table identities to the canonical Core codec.
func UnmarshalEntity(data []byte) (*datatug.Entity, error) {
	entity := new(datatug.Entity)
	if err := jsonUnmarshal(data, entity); err != nil {
		return nil, err
	}
	return entity, nil
}

// MarshalEntity encodes entity to indented JSON, correctly including Tables
// (see UnmarshalEntity). Re-marshals through a map, so key order is
// alphabetical rather than following the struct's field declaration order.
func MarshalEntity(entity *datatug.Entity) ([]byte, error) {
	data, err := jsonMarshal(entity)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := jsonUnmarshal(data, &doc); err != nil {
		return nil, err
	}
	return json.MarshalIndent(doc, "", "\t")
}
