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
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/storage"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	moduledatatug "github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/semantic"
)

// sqliteFilePath is this package's own convention for finding a SQL source's
// physical file: <projectDir>/dbs/<source>.sqlite. It exists because, as
// verified while building these endpoints, this codebase currently has no
// working "source name -> connection" registry for SQL sources — a
// project's DbModels/Environments carry only catalog ID strings, never a
// path or connection string (see cmd_demo.go's legacy, unrelated download
// flow, and pkg/api/scan_db_schema_api.go's scanDbCatalog, which both expect
// that path to be supplied out of band, not read from a project file). This
// convention is intentionally the simplest thing that could work, and
// applies ONLY within these new endpoints; see the PR body for the demo
// project's current gap (it has no physical dbs/chinook.sqlite yet).
func sqliteFilePath(projectDir, source string) string {
	return filepath.Join(projectDir, storage.DbsFolder, source+".sqlite")
}

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

// resolveSource resolves (source, collection) to a resolvedSource: SQL when
// sqliteFilePath(projectDir, source) exists (schema read live through
// dal-go/dalgo/dbschema.SchemaReader — dalgo2sqlite implements it, pure Go,
// no cgo, unlike the legacy pkg/schemers/sqliteschema this repo's own
// pkg/api/scan_db_schema_api.go uses), otherwise inGitDB, assuming source
// names a recordsets/<source>.recordset.json definition whose declared
// Columns/PrimaryKey/ForeignKeys supply the schema directly (inGitDB itself
// has no schema-introspection capability to fall back to).
func resolveSource(ctx context.Context, projectDir, source, collection string) (resolvedSource, error) {
	if sqlPath := sqliteFilePath(projectDir, source); fileExists(sqlPath) {
		return resolveSQLSource(ctx, sqlPath, collection)
	}
	if recordsetPath := recordsetDefinitionPath(projectDir, source); fileExists(recordsetPath) {
		return resolveRecordsetSource(projectDir, recordsetPath, source, collection)
	}
	return resolvedSource{}, newFieldError("source", fmt.Sprintf(
		"unknown source %q: no %s and no %s", source, sqliteFilePath(projectDir, source), recordsetDefinitionPath(projectDir, source),
	))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveSQLSource(ctx context.Context, sqlPath, collection string) (resolvedSource, error) {
	sourceURL := "sqlite://" + sqlPath
	ref, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return resolvedSource{}, err
	}
	db, err := ref.Open(ctx)
	if err != nil {
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
	var primaryKey *moduledatatug.UniqueKey
	if len(def.PrimaryKey) > 0 {
		cols := make([]string, len(def.PrimaryKey))
		for i, c := range def.PrimaryKey {
			cols[i] = string(c)
		}
		primaryKey = &moduledatatug.UniqueKey{Name: "primary", Columns: cols}
	}
	var referencedBy moduledatatug.ReferencedBys
	referrers, err := reader.ListReferrers(ctx, &collRef)
	if err != nil && !errors.Is(err, dal.ErrNotSupported) {
		return resolvedSource{}, fmt.Errorf("list referrers for %s.%s: %w", sourceURL, collection, err)
	}
	for _, ref := range referrers {
		cols := make([]string, len(ref.Fields))
		for i, f := range ref.Fields {
			cols[i] = string(f)
		}
		referencedBy = append(referencedBy, &moduledatatug.ReferencedBy{
			DBCollectionKey: moduledatatug.NewTableKey(ref.Collection.Name(), "", "", nil),
			ForeignKeys: []*moduledatatug.RefByForeignKey{{
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

func resolveRecordsetSource(projectDir, recordsetPath, source, collection string) (resolvedSource, error) {
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
	var primaryKey *moduledatatug.UniqueKey
	if def.PrimaryKey != nil {
		primaryKey = &moduledatatug.UniqueKey{
			Name: def.PrimaryKey.Name, Columns: def.PrimaryKey.Columns, IsClustered: def.PrimaryKey.IsClustered,
		}
	}
	foreignKeys := make(moduledatatug.ForeignKeys, len(def.ForeignKeys))
	for i, fk := range def.ForeignKeys {
		foreignKeys[i] = &moduledatatug.ForeignKey{
			Name:        fk.Name,
			Columns:     fk.Columns,
			RefTable:    moduledatatug.NewTableKey(fk.RefTable.Name, fk.RefTable.Schema, fk.RefTable.Catalog, nil),
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
		URL:     semanticIngitdbPath(projectDir),
		Columns: columns,
		Schema: semantic.TableSchema{
			PrimaryKey:  primaryKey,
			ForeignKeys: foreignKeys,
		},
	}, nil
}
