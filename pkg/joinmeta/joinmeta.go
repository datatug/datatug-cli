// Package joinmeta reads the relations a DTQL query reads and the foreign keys
// of a database, from the query's own tree and from DALgo's engine-neutral
// schema calls (dbschema.ListCollections and DescribeCollection), never from
// engine-specific SQL. The terminal chat and `query run --result-id` share it.
package joinmeta

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// MaxCollections bounds how many collections one snapshot describes.
const MaxCollections = 500

// ErrTooManyCollections is returned when a database holds more collections
// than MaxCollections.
var ErrTooManyCollections = errors.New("the database holds more collections than are described")

// RelationInstanceID identifies a node in the DTQL source tree. It is stable
// for a given document: root is "root" and child joins append their index.
type RelationInstanceID string

// RelationInstance is one source occurrence of a query.
type RelationInstance struct {
	ID       RelationInstanceID
	Schema   string
	Relation string
	Alias    string
	// Database is the named database of a federated query's source, else "".
	Database string
}

// ForeignKey is source-scoped schema evidence. Fields are paired by index and
// are intentionally not flattened: a composite FK remains one relationship.
type ForeignKey struct {
	ConstraintID string
	Schema       string
	FromRelation string
	FromFields   []string
	ToSchema     string
	ToRelation   string
	ToFields     []string
}

// ForeignKeySnapshot is the foreign keys of one source.
type ForeignKeySnapshot struct {
	Source string
	Keys   []ForeignKey
	// Columns contains the source's ordered physical columns, keyed by
	// lowercase schema.relation. It is used to expand wildcards before a JOIN.
	Columns map[string][]string
	// Definitions is each described collection, keyed like Columns.
	Definitions map[string]*dbschema.CollectionDef
	// Views is the collections the source reports as views, keyed like Columns.
	Views map[string]bool
}

// RelationKey is the key of schema.relation in a snapshot; an empty schema is
// the source's default one.
func RelationKey(schema, relation string) string {
	if schema == "" {
		schema = "main"
	}
	return strings.ToLower(schema + "." + relation)
}

// Kind is "view", "table" or "unknown" for schema.relation of the snapshot.
func (s ForeignKeySnapshot) Kind(schema, relation string) string {
	key := RelationKey(schema, relation)
	switch {
	case s.Views[key]:
		return "view"
	case s.Definitions[key] != nil:
		return "table"
	default:
		return "unknown"
	}
}

// Seams over the dbschema calls, so tests reach the failures a fixture cannot.
var (
	listCollections    = dbschema.ListCollections
	describeCollection = dbschema.DescribeCollection
)

