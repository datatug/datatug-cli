// Package dtentity provides JSON encode/decode for *datatug.Entity that
// correctly round-trips Tables (the "generated mapping copy") - see
// TableKeyDoc for why the model's own JSON tags can't do this in
// datatug-core v0.17.0. This is unrelated to and does not paper over the
// separate, tracked datatug-core filestore entities layout bug (S27); it
// only fixes how this CLI parses/renders the "tables" YAML/JSON section it
// itself authors and displays.
package dtentity

import (
	"encoding/json"

	"github.com/datatug/datatug-core/pkg/datatug"
)

// TableKeyDoc is the JSON/YAML shape a table/mapping-copy reference is
// authored or rendered as: {name, schema, catalog}. datatug-core v0.17.0's
// datatug.DBCollectionKey (embedded in datatug.TableKeys, i.e.
// datatug.Entity.Tables - the "generated mapping copy") made its identity
// fields unexported - Name()/Schema()/Catalog() methods backed by a
// dal.CollectionRef, instead of exported string fields - so it has no
// exported fields or MarshalJSON/UnmarshalJSON encoding/json can use.
// Confirmed: a generic json.Marshal of a populated Entity.Tables renders as
// `{"Ref":{}}` per key, and json.Unmarshal of authored {"name":...} JSON
// leaves DBCollectionKey entirely zero-valued. MarshalEntity/UnmarshalEntity
// bridge this with the module's own public NewTableKey constructor and
// Name()/Schema()/Catalog() accessors - not a fork of the type, just correct
// use of its actual (methods-based) public API instead of relying on
// encoding/json reflection, which v0.17.0 no longer supports for it.
type TableKeyDoc struct {
	Name    string `json:"name"`
	Schema  string `json:"schema,omitempty"`
	Catalog string `json:"catalog,omitempty"`
}

func tableKeysFromDocs(docs []TableKeyDoc) datatug.TableKeys {
	if len(docs) == 0 {
		return nil
	}
	keys := make(datatug.TableKeys, len(docs))
	for i, d := range docs {
		keys[i] = datatug.NewTableKey(d.Name, d.Schema, d.Catalog, nil)
	}
	return keys
}

func tableKeyDocsFromKeys(keys datatug.TableKeys) []TableKeyDoc {
	if len(keys) == 0 {
		return nil
	}
	docs := make([]TableKeyDoc, len(keys))
	for i, k := range keys {
		docs[i] = TableKeyDoc{Name: k.Name(), Schema: k.Schema(), Catalog: k.Catalog()}
	}
	return docs
}

// UnmarshalEntity decodes JSON into a *datatug.Entity, additionally
// populating Tables - see the TableKeyDoc doc comment for why that can't
// happen through *datatug.Entity's own JSON tags in datatug-core v0.17.0.
func UnmarshalEntity(data []byte) (*datatug.Entity, error) {
	entity := new(datatug.Entity)
	if err := json.Unmarshal(data, entity); err != nil {
		return nil, err
	}
	var tablesDoc struct {
		Tables []TableKeyDoc `json:"tables,omitempty"`
	}
	if err := json.Unmarshal(data, &tablesDoc); err != nil {
		return nil, err
	}
	entity.Tables = tableKeysFromDocs(tablesDoc.Tables)
	return entity, nil
}

// MarshalEntity encodes entity to indented JSON, correctly including Tables
// (see UnmarshalEntity). Re-marshals through a map, so key order is
// alphabetical rather than following the struct's field declaration order.
func MarshalEntity(entity *datatug.Entity) ([]byte, error) {
	data, err := json.Marshal(entity)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if tables := tableKeyDocsFromKeys(entity.Tables); len(tables) > 0 {
		tablesJSON, err := json.Marshal(tables)
		if err != nil {
			return nil, err
		}
		doc["tables"] = tablesJSON
	} else {
		delete(doc, "tables")
	}
	return json.MarshalIndent(doc, "", "\t")
}
