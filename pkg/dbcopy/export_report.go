package dbcopy

import (
	"errors"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/dbcopy/filter"
)

// ErrNoCollectionsSelected is returned by the native export when --include and --exclude leave no collection.
var ErrNoCollectionsSelected = errors.New("no collection is selected; nothing to export")

// ExportTable is what the native export did to one collection.
type ExportTable struct {
	Name              string
	Rows              int64
	Columns           int
	PrimaryKey        []string
	ForeignKeys       int
	Indexes           int
	UniqueConstraints int
}

// ExportWarning is a thing the native export did not carry exactly. Kind is a stable word, Object
// names the collection or foreign key it is about.
type ExportWarning struct {
	Kind    string
	Object  string
	Message string
}

// ExportResult is what the native inGitDB export did (with DryRun, what it would do).
type ExportResult struct {
	Tables   []ExportTable
	Warnings []ExportWarning
}

func exportTableReport(def dbschema.CollectionDef, rows int64) ExportTable {
	table := ExportTable{Name: def.Name, Rows: rows, Columns: len(def.Fields), PrimaryKey: []string{}, ForeignKeys: len(def.ForeignKeys)}
	for _, field := range def.PrimaryKey {
		table.PrimaryKey = append(table.PrimaryKey, string(field))
	}
	for _, index := range def.Indexes {
		if index.Unique {
			table.UniqueConstraints++
		} else {
			table.Indexes++
		}
	}
	return table
}

// checkFilterTables refuses a --where or --limit that names a collection the export does not write.
func checkFilterTables(filters *filter.Directives, selected map[string]bool) error {
	if filters == nil {
		return nil
	}
	var missing []string
	for name := range filters.Where {
		if !selected[name] {
			missing = append(missing, "--where "+name)
		}
	}
	for name := range filters.LimitsByTable {
		if !selected[name] {
			missing = append(missing, "--limit "+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%v names a table that is not exported (absent from the source, or left out by --include/--exclude)", missing)
}

// dropUnselectedForeignKeys is the foreign keys of def whose target collection is exported. A key to a
// collection that --include/--exclude left out cannot be carried, and is warned about; a key to a
// collection the source does not have is kept, for the relationship check to refuse.
func dropUnselectedForeignKeys(def *dbschema.CollectionDef, selected, all map[string]bool, result *ExportResult) []dbschema.ForeignKeyDef {
	if len(def.ForeignKeys) == 0 {
		return def.ForeignKeys
	}
	kept := make([]dbschema.ForeignKeyDef, 0, len(def.ForeignKeys))
	for _, fk := range def.ForeignKeys {
		if !selected[fk.ReferencedCollection] && all[fk.ReferencedCollection] {
			result.Warnings = append(result.Warnings, ExportWarning{
				Kind: "foreign-key-dropped", Object: def.Name + "." + fk.Name,
				Message: fmt.Sprintf("references %q, which is left out by --include/--exclude; the key is not carried", fk.ReferencedCollection),
			})
			continue
		}
		kept = append(kept, fk)
	}
	return kept
}

// warnFilteredParents warns about every kept foreign key whose target collection has its rows narrowed by
// --where or --limit: rows of the referencing collection may point at rows the export leaves out.
func warnFilteredParents(defs []exportTable, filters *filter.Directives, result *ExportResult) {
	if filters == nil {
		return
	}
	for _, table := range defs {
		for _, fk := range table.def.ForeignKeys {
			_, where := filters.Where[fk.ReferencedCollection]
			_, limit := filters.LimitsByTable[fk.ReferencedCollection]
			if where || limit {
				result.Warnings = append(result.Warnings, ExportWarning{
					Kind: "filtered-parent", Object: table.def.Name + "." + fk.Name,
					Message: fmt.Sprintf("rows of %q are narrowed by --where/--limit, so rows of %q may reference rows that are not exported", fk.ReferencedCollection, table.def.Name),
				})
			}
		}
	}
}

// applyExportRowFilters adds the --where predicates and the --limit of def's collection to builder. The
// bool says that rows must be read through the query, which the native row cursor cannot filter.
func applyExportRowFilters(builder dal.IQueryBuilder, source dal.DB, def dbschema.CollectionDef, filters *filter.Directives) (dal.IQueryBuilder, bool, error) {
	if filters == nil {
		return builder, false, nil
	}
	narrowed := false
	if group := filters.Where[def.Name]; group != nil {
		conds, err := filter.CompileWhereForTable(def.Name, group, &def)
		if err != nil {
			return builder, false, fmt.Errorf("compile where for %q: %w", def.Name, err)
		}
		if adapterName(source) == sqliteAdapterName {
			conds = sqliteTextTimeConstants(conds)
		}
		if len(conds) > 0 {
			builder = builder.Where(conds...)
			narrowed = true
		}
	}
	if n := filters.LimitsByTable[def.Name]; n > 0 {
		builder = builder.Limit(n)
		narrowed = true
	}
	return builder, narrowed, nil
}
