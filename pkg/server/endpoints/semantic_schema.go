package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

var apiResolveSource = api.ResolveSource
var recordsetUnmarshalJSON = json.Unmarshal
var dalAsSchemaReader = dal.As[dbschema.SchemaReader]
var dbcopyParse = dbcopy.Parse

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
	resolved, err := apiResolveSource(ctx, projStore, projectDir, environment, source)
	if err != nil {
		return resolvedSource{}, newSourceUnavailable(err.Error())
	}
	switch resolved.Kind {
	case api.SourceKindSQL:
		return resolveSQLSourceURL(ctx, resolved.ID, resolved.URL, collection)
	case api.SourceKindInGitDB:
		// The ID of the source is joined into the path of its definition, and it is read out
		// of a file of the project (the ID of a recordset definition, the model of a catalog),
		// which may hold anything: only a plain source ID is a name of a file in the folder of
		// the recordsets.
		if !dbcopy.IsPlainSourceID(resolved.ID) {
			return resolvedSource{}, recordsetUnavailable(resolved.ID, errors.New("the ID of the source is not a plain source ID"))
		}
		recordsetPath := recordsetDefinitionPath(projectDir, resolved.ID)
		if !fileExists(recordsetPath) {
			return resolvedSource{}, recordsetUnavailable(resolved.ID, fmt.Errorf("%s: %w", recordsetPath, os.ErrNotExist))
		}
		definition, err := resolveRecordsetSource(resolved.URL, recordsetPath, collection)
		if err != nil {
			return resolvedSource{}, recordsetUnavailable(resolved.ID, err)
		}
		return definition, nil
	case api.SourceKindHTTP:
		return resolveHTTPSource(projectDir, resolved.ID, collection)
	default:
		return resolvedSource{}, newSourceUnavailable(fmt.Sprintf("source %q has an unsupported kind %q", resolved.ID, resolved.Kind))
	}
}

// unavailableOr answers a failure of the source whose ID is sourceID that dbcopy built a sentence for (a source that
// is refused, whose data file is not there, that cannot be opened, or whose connection was lost) as SOURCE_UNAVAILABLE
// with the sentence of api.SourceUnavailable, which names the source by its ID and never by the display form that
// dbcopy's own sentence holds (that is logged), and any other failure as otherwise.
func unavailableOr(sourceID string, cause, otherwise error) error {
	if unavailable := api.SourceUnavailable(sourceID, cause); unavailable != nil {
		return newSourceUnavailable(unavailable.Error())
	}
	return otherwise
}

// sourceUnavailableAnswer answers err, a failure of the source whose ID is sourceID, as SOURCE_UNAVAILABLE with a
// sentence built from the ID (see api.SourceUnavailable).
func sourceUnavailableAnswer(sourceID string, err error) *contractError {
	return newSourceUnavailable(api.SourceUnavailable(sourceID, err).Error())
}

// recordsetUnavailable is the answer for a source whose recordset definition cannot be used:
// it is not there, it is not a file, it cannot be read, it cannot be parsed, or the ID of the
// source is not a name of a file. They are one answer, a sentence built from the source (named
// when its ID is a plain name) and from nothing the file system said, which quotes a path of
// the server and says whether it is missing, a file or a folder. The cause goes to the log.
func recordsetUnavailable(sourceID string, cause error) *contractError {
	shown := dbcopy.SourceIDDisplay(sourceID)
	log.Printf("semantic: the recordset definition of source %q cannot be used: %s", shown, dbcopy.RedactText(cause.Error()))
	return newSourceUnavailable(fmt.Sprintf("source %q has no readable recordset definition", shown))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveSQLSourceURL is resolveSQLSource's own logic, taking an
// already-resolved sqlite:// URL (api.ResolvedSource.URL) instead of
// building one from a bare filesystem path itself.
func resolveSQLSourceURL(ctx context.Context, sourceID, sourceURL, collection string) (resolvedSource, error) {
	ref, err := dbcopyParse(sourceURL)
	if err != nil {
		return resolvedSource{}, err
	}
	// shown is the source in a form safe for an error text: a password inside
	// a source URL must never reach an error, a log or a client.
	shown := ref.String()
	db, err := ref.Open(ctx)
	if err != nil {
		// A source that is refused, whose data file is not there or that cannot be opened is
		// unavailable. A file source is named in the answer by its ID: Open's own error holds the
		// display form of the source (the path of a file), and goes to the log. A PostgreSQL source
		// names no source in its text: the adapter's sentence for the failure and where its
		// connection string is read from (the variable), no host, port or database; so do its fixed
		// refusals (dbcopy.ErrPostgresPreview while the preview is off, and the refusal of a URL that
		// turns the read-only session off). They are answered, and logged, as they are.
		return resolvedSource{}, unavailableOr(sourceID, err, err)
	}
	reader, ok := dalAsSchemaReader(db)
	if !ok {
		return resolvedSource{}, fmt.Errorf("%s does not support schema introspection", shown)
	}
	collRef := dal.NewRootCollectionRef(collection, "")
	def, err := reader.DescribeCollection(ctx, &collRef)
	if err != nil {
		return resolvedSource{}, unavailableOr(sourceID, err, fmt.Errorf("describe %s.%s: %w", shown, collection, dbcopy.RedactError(err)))
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
		return resolvedSource{}, unavailableOr(sourceID, err, fmt.Errorf("list referrers for %s.%s: %w", shown, collection, dbcopy.RedactError(err)))
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
	if err := recordsetUnmarshalJSON(data, &def); err != nil {
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
		var refTable datatug.DBCollectionKey
		if name := fk.RefTable.Name(); name != "" {
			refTable = datatug.NewTableKey(name, fk.RefTable.Schema(), fk.RefTable.Catalog(), nil)
		}
		foreignKeys[i] = &datatug.ForeignKey{
			Name:        fk.Name,
			Columns:     fk.Columns,
			RefTable:    refTable,
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
	queries, _, err := loadModuleQueries(projectDir)
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
	queries, _, err := loadModuleQueries(projectDir)
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
