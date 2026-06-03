package datatug

import (
	"fmt"

	"github.com/dal-go/dalgo/dal"
)

type CollectionType string

const (
	CollectionTypeAny                    = "*"
	CollectionTypeUnknown CollectionType = ""
	CollectionTypeTable   CollectionType = "table"
	CollectionTypeView    CollectionType = "view"
)

func IsKnownCollectionType(v CollectionType) bool {
	switch v {
	case CollectionTypeAny, CollectionTypeTable, CollectionTypeView:
		return true
	case CollectionTypeUnknown:
		return false
	default:
		return false
	}
}

// DBCollectionKey defines a key that identifies a table or a view.
//
// The identity fields are exported (with json tags) so a collection/table
// round-trips through JSON. Ref is in-memory only (json:"-") and is populated
// by NewCollectionKey.
type DBCollectionKey struct {
	Name    string            `json:"name"`
	Schema  string            `json:"schema,omitempty"`
	Catalog string            `json:"catalog,omitempty"`
	Type    CollectionType    `json:"type,omitempty"`
	Ref     dal.CollectionRef `json:"-"`
}

func NewCollectionKey(t CollectionType, name, schema, catalog string, parent *dal.Key) DBCollectionKey {
	if !IsKnownCollectionType(t) {
		panic(fmt.Sprintf("unknown collection type: %s", t))
	}
	return DBCollectionKey{
		Type:    t,
		Schema:  schema,
		Catalog: catalog,
		Name:    name,
		Ref:     dal.NewCollectionRef(name, "", parent),
	}
}

func NewTableKey(name, schema, catalog string, parent *dal.Key) DBCollectionKey {
	return NewCollectionKey(CollectionTypeTable, name, schema, catalog, parent)
}

func NewViewKey(name, schema, catalog string, parent *dal.Key) DBCollectionKey {
	return NewCollectionKey(CollectionTypeView, name, schema, catalog, parent)
}

func (v DBCollectionKey) String() string {
	return fmt.Sprintf("DBCollectionKey{catalog=%s,schema=%s,name=%s}", v.Catalog, v.Schema, v.Name)
}

// Validate returns error if not valid
func (v DBCollectionKey) Validate() error {
	return nil
}
