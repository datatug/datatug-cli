package daemon

import (
	"encoding/json"
	"sort"
)

// WarningNotScanned is the one warning of a link that was not checked against the project's
// scanned metadata: the project has none for the source (never scanned, or it cannot be read).
const WarningNotScanned = "table metadata not scanned: link not validated"

// Relation is one scanned table or view: its schema ("" for a schemaless source), its name and, for
// a table, its primary-key column names.
type Relation struct {
	Schema, Name string
	View         bool
	PrimaryKey   []string
}

// Metadata is the scanned relations of one source, as the project declares them. A nil *Metadata
// means the project holds none (the source was never scanned); an empty one is a scan that found
// no table and no view.
type Metadata struct{ Relations []Relation }

// NotFoundError is a link to a table or view that the project's scanned metadata does not list
// (exit code 3). It names the kind of problem only, never the name asked for.
type NotFoundError struct{ Msg string }

func (e NotFoundError) Error() string { return e.Msg }

// find is the relation the request names: the name, and the schema when one is given. A request
// with no schema takes the relation of any schema.
func (m *Metadata) find(name, schema string, view bool) (found *Relation, kindMismatch bool) {
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Name != name || (schema != "" && r.Schema != schema) {
			continue
		}
		if r.View == view {
			return r, false
		}
		kindMismatch = true
	}
	return nil, kindMismatch
}

// check validates the request against the metadata: the object exists, --view matches its kind and
// the record's key columns are exactly its primary key. Every message names the kind of problem,
// never a name or a value.
func (m *Metadata) check(req LinkRequest, keys []string) error {
	rel, mismatch := m.find(req.Table, req.Schema, req.View)
	if rel == nil && mismatch {
		if req.View {
			return UsageError{"--view does not match: the object is a table"}
		}
		return UsageError{"--view is required: the object is a view"}
	}
	if rel == nil {
		return NotFoundError{"the table or view is not in the project's scanned metadata"}
	}
	if keys == nil {
		return nil
	}
	pk := map[string]bool{}
	for _, c := range rel.PrimaryKey {
		pk[c] = true
	}
	given := map[string]bool{}
	for _, k := range keys {
		if !pk[k] {
			return UsageError{"--record: not a primary-key column"}
		}
		given[k] = true
	}
	if len(given) != len(pk) {
		return UsageError{"--record: missing primary-key column"}
	}
	return nil
}

// keyNames are the column names of a canonical key (the object canonicalPK printed), sorted.
func keyNames(pk string) []string {
	var obj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(pk), &obj) // canonicalPK printed it: it always decodes
	names := make([]string, 0, len(obj))
	for k := range obj {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
