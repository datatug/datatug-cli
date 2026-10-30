package daemon

import (
	"encoding/json"
	"sort"
)

// WarningNotScanned is the one warning of a link that was not checked against the project's
// scanned metadata: the project has none for the source (never scanned, or it cannot be read).
const WarningNotScanned = "table metadata not scanned: link not validated"

// Relation is one scanned table or view: its schema ("" for a schemaless source), its name and, for
// a table, its primary-key column names (none when the scan holds no key information).
type Relation struct {
	Schema, Name string
	View         bool
	PrimaryKey   []string
}

// Metadata is the scanned relations of one source, as the project declares them. A nil *Metadata
// means the project holds none (the source was never scanned); relations are those of the one name
// asked for, so an empty one is a scanned source without that name.
type Metadata struct{ Relations []Relation }

// NotFoundError is a link to a table or view that the project's scanned metadata does not list
// (exit code 3). It names the kind of problem only, never the name asked for.
type NotFoundError struct{ Msg string }

func (e NotFoundError) Error() string { return e.Msg }

// WarningKeyNotScanned is the warning of a record link to a table whose scan holds no primary-key
// information: the key is not validated.
const WarningKeyNotScanned = "primary key not in scanned metadata: key not validated"

// find is the relation the request names: the name, and the schema when one is given (compared
// exactly, case-sensitively, like the name; SQLite's one schema is "main"). A request with no
// schema takes the one relation of that name, and is ambiguous when several schemas hold one.
func (m *Metadata) find(name, schema string, view bool) (found *Relation, kindMismatch, ambiguous bool) {
	schemas := map[string]bool{}
	for i := range m.Relations {
		r := &m.Relations[i]
		if r.Name != name || (schema != "" && r.Schema != schema) {
			continue
		}
		schemas[r.Schema] = true
		if r.View == view {
			found = r
		} else {
			kindMismatch = true
		}
	}
	if schema == "" && len(schemas) > 1 {
		return nil, false, true
	}
	return found, found == nil && kindMismatch, false
}

// check validates the request against the metadata: the object exists, --view matches its kind and
// the record's key columns are exactly its primary key (not checked, with a warning, when the scan
// holds no primary-key information of the table; a view has none). Every message names the kind of
// problem, never a name or a value. Names are compared exactly, case-sensitively.
func (m *Metadata) check(req LinkRequest, keys []string) (warnings []string, err error) {
	rel, mismatch, ambiguous := m.find(req.Table, req.Schema, req.View)
	switch {
	case ambiguous:
		return nil, UsageError{"ambiguous: several schemas hold that name, give --schema"}
	case rel == nil && mismatch && req.View:
		return nil, UsageError{"--view does not match: the object is a table"}
	case rel == nil && mismatch:
		return nil, UsageError{"--view is required: the object is a view"}
	case rel == nil:
		return nil, NotFoundError{"the table or view is not in the project's scanned metadata"}
	case keys == nil:
		return nil, nil
	case rel.View:
		return nil, UsageError{"--record: a view has no primary key"}
	case len(rel.PrimaryKey) == 0:
		return []string{WarningKeyNotScanned}, nil
	}
	pk := map[string]bool{}
	for _, c := range rel.PrimaryKey {
		pk[c] = true
	}
	given := map[string]bool{}
	for _, k := range keys {
		if !pk[k] {
			return nil, UsageError{"--record: not a primary-key column"}
		}
		given[k] = true
	}
	if len(given) != len(pk) {
		return nil, UsageError{"--record: missing primary-key column"}
	}
	return nil, nil
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
