package chat

import (
	"slices"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
)

type inspectorColumnMeta struct {
	qualified string
	dbType    string
	objects   []string
}

// columnMetaFor is ui.go's original (*UI).columnMeta body, extracted so
// ChatUI (chatui_inspector.go) shares the exact same catalog-attribution
// logic without a second copy — both structs carry a ProjectCatalog under
// the same field name.
func columnMetaFor(catalog ProjectCatalog, record *RecordSet, name string) inspectorColumnMeta {
	meta := inspectorColumnMeta{}
	var sourceRelations map[string]bool
	shortName := name
	if last := strings.LastIndexByte(name, '.'); last >= 0 {
		shortName = name[last+1:]
	}
	if record != nil {
		if record.DTQL == "" {
			return meta
		}
		query, err := dtql.Deserialize([]byte(record.DTQL))
		if err != nil {
			return meta
		}
		// relationInstances' own walk always appends at least the root FROM
		// relation (dtql.Deserialize requires a FROM clause to succeed at
		// all), so instances is never empty for any document that reached
		// this point -- the r5 fix round (#289) removed that zero-instance
		// guard as provably unreachable, and it stays removed.
		//
		// The r5 round ALSO removed an "unqualified source with more than
		// one relation" guard on the (wrong) belief that DTQL has no
		// multi-relation FROM at all. That's false: DiscoverJoinCandidates/
		// deriveJoinDTQL (join.go) and the #291 attached-join widening
		// produce real joined DTQL documents with a `joins:` clause, and
		// relationInstances walks every one of them, so len(instances) > 1
		// is a real, reachable case for a query.Columns() field or wildcard
		// left unqualified (empty Source()) -- e.g. an unqualified
		// "CustomerId" projected from Invoice⋈Customer, where both sides
		// carry that column name. Without this guard, an unqualified
		// source matched EVERY relation instead of none, so the catalog
		// loop below matched both sides' same-named columns and reported a
		// false "ambiguous source" instead of leaving the column
		// unattributed (the r6 fix round restored this guard; see
		// TestColumnMetaForUnqualifiedFieldInJoinedQueryIsUnattributed).
		instances := relationInstances(query.From())
		allowed := func(source string) map[string]bool {
			matches := map[string]bool{}
			if source == "" && len(instances) != 1 {
				return matches
			}
			for _, instance := range instances {
				if source == "" || strings.EqualFold(source, instance.Alias) || strings.EqualFold(source, instance.Relation) {
					matches[relationKey(instance.Schema, instance.Relation)] = true
				}
			}
			return matches
		}
		if len(query.Columns()) == 0 {
			sourceRelations = allowed("")
		} else {
			matched := false
			for _, projection := range query.Columns() {
				if projection.Wildcard != nil {
					if slices.Contains(projection.Wildcard.Exclude, shortName) {
						continue
					}
					relations := allowed(projection.Wildcard.Source)
					if len(relations) == 1 {
						sourceRelations = relations
						matched = true
					}
					continue
				}
				field, ok := projection.Expression.(dal.FieldRef)
				if !ok {
					if projection.Alias == name {
						return meta // Derived value; a same-named physical field is not provenance.
					}
					continue
				}
				output := projection.Alias
				if output == "" {
					output = field.Name()
				}
				if !strings.EqualFold(output, name) {
					continue
				}
				if matched {
					return meta // Duplicate output names cannot be attributed safely.
				}
				shortName = field.Name()
				sourceRelations = allowed(field.Source())
				matched = true
			}
			if !matched {
				return meta
			}
		}
		if len(sourceRelations) == 0 {
			return meta
		}
	}
	for _, object := range catalog.Objects {
		if object.Reference.Kind != "table" && object.Reference.Kind != "project_view" {
			continue
		}
		if record != nil && record.Database != "" && object.Reference.SourceID != record.Database {
			continue
		}
		if len(sourceRelations) > 0 && !sourceRelations[strings.ToLower(object.Reference.ObjectID)] {
			continue
		}
		for _, column := range object.Columns {
			if !strings.EqualFold(column, shortName) {
				continue
			}
			qualified := object.Reference.ObjectID + "." + column
			meta.objects = append(meta.objects, qualified)
			if len(meta.objects) == 1 {
				meta.qualified = qualified
				meta.dbType = object.ColumnTypes[column]
			} else {
				meta.qualified = "ambiguous source"
				meta.dbType = ""
			}
		}
	}
	return meta
}
