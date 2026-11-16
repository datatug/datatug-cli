package datarolesfile

import (
	"sort"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dataroles"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// Collections is the schema-only shape of a DB model's stored tables for
// dataroles.Suggest: columns with their primary-key flag (the columns file's
// pkPosition) and single-column unique flag (the model's indexes file), and
// counts of the foreign keys each table holds and receives (the model's
// foreign-keys file). It reads no record values. Views are left out: they
// hold no data of their own.
func Collections(schema *api.CatalogSchema, foreignKeys []datatug.TableForeignKeys, indexes []datatug.TableIndexes) []dataroles.Collection {
	unique := map[string]map[string]bool{}
	for _, table := range indexes {
		for _, index := range table.Indexes {
			if index == nil || !index.IsUnique && !index.IsUniqueConstraint && !index.IsPrimaryKey || len(index.Columns) != 1 {
				continue
			}
			if unique[table.Table.Name()] == nil {
				unique[table.Table.Name()] = map[string]bool{}
			}
			unique[table.Table.Name()][index.Columns[0].Name] = true
		}
	}
	references, referencedBy := map[string]int{}, map[string]int{}
	for _, table := range foreignKeys {
		for _, key := range table.ForeignKeys {
			references[table.Table.Name()]++
			referencedBy[key.RefTable.Name()]++
		}
	}
	out := []dataroles.Collection{}
	for _, relation := range schema.Relations {
		if relation.DbType != "BASE TABLE" {
			continue
		}
		collection := dataroles.Collection{Name: relation.Name, Columns: []dataroles.Column{}, References: references[relation.Name], ReferencedBy: referencedBy[relation.Name]}
		for _, column := range relation.Columns {
			collection.Columns = append(collection.Columns, dataroles.Column{
				Name: column.Name, Type: column.DbType, PrimaryKey: column.PrimaryKeyPosition > 0, Unique: unique[relation.Name][column.Name],
			})
		}
		out = append(out, collection)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