// LoadForeignKeySnapshot describes every collection of db through dalgo's
// schema reader: each collection's fields and primary key, and each declared
// foreign key. A foreign key whose referenced fields are not reported points
// at the target's primary key. Describing every collection yields every
// incoming key too. A provider without a schema reader answers a
// *dbschema.NotSupportedError.
func LoadForeignKeySnapshot(ctx context.Context, db dal.DB, source string) (ForeignKeySnapshot, error) {
	if db == nil || source == "" {
		return ForeignKeySnapshot{}, fmt.Errorf("foreign-key metadata requires a source and database")
	}
	refs, err := listCollections(ctx, db, nil)
	if err != nil {
		return ForeignKeySnapshot{}, err
	}
	if len(refs) > MaxCollections {
		return ForeignKeySnapshot{}, ErrTooManyCollections
	}
	snapshot := ForeignKeySnapshot{Source: source, Keys: []ForeignKey{}, Columns: map[string][]string{}, Definitions: map[string]*dbschema.CollectionDef{}, Views: map[string]bool{}}
	if reader, ok := dal.As[dbschema.SourceViewReader](db); ok {
		if views, viewErr := reader.ListSourceViews(ctx); viewErr == nil {
			for _, view := range views {
				snapshot.Views[RelationKey("", view.Name)] = true
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		return RelationKey(refs[i].Schema(), refs[i].Name()) < RelationKey(refs[j].Schema(), refs[j].Name())
	})
	type pending struct {
		schema string
		def    *dbschema.CollectionDef
	}
	described := make([]pending, 0, len(refs))
	for i := range refs {
		def, err := describeCollection(ctx, db, &refs[i])
		if err != nil {
			return ForeignKeySnapshot{}, err
		}
		key := RelationKey(refs[i].Schema(), refs[i].Name())
		snapshot.Definitions[key] = def
		for _, field := range def.Fields {
			snapshot.Columns[key] = append(snapshot.Columns[key], string(field.Name))
		}
		described = append(described, pending{schema: refs[i].Schema(), def: def})
	}
	sqlite := isSQLite(db)
	for _, entry := range described {
		for i, fk := range entry.def.ForeignKeys {
			schema := entry.schema
			toSchema := fk.ReferencedNamespace
			if toSchema == "" {
				toSchema = schema
			}
			if sqlite {
				schema, toSchema = schemaOrMain(schema), schemaOrMain(toSchema)
			}
			id := constraintID(sqlite, schema, entry.def.Name, fk.Name, i)
			key := ForeignKey{ConstraintID: id, Schema: schema, FromRelation: entry.def.Name, ToSchema: toSchema, ToRelation: fk.ReferencedCollection}
			for _, field := range fk.Fields {
				key.FromFields = append(key.FromFields, string(field))
			}
			for _, field := range fk.ReferencedFields {
				key.ToFields = append(key.ToFields, string(field))
			}
			if len(key.ToFields) == 0 {
				target := snapshot.Definitions[RelationKey(toSchema, fk.ReferencedCollection)]
				if target == nil || len(target.PrimaryKey) != len(key.FromFields) {
					return ForeignKeySnapshot{}, fmt.Errorf("foreign key %s has no resolvable target primary key", id)
				}
				for _, field := range target.PrimaryKey {
					key.ToFields = append(key.ToFields, string(field))
				}
			}
			snapshot.Keys = append(snapshot.Keys, key)
		}
	}
	return snapshot, nil
}

// sqliteAdapter is the name dalgo2sqlite reports for its adapter.
const sqliteAdapter = "dalgo2sqlite"

func isSQLite(db dal.DB) bool {
	adapter := db.Adapter()
	return adapter != nil && adapter.Name() == sqliteAdapter
}

func schemaOrMain(schema string) string {
	if schema == "" {
		return "main"
	}
	return schema
}

// constraintID is a foreign key's ID, unique within its database, which chat
// sessions store with every applied JOIN, so it must not change across
// versions. A SQLite key keeps the "sqlite:<table>:<id>" of the PRAGMA reader
// it replaces (dalgo2sqlite names it "<table>_fk_<id>"). Any other engine's
// constraint name is only unique per table, so it is qualified by the table;
// a key with no name gets its position.
func constraintID(sqlite bool, schema, table, name string, i int) string {
	if sqlite {
		if n, ok := strings.CutPrefix(name, table+"_fk_"); ok && n != "" && strings.Trim(n, "0123456789") == "" {
			return fmt.Sprintf("sqlite:%s:%s", table, n)
		}
		return fmt.Sprintf("sqlite:%s:fk%d", table, i)
	}
	if name == "" {
		name = fmt.Sprintf("fk:%d", i)
	}
	if schema == "" {
		return table + ":" + name
	}
	return schema + "." + table + ":" + name
}

// RelationInstances walks a DTQL FROM/JOIN tree into its source occurrences,
// root first, each join's subtree in order.
func RelationInstances(from dal.FromSource) []RelationInstance {
	if from == nil {
		return nil
	}
	var out []RelationInstance
	var walk func(dal.FromSource, string)
	walk = func(node dal.FromSource, path string) {
		base := node.Base()
		instance := RelationInstance{ID: RelationInstanceID(path), Relation: base.Name(), Alias: base.Alias()}
		if qualified, ok := base.(dal.CollectionRef); ok {
			instance.Schema = qualified.Schema()
			instance.Database = qualified.Database()
		}
		out = append(out, instance)
		for i, join := range node.Joins() {
			child := join.From()
			if child == nil {
				child = dal.From(join.RecordsetSource)
			}
			walk(child, fmt.Sprintf("%s/%d", path, i))
		}
	}
	walk(from, "root")
	return out
}
