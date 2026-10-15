package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
)

const (
	schemaKindTable = "table"
	schemaKindView  = "view"

	// sqliteSchemaName is the one schema SQLite has.
	sqliteSchemaName = "main"
)

// schemaProviders are the providers `db schema` lists, in the order the
// unsupported-provider error names them.
var schemaProviders = []string{"sqlite", "postgres"}

// schemaEntry is one table or view of the listing.
type schemaEntry struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

// schemaListing is the JSON document `db schema` prints.
type schemaListing struct {
	Provider string        `json:"provider"`
	Target   string        `json:"target"`
	Tables   []schemaEntry `json:"tables"`
}

// postgresSchemaReader is what a PostgreSQL source offers beyond dbschema: its
// schemas and, per schema, the collections (views included) and the views.
type postgresSchemaReader interface {
	ListSchemas(ctx context.Context) ([]string, error)
	ListSchemaCollections(ctx context.Context, schema string) ([]dal.CollectionRef, error)
	ListSchemaViews(ctx context.Context, schema string) ([]dal.CollectionRef, error)
}

// openSchemaSource opens the source read-only; a package var so tests can
// substitute a fake database.
var openSchemaSource = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
	return ref.OpenProtected(ctx)
}

// dbSchemaCommand wires `datatug db schema --db <url> [--format json|text]`.
func dbSchemaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "List the tables and views of a database (read-only)",
		Long: "Opens a database from a connection string, detects the provider from it and prints its tables and views, sorted by schema, kind and name. " +
			"Supported providers: " + strings.Join(schemaProviders, ", ") + ". " +
			"--db takes " + dbSchemesHelp() + ".",
		Example: "  datatug db schema --db sqlite://./chinook.sqlite --format json\n" +
			"  DATATUG_OPEN_DB=sqlite://./chinook.sqlite datatug db schema --db env:DATATUG_OPEN_DB",
		Args: cobra.NoArgs,
		RunE: dbSchemaAction,
	}
	cmd.Flags().String("db", "", "Database URL: "+dbSchemesHelp())
	cmd.Flags().String("format", "json", "Output format: json or text")
	return cmd
}

func dbSchemaAction(cmd *cobra.Command, _ []string) error {
	dbArg, _ := cmd.Flags().GetString("db")
	format, _ := cmd.Flags().GetString("format")
	if format != "json" && format != "text" {
		return Exit(fmt.Sprintf("unsupported --format %q: want one of json, text", format), exitCodeUsage)
	}
	ref, err := dbcopy.Parse(dbArg)
	if err != nil {
		return Exit(err.Error(), exitCodeUsage)
	}
	ref = ref.WithFlag("--db")
	if !isSchemaProvider(ref.Scheme) {
		return Exit(fmt.Sprintf("unsupported provider %q for db schema: supported providers are %s", ref.Scheme, strings.Join(schemaProviders, ", ")), exitCodeUsage)
	}
	if err = dbcopy.CheckPostgresRead(ref, 0); err != nil {
		return Exit(err.Error(), exitCodeDatabase)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := openSchemaSource(ctx, ref)
	if err != nil {
		return Exit(ref.OpenFailure(err).Error(), exitCodeDatabase)
	}
	entries, err := listSchemaEntries(ctx, ref.Scheme, db)
	if err != nil {
		return Exit(dbcopy.RedactErrorWithSecrets(dbcopy.RedactErrorWithSecrets(err, ref.Path), dbArg).Error(), exitCodeDatabase)
	}
	listing := schemaListing{Provider: ref.Scheme, Target: schemaTarget(ref), Tables: entries}
	return writeSchemaListing(cmd.OutOrStdout(), format, listing)
}

func isSchemaProvider(scheme string) bool {
	for _, p := range schemaProviders {
		if p == scheme {
			return true
		}
	}
	return false
}

// schemaTarget is the resolved source URL without userinfo, query or fragment.
// An env:NAME source shows what the variable holds, stripped the same way.
func schemaTarget(ref dbcopy.BackendRef) string {
	if ref.Scheme == "postgres" {
		return dbcopy.SourceDisplay(ref.Path)
	}
	return dbcopy.SourceDisplay(ref.Scheme + "://" + ref.Path)
}

// listSchemaEntries reads the tables and views of db for provider.
func listSchemaEntries(ctx context.Context, provider string, db dal.DB) ([]schemaEntry, error) {
	var entries []schemaEntry
	var err error
	if provider == "postgres" {
		entries, err = postgresEntries(ctx, db)
	} else {
		entries, err = sqliteEntries(ctx, db)
	}
	if err != nil {
		return nil, err
	}
	sortSchemaEntries(entries)
	return entries, nil
}

func sqliteEntries(ctx context.Context, db dal.DB) ([]schemaEntry, error) {
	entries := []schemaEntry{}
	refs, err := dbschema.ListCollections(ctx, db, nil)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	for _, ref := range refs {
		entries = append(entries, schemaEntry{Schema: sqliteSchemaName, Name: ref.Name(), Kind: schemaKindTable})
	}
	if reader, ok := dal.As[dbschema.SourceViewReader](db); ok {
		views, viewErr := reader.ListSourceViews(ctx)
		if viewErr != nil {
			return nil, fmt.Errorf("list views: %w", viewErr)
		}
		for _, view := range views {
			entries = append(entries, schemaEntry{Schema: sqliteSchemaName, Name: view.Name, Kind: schemaKindView})
		}
	}
	return entries, nil
}

func postgresEntries(ctx context.Context, db dal.DB) ([]schemaEntry, error) {
	reader, ok := dal.As[postgresSchemaReader](db)
	if !ok {
		return nil, fmt.Errorf("the PostgreSQL source cannot list its schemas")
	}
	schemas, err := reader.ListSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}
	entries := []schemaEntry{}
	for _, schema := range schemas {
		views, viewErr := reader.ListSchemaViews(ctx, schema)
		if viewErr != nil {
			return nil, fmt.Errorf("list views of schema %q: %w", schema, viewErr)
		}
		isView := make(map[string]bool, len(views))
		for _, view := range views {
			isView[view.Name()] = true
		}
		refs, collErr := reader.ListSchemaCollections(ctx, schema)
		if collErr != nil {
			return nil, fmt.Errorf("list tables of schema %q: %w", schema, collErr)
		}
		for _, ref := range refs {
			kind := schemaKindTable
			if isView[ref.Name()] {
				kind = schemaKindView
			}
			entries = append(entries, schemaEntry{Schema: schema, Name: ref.Name(), Kind: kind})
		}
	}
	return entries, nil
}

// sortSchemaEntries orders by schema, kind (table before view), then name.
func sortSchemaEntries(entries []schemaEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Schema != b.Schema {
			return a.Schema < b.Schema
		}
		if a.Kind != b.Kind {
			return a.Kind == schemaKindTable
		}
		return a.Name < b.Name
	})
}

func writeSchemaListing(w io.Writer, format string, listing schemaListing) error {
	if format == "text" {
		_, _ = fmt.Fprintf(w, "provider: %s\ntarget: %s\n", listing.Provider, listing.Target)
		for _, e := range listing.Tables {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", e.Schema, e.Kind, e.Name)
		}
		return nil
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(listing)
}
