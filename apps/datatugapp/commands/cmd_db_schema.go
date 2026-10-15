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
	"github.com/datatug/datatug-cli/pkg/schemers/dalgoschema"
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
	// Columns is set by --columns; a pointer to a nil slice is a table whose
	// columns could not be read and prints as null.
	Columns *[]schemaColumn `json:"columns,omitempty"`
	// ForeignKeys is set by --columns, for tables whose definition could be read
	// (never for a view): the outgoing keys, an empty list for a table with none.
	ForeignKeys *[]schemaForeignKey `json:"foreignKeys,omitempty"`
	// RowCount is set by --counts, for tables only; a table that cannot be
	// counted prints null.
	RowCount *rowCount `json:"rowCount,omitempty"`

	ref         dal.CollectionRef
	viewColumns []string
	definition  string
}

// schemaForeignKey is an outgoing foreign key, named as `datatug show` names it.
type schemaForeignKey struct {
	Name       string         `json:"name"`
	Columns    []string       `json:"columns"`
	RefTable   schemaTableRef `json:"refTable"`
	RefColumns []string       `json:"refColumns"`
}

// schemaTableRef names the referenced table.
type schemaTableRef struct {
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name"`
}

// schemaColumn is one column of a table or view.
type schemaColumn struct {
	Name               string  `json:"name"`
	Type               string  `json:"type"`
	Nullable           bool    `json:"nullable"`
	PrimaryKeyPosition int     `json:"primaryKeyPosition,omitempty"`
	Default            *string `json:"default,omitempty"`
}

// rowCount is a count that prints as null when the count is unknown.
type rowCount struct{ n *int }

func (c rowCount) MarshalJSON() ([]byte, error) {
	if c.n == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*c.n)
}

// schemaListing is the JSON document `db schema` prints.
type schemaListing struct {
	Provider     string        `json:"provider"`
	Target       string        `json:"target"`
	DetectedFrom string        `json:"detectedFrom"`
	Tables       []schemaEntry `json:"tables"`
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
	cmd.Flags().Bool("columns", false, "Also list each table's and view's columns (name, declared type, nullability, primary key position, default)")
	cmd.Flags().Bool("counts", false, "Also count the rows of each table (can be slow on large tables); a table that cannot be counted reports null")
	return cmd
}

// schemaSession is an opened source of one `db` metadata command.
type schemaSession struct {
	ctx          context.Context
	ref          dbcopy.BackendRef
	db           dal.DB
	dbArg        string
	format       string
	detectedFrom string
}

