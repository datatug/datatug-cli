package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/semantic"
	"github.com/datatug/datatug-core/pkg/storage"
)

// recordsetDefinitionPath is the project's declared shape for a
// non-SQL (inGitDB-backed) collection: recordsets/<source>.recordset.json,
// per recordsets/README.md's own documented convention in
// datatug-demo-projects/demo-project-1 ("this file documents the shape; ...
// the actual records live ... in the inGitDB database").
func recordsetDefinitionPath(projectDir, source string) string {
	return filepath.Join(projectDir, storage.RecordsetsFolder, source+"."+storage.RecordsetFileSuffix+".json")
}

// resolvedSource is what sourceURL to open a collection through, and its
// schema translated into pkg/semantic's own types.
type resolvedSource struct {
	URL     string
	Columns []semantic.Column
	Schema  semantic.TableSchema
}

// resolveSource resolves (environment, source, collection) to a
// resolvedSource through pkg/api's unified resolver (resolver.go) — the
// SAME registry exec/run_query and every other execution path now uses
// (REQ:exact-transport-and-source-contract: "Semantic discovery and
// execution use the same authorized project source registry"). It replaces
// this file's previous own "<projectDir>/dbs/<source>.sqlite" guess (never
// matched by any real project — see the PR body's inventory) and its
// separate environment-blind inGitDB-only fallback.
func resolveSource(ctx context.Context, projStore datatug.ProjectStore, projectDir, environment, source, collection string) (resolvedSource, error) {
	resolved, err := api.ResolveSource(ctx, projStore, projectDir, environment, source)
	if err != nil {
		return resolvedSource{}, newSourceUnavailable(err.Error())
	}
	switch resolved.Kind {
	case api.SourceKindSQL:
		return resolveSQLSourceURL(ctx, resolved.URL, collection)
	case api.SourceKindInGitDB:
		recordsetPath := recordsetDefinitionPath(projectDir, resolved.ID)
		if !fileExists(recordsetPath) {
			return resolvedSource{}, newSourceUnavailable(fmt.Sprintf("source %q has no recordset definition at %s", source, recordsetPath))
		}
		return resolveRecordsetSource(resolved.URL, recordsetPath, collection)
	case api.SourceKindHTTP:
		return resolveHTTPSource(projectDir, resolved.ID, collection)
	default:
		return resolvedSource{}, newSourceUnavailable(fmt.Sprintf("source %q has an unsupported kind %q", source, resolved.Kind))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveSQLSourceURL is resolveSQLSource's own logic, taking an
// already-resolved sqlite:// URL (api.ResolvedSource.URL) instead of
// building one from a bare filesystem path itself.
func resolveSQLSourceURL(ctx context.Context, sourceURL, collection string) (resolvedSource, error) {
	ref, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return resolvedSource{}, err
	}
	db, err := ref.Open(ctx)
	if err != nil {
		if errors.Is(err, dbcopy.ErrSourceFileMissing) {
			return resolvedSource{}, newSourceUnavailable(err.Error())
		}
		return resolvedSource{}, fmt.Errorf("open %s: %w", sourceURL, err)
	}
	reader, ok := dal.As[dbschema.SchemaReader](db)
	if !ok {
		return resolvedSource{}, fmt.Errorf("%s does not support schema introspection", sourceURL)
	}
	collRef := dal.NewRootCollectionRef(collection, "")
	def, err := reader.DescribeCollection(ctx, &collRef)
	if err != nil {
		return resolvedSource{}, fmt.Errorf("describe %s.%s: %w", sourceURL, collection, err)
	}
	columns := make([]semantic.Column, len(def.Fields))
	for i, f := range def.Fields {
		columns[i] = semantic.Column{Name: string(f.Name), Type: f.Type.String()}
	}
	var primaryKey *datatug.UniqueKey
	if len(def.PrimaryKey) > 0 {
		cols := make([]string, len(def.PrimaryKey))
		for i, c := range def.PrimaryKey {
			cols[i] = string(c)
		}
		primaryKey = &datatug.UniqueKey{Name: "primary", Columns: cols}
	}
	var referencedBy datatug.ReferencedBys
	referrers, err := reader.ListReferrers(ctx, &collRef)
	if err != nil && !errors.Is(err, dal.ErrNotSupported) {
		return resolvedSource{}, fmt.Errorf("list referrers for %s.%s: %w", sourceURL, collection, err)
	}
	for _, ref := range referrers {
		cols := make([]string, len(ref.Fields))
		for i, f := range ref.Fields {
			cols[i] = string(f)
		}
		referencedBy = append(referencedBy, &datatug.ReferencedBy{
			DBCollectionKey: datatug.NewTableKey(ref.Collection.Name(), "", "", nil),
			ForeignKeys: []*datatug.RefByForeignKey{{
				Name:    "referrer:" + ref.Collection.Name(),
				Columns: cols,
			}},
		})
	}
	// ForeignKeys (this collection's own OUTGOING foreign keys) is left
	// empty: dbschema.ConstraintDef (Tier 1) reports that a "foreign-key"
	// constraint exists by name only, with no referenced-table/columns —
	// there is nothing here to build a valid semantic.Lookup target from.
	// LookupReferencedBy (populated above via ListReferrers) is what the
	// demo project's own scenario (Customer -> Invoice) needs; outgoing
	// LookupForeignKey lookups will start working the moment dbschema grows
	// richer FK constraint data, with no change needed here.
	return resolvedSource{
		URL:     sourceURL,
		Columns: columns,
		Schema: semantic.TableSchema{
			PrimaryKey:   primaryKey,
			ReferencedBy: referencedBy,
		},
	}, nil
}

// resolveRecordsetSource reads recordsetPath's declared schema for an
// inGitDB-backed collection. sourceURL is api.ResolvedSource.URL (the
// project's shared ingitdb:// store) — resolveRecordsetSource no longer
// builds it itself. collection is currently unused (a recordset definition
// declares exactly one collection, itself); kept as a parameter for
// symmetry with resolveSQLSourceURL and so a future multi-collection
// recordset definition needs no signature change here.
func resolveRecordsetSource(sourceURL, recordsetPath, collection string) (resolvedSource, error) {
	_ = collection
	data, err := os.ReadFile(recordsetPath)
	if err != nil {
		return resolvedSource{}, fmt.Errorf("read %s: %w", recordsetPath, err)
	}
	var def datatug.RecordsetDefinition
	if err := json.Unmarshal(data, &def); err != nil {
		return resolvedSource{}, fmt.Errorf("parse %s: %w", recordsetPath, err)
	}
	columns := make([]semantic.Column, len(def.Columns))
	for i, c := range def.Columns {
		columns[i] = semantic.Column{Name: c.Name, Type: c.Type}
	}
	var primaryKey *datatug.UniqueKey
	if def.PrimaryKey != nil {
		primaryKey = &datatug.UniqueKey{
			Name: def.PrimaryKey.Name, Columns: def.PrimaryKey.Columns, IsClustered: def.PrimaryKey.IsClustered,
		}
	}
	foreignKeys := make(datatug.ForeignKeys, len(def.ForeignKeys))
	for i, fk := range def.ForeignKeys {
		foreignKeys[i] = &datatug.ForeignKey{
			Name:        fk.Name,
			Columns:     fk.Columns,
			RefTable:    datatug.NewTableKey(fk.RefTable.Name(), fk.RefTable.Schema(), fk.RefTable.Catalog(), nil),
			MatchOption: fk.MatchOption,
			UpdateRule:  fk.UpdateRule,
			DeleteRule:  fk.DeleteRule,
		}
	}
	// RecordsetDefinition (RecordsetBaseDef) declares only a collection's OWN
	// outgoing ForeignKeys, never "who references me" — there is no
	// ReferencedBy field to convert here. That is not a gap for this path:
	// support-notes's connection to Customer is the sameField (mapping-based)
	// kind, resolved from EntityField.Mappings, not from this schema at all.
	return resolvedSource{
		URL:     sourceURL,
		Columns: columns,
		Schema: semantic.TableSchema{
			PrimaryKey:  primaryKey,
			ForeignKeys: foreignKeys,
		},
	}, nil
}

// resolveHTTPSource shapes an HTTP QueryDef's own declared recordset
// columns (Recordsets[0].Columns[].Meta) into a resolvedSource: HTTP
// QueryDefs carry no EntityField.Mappings/NamePatterns wiring of their own
// (task 4's demo entities map Country.Name to chinook/support-notes
// columns only; a query's response columns are typed and Meta-tagged
// directly on the QueryDef instead — see datatug-demo-projects/demo-
// project-1's queries/reference/country-facts.query.json), so this is a
// different, simpler resolution path than resolveSQLSourceURL/
// resolveRecordsetSource's declared-Mappings-or-NamePatterns pipeline: a
// Meta-tagged response column is reported "declared" directly (Task 12's
// own engineering decision — the appendix names no HTTP-specific column-
// resolution rule).
func resolveHTTPSource(projectDir, queryID, collection string) (resolvedSource, error) {
	_ = collection
	queries, err := loadModuleQueries(projectDir)
	if err != nil {
		return resolvedSource{}, err
	}
	for _, q := range queries {
		if q == nil || q.ID != queryID {
			continue
		}
		if len(q.Recordsets) == 0 {
			return resolvedSource{}, nil
		}
		var columns []semantic.Column
		for _, c := range q.Recordsets[0].Columns {
			columns = append(columns, semantic.Column{Name: c.Name, Type: c.Type})
		}
		return resolvedSource{Columns: columns}, nil
	}
	return resolvedSource{}, newSourceUnavailable(fmt.Sprintf("HTTP query %q not found", queryID))
}

// httpDeclaredColumns returns queryID's declared recordset column ->
// {entity,field} map, for computeSemanticColumns' HTTP branch (which skips
// pkg/semantic.Resolve entirely — see resolveHTTPSource's doc comment).
func httpDeclaredColumns(projectDir, queryID string) (map[string]datatug.EntityFieldRef, error) {
	queries, err := loadModuleQueries(projectDir)
	if err != nil {
		return nil, err
	}
	out := map[string]datatug.EntityFieldRef{}
	for _, q := range queries {
		if q == nil || q.ID != queryID || len(q.Recordsets) == 0 {
			continue
		}
		for _, c := range q.Recordsets[0].Columns {
			if c.Meta != nil {
				out[c.Name] = *c.Meta
			}
		}
	}
	return out, nil
}