// openSchemaSession reads --db and --format of cmd and opens the source
// read-only: it is the one place a metadata command resolves, checks and opens
// its source.
func openSchemaSession(cmd *cobra.Command, command string) (*schemaSession, error) {
	dbArg, _ := cmd.Flags().GetString("db")
	format, _ := cmd.Flags().GetString("format")
	if format != "json" && format != "text" {
		return nil, Exit(fmt.Sprintf("unsupported --format %q: want one of json, text", format), exitCodeUsage)
	}
	source, detectedFrom, err := resolveSchemaSource(dbArg)
	if err != nil {
		return nil, Exit(err.Error(), exitCodeUsage)
	}
	ref, err := dbcopy.Parse(source)
	if err != nil {
		return nil, Exit(err.Error(), exitCodeUsage)
	}
	ref = ref.WithFlag("--db")
	if !isSchemaProvider(ref.Scheme) {
		return nil, Exit(fmt.Sprintf("unsupported provider %q for %s: supported providers are %s", ref.Scheme, command, strings.Join(schemaProviders, ", ")), exitCodeUsage)
	}
	if err = dbcopy.CheckPostgresRead(ref, 0); err != nil {
		return nil, Exit(err.Error(), exitCodeDatabase)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := openSchemaSource(ctx, ref)
	if err != nil {
		return nil, Exit(ref.OpenFailure(err).Error(), exitCodeDatabase)
	}
	return &schemaSession{ctx: ctx, ref: ref, db: db, dbArg: dbArg, format: format, detectedFrom: detectedFrom}, nil
}

// failure is the exit error for a read of the opened source that failed; the
// text never holds the connection string or what a driver echoed of it.
func (s *schemaSession) failure(err error) error {
	return Exit(dbcopy.RedactErrorWithSecrets(dbcopy.RedactErrorWithSecrets(err, s.ref.Path), s.dbArg).Error(), exitCodeDatabase)
}

func dbSchemaAction(cmd *cobra.Command, _ []string) error {
	session, err := openSchemaSession(cmd, "db schema")
	if err != nil {
		return err
	}
	withColumns, _ := cmd.Flags().GetBool("columns")
	withCounts, _ := cmd.Flags().GetBool("counts")
	entries, err := listSchemaEntries(session.ctx, session.ref.Scheme, session.db)
	if err != nil {
		return session.failure(err)
	}
	if withColumns {
		addSchemaColumns(session.ctx, session.db, session.ref.Scheme, entries)
	}
	if withCounts {
		addSchemaCounts(session.ctx, session.db, session.ref.Scheme, entries)
	}
	listing := schemaListing{Provider: session.ref.Scheme, Target: schemaTarget(session.ref), DetectedFrom: session.detectedFrom, Tables: entries}
	return writeSchemaListing(cmd.OutOrStdout(), session.format, listing)
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
		entries = append(entries, schemaEntry{Schema: sqliteSchemaName, Name: ref.Name(), Kind: schemaKindTable, ref: ref})
	}
	if reader, ok := dal.As[dbschema.SourceViewReader](db); ok {
		views, viewErr := reader.ListSourceViews(ctx)
		if viewErr != nil {
			return nil, fmt.Errorf("list views: %w", viewErr)
		}
		for _, view := range views {
			entries = append(entries, schemaEntry{Schema: sqliteSchemaName, Name: view.Name, Kind: schemaKindView, viewColumns: view.Columns, definition: view.CreateSQL})
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
			entries = append(entries, schemaEntry{Schema: schema, Name: ref.Name(), Kind: kind, ref: ref})
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

// addSchemaColumns reads the columns of every entry. A table whose columns
// cannot be read gets null; a view of a source that describes only tables
// (SQLite) gets the column names its catalog lists, with no type.
func addSchemaColumns(ctx context.Context, db dal.DB, provider string, entries []schemaEntry) {
	for i := range entries {
		entry := &entries[i]
		var columns []schemaColumn
		if entry.Kind == schemaKindView && entry.viewColumns != nil {
			columns = viewColumns(entry.viewColumns)
		} else if def, err := dbschema.DescribeCollection(ctx, db, &entry.ref); err == nil {
			columns = columnsOf(def)
			if entry.Kind == schemaKindTable {
				entry.ForeignKeys = schemaForeignKeys(provider, entry.Schema, def)
			}
		}
		entry.Columns = &columns
	}
}

func viewColumns(names []string) []schemaColumn {
	columns := make([]schemaColumn, 0, len(names))
	for _, name := range names {
		columns = append(columns, schemaColumn{Name: name, Nullable: true})
	}
	return columns
}

// columnsOf builds the columns of def. The declared type, the default and the
// primary key position come from the provider's own catalog text when it
// keeps one (SourceDefinition), and from the portable definition otherwise.
func columnsOf(def *dbschema.CollectionDef) []schemaColumn {
	source := map[string]dbschema.SourceColumnDef{}
	if def.SourceDefinition != nil {
		for _, column := range def.SourceDefinition.Columns {
			source[column.Name] = column
		}
	}
	columns := make([]schemaColumn, 0, len(def.Fields))
	for _, field := range def.Fields {
		column := schemaColumn{Name: string(field.Name), Type: field.Type.String(), Nullable: field.Nullable}
		for position, name := range def.PrimaryKey {
			if name == field.Name {
				column.PrimaryKeyPosition = position + 1
			}
		}
		if declared, ok := source[column.Name]; ok {
			column.Type = declared.DeclaredType
			column.Default = declared.DefaultSQL
			if declared.PrimaryKeyPosition > 0 {
				column.PrimaryKeyPosition = declared.PrimaryKeyPosition
			}
		}
		columns = append(columns, column)
	}
	return columns
}

// addSchemaCounts counts the rows of every table with the native COUNT(*) of
// the source; a source that cannot run it natively, or a table whose count
// fails, reports null rather than reading the table here.
func addSchemaCounts(ctx context.Context, db dal.DB, provider string, entries []schemaEntry) {
	counter := dalgoschema.NewNativeCounter(db)
	if provider == "sqlite" {
		// SQLite runs COUNT(*) itself, but its adapter does not declare it.
		counter = dalgoschema.NewNativeCounterWithCapabilities(db, dal.QueryCapabilities{Aggregate: dal.AggregateCapabilities{Count: true}})
	}
	for i := range entries {
		entry := &entries[i]
		if entry.Kind != schemaKindTable {
			continue
		}
		schema := entry.Schema
		if provider == "sqlite" {
			schema = ""
		}
		n, err := counter.CountRecords(ctx, schema, entry.Name)
		if err != nil {
			n = nil
		}
		entry.RowCount = &rowCount{n: n}
	}
}

// schemaForeignKeys lists the outgoing foreign keys of def.
func schemaForeignKeys(provider, schema string, def *dbschema.CollectionDef) *[]schemaForeignKey {
	keys := []schemaForeignKey{}
	for _, key := range def.ForeignKeys {
		keys = append(keys, schemaForeignKey{
			Name: key.Name, Columns: namesOf(key.Fields), RefColumns: namesOf(key.ReferencedFields),
			RefTable: schemaTableRef{Schema: refSchemaOf(provider, schema, key.ReferencedNamespace), Name: key.ReferencedCollection},
		})
	}
	return &keys
}

// refSchemaOf is the schema a foreign key points into: the one it names, else
// the schema of the table that holds it (main in SQLite).
func refSchemaOf(provider, schema, named string) string {
	if named != "" {
		return named
	}
	return defaultSchema(provider, schema)
}
