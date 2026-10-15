package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/spf13/cobra"
)

// Column families, so that consumers can group types of every provider.
const (
	familyNumeric = "numeric"
	familyText    = "text"
	familyDate    = "date"
	familyBoolean = "boolean"
	familyBinary  = "binary"
	familyJSON    = "json"
	familyOther   = "other"
)

// schemaEnvelope starts the output of every `db` metadata command.
type schemaEnvelope struct {
	Provider     string `json:"provider"`
	Target       string `json:"target"`
	DetectedFrom string `json:"detectedFrom"`
}

// keyRef is a foreign key of the object it is printed on (outgoing).
type keyRef struct {
	Name              string   `json:"name,omitempty"`
	Columns           []string `json:"columns"`
	ReferencedSchema  string   `json:"referencedSchema"`
	ReferencedTable   string   `json:"referencedTable"`
	ReferencedColumns []string `json:"referencedColumns"`
	OnUpdate          string   `json:"onUpdate,omitempty"`
	OnDelete          string   `json:"onDelete,omitempty"`
}

// incomingKey is a foreign key of another table that references the object.
type incomingKey struct {
	Name              string   `json:"name,omitempty"`
	Schema            string   `json:"schema"`
	Table             string   `json:"table"`
	Columns           []string `json:"columns"`
	ReferencedColumns []string `json:"referencedColumns"`
}

// indexInfo is one index of a table.
type indexInfo struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	Unique     bool     `json:"unique"`
	Partial    bool     `json:"partial,omitempty"`
	Expression bool     `json:"expression,omitempty"`
	Origin     string   `json:"origin,omitempty"`
	Definition string   `json:"definition,omitempty"`
}

// constraintInfo is a constraint as the provider lists it.
type constraintInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// columnDetail is one column in full.
type columnDetail struct {
	Name               string        `json:"name"`
	Ordinal            int           `json:"ordinal"`
	Type               string        `json:"type"`
	Length             *int          `json:"length,omitempty"`
	Precision          *int          `json:"precision,omitempty"`
	Scale              *int          `json:"scale,omitempty"`
	Family             string        `json:"family"`
	Nullable           bool          `json:"nullable"`
	Default            *string       `json:"default,omitempty"`
	PrimaryKeyPosition int           `json:"primaryKeyPosition,omitempty"`
	AutoIncrement      bool          `json:"autoIncrement"`
	ForeignKeys        []keyRef      `json:"foreignKeys"`
	ReferencedBy       []incomingKey `json:"referencedBy"`
}

// tableDetail is the output of `db table`.
type tableDetail struct {
	schemaEnvelope
	Schema       string           `json:"schema"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	Definition   string           `json:"definition,omitempty"`
	RowCount     *rowCount        `json:"rowCount,omitempty"`
	Columns      []columnDetail   `json:"columns"`
	PrimaryKey   []string         `json:"primaryKey"`
	ForeignKeys  []keyRef         `json:"foreignKeys"`
	ReferencedBy []incomingKey    `json:"referencedBy"`
	Indexes      []indexInfo      `json:"indexes"`
	Constraints  []constraintInfo `json:"constraints"`
	Unsupported  []string         `json:"unsupported"`
}

// columnOutput is the output of `db column`.
type columnOutput struct {
	schemaEnvelope
	Schema      string       `json:"schema"`
	Table       string       `json:"table"`
	Column      columnDetail `json:"column"`
	Unsupported []string     `json:"unsupported"`
}

// columnUnsupported are the column facts no provider reports yet.
var columnUnsupported = []string{"columns.collation", "columns.generated", "columns.comment"}

// tableUnsupported are the table facts no provider reports yet.
var tableUnsupported = []string{"comment", "size", "triggers", "uniqueConstraints", "checkConstraints"}

func newDBMetadataCommand(use, short string, args cobra.PositionalArgs, run func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: run}
	cmd.Flags().String("db", "", "Database URL, env:NAME or SQLite file path: "+dbSchemesHelp())
	cmd.Flags().String("format", "json", "Output format: json or text")
	cmd.Flags().String("schema", "", "Schema of the object (PostgreSQL); required when the name exists in several")
	return cmd
}

// dbTableCommand wires `datatug db table <name> --db <url>`.
func dbTableCommand() *cobra.Command {
	cmd := newDBMetadataCommand("table <name>", "Show everything known about one table or view (read-only)", cobra.ExactArgs(1), dbTableAction)
	cmd.Flags().Bool("counts", false, "Also count the rows of a table (can be slow on large tables); null when it cannot be counted")
	return cmd
}

// dbColumnCommand wires `datatug db column <table> <column> --db <url>`.
func dbColumnCommand() *cobra.Command {
	return newDBMetadataCommand("column <table> <column>", "Show everything known about one column (read-only)", cobra.ExactArgs(2), dbColumnAction)
}

func dbTableAction(cmd *cobra.Command, args []string) error {
	session, entry, err := openSchemaObject(cmd, "db table", args[0])
	if err != nil {
		return err
	}
	detail, err := describeTable(session, entry)
	if err != nil {
		return session.failure(err)
	}
	if counts, _ := cmd.Flags().GetBool("counts"); counts && entry.Kind == schemaKindTable {
		entries := []schemaEntry{entry}
		addSchemaCounts(session.ctx, session.db, session.ref.Scheme, entries)
		detail.RowCount = entries[0].RowCount
	}
	return writeJSONOrText(cmd.OutOrStdout(), session.format, detail, func(w io.Writer) { writeTableText(w, detail) })
}

func dbColumnAction(cmd *cobra.Command, args []string) error {
	session, entry, err := openSchemaObject(cmd, "db column", args[0])
	if err != nil {
		return err
	}
	detail, err := describeTable(session, entry)
	if err != nil {
		return session.failure(err)
	}
	for _, column := range detail.Columns {
		if column.Name == args[1] {
			out := columnOutput{schemaEnvelope: detail.schemaEnvelope, Schema: detail.Schema, Table: detail.Name, Column: column, Unsupported: columnUnsupported}
			return writeJSONOrText(cmd.OutOrStdout(), session.format, out, func(w io.Writer) { writeColumnText(w, out) })
		}
	}
	return Exit(fmt.Sprintf("column %q not found in %s.%s", args[1], detail.Schema, detail.Name), exitCodeUsage)
}

// openSchemaObject opens the source and finds the table or view the command
// names, taking --schema into account.
func openSchemaObject(cmd *cobra.Command, command, name string) (*schemaSession, schemaEntry, error) {
	session, err := openSchemaSession(cmd, command)
	if err != nil {
		return nil, schemaEntry{}, err
	}
	entries, err := listSchemaEntries(session.ctx, session.ref.Scheme, session.db)
	if err != nil {
		return nil, schemaEntry{}, session.failure(err)
	}
	schema, _ := cmd.Flags().GetString("schema")
	entry, err := findSchemaEntry(entries, schema, name)
	if err != nil {
		return nil, schemaEntry{}, err
	}
	return session, entry, nil
}

// findSchemaEntry returns the table or view called name, in schema when given.
func findSchemaEntry(entries []schemaEntry, schema, name string) (schemaEntry, error) {
	var found []schemaEntry
	for _, entry := range entries {
		if entry.Name == name && (schema == "" || entry.Schema == schema) {
			found = append(found, entry)
		}
	}
	switch len(found) {
	case 0:
		return schemaEntry{}, Exit(fmt.Sprintf("table or view %q not found", name), exitCodeUsage)
	case 1:
		return found[0], nil
	}
	schemas := make([]string, len(found))
	for i, entry := range found {
		schemas[i] = entry.Schema
	}
	return schemaEntry{}, Exit(fmt.Sprintf("%q exists in several schemas (%s): pass --schema", name, strings.Join(schemas, ", ")), exitCodeUsage)
}

// describeTable reads one table or view in full.
func describeTable(session *schemaSession, entry schemaEntry) (tableDetail, error) {
	detail := tableDetail{
		schemaEnvelope: schemaEnvelope{Provider: session.ref.Scheme, Target: schemaTarget(session.ref), DetectedFrom: session.detectedFrom},
		Schema:         entry.Schema, Name: entry.Name, Kind: entry.Kind, Definition: entry.definition,
		PrimaryKey: []string{}, ForeignKeys: []keyRef{}, ReferencedBy: []incomingKey{}, Indexes: []indexInfo{}, Constraints: []constraintInfo{},
		Unsupported: append(append([]string{}, tableUnsupported...), columnUnsupported...),
	}
	if entry.Kind == schemaKindView && entry.viewColumns != nil {
		for i, name := range entry.viewColumns {
			detail.Columns = append(detail.Columns, columnDetail{Name: name, Ordinal: i + 1, Family: familyOther, Nullable: true, ForeignKeys: []keyRef{}, ReferencedBy: []incomingKey{}})
		}
		detail.Unsupported = append(detail.Unsupported, "primaryKey", "foreignKeys", "referencedBy", "indexes", "constraints", "columns.type", "columns.default")
		return detail, nil
	}
	def, err := dbschema.DescribeCollection(session.ctx, session.db, &entry.ref)
	if err != nil {
		return detail, err
	}
	provider := session.ref.Scheme
	for _, name := range def.PrimaryKey {
		detail.PrimaryKey = append(detail.PrimaryKey, string(name))
	}
	for _, key := range def.ForeignKeys {
		detail.ForeignKeys = append(detail.ForeignKeys, outgoingKey(provider, entry.Schema, key))
	}
	detail.ReferencedBy = incomingKeys(session, entry)
	detail.Indexes = indexesOf(def)
	if constraints, constraintErr := dbschema.ListConstraints(session.ctx, session.db, &entry.ref); constraintErr == nil {
		for _, constraint := range constraints {
			detail.Constraints = append(detail.Constraints, constraintInfo{Name: constraint.Name, Type: constraint.Type})
		}
	} else {
		detail.Unsupported = append(detail.Unsupported, "constraints")
	}
	detail.Columns = columnDetails(def, detail.ForeignKeys, detail.ReferencedBy)
	return detail, nil
}

func namesOf(fields []dal.FieldName) []string {
	names := make([]string, len(fields))
	for i, field := range fields {
		names[i] = string(field)
	}
	return names
}

// defaultSchema is the schema a provider that does not name one is in.
func defaultSchema(provider, fallback string) string {
	if provider == "sqlite" {
		return sqliteSchemaName
	}
	return fallback
}

func outgoingKey(provider, schema string, key dbschema.ForeignKeyDef) keyRef {
	referencedSchema := key.ReferencedNamespace
	if referencedSchema == "" {
		referencedSchema = defaultSchema(provider, schema)
	}
	return keyRef{
		Name: key.Name, Columns: namesOf(key.Fields), ReferencedSchema: referencedSchema,
		ReferencedTable: key.ReferencedCollection, ReferencedColumns: namesOf(key.ReferencedFields),
		OnUpdate: key.OnUpdate, OnDelete: key.OnDelete,
	}
}

// incomingKeys lists the foreign keys of other tables that reference entry.
// A source that cannot list its referrers reports none.
func incomingKeys(session *schemaSession, entry schemaEntry) []incomingKey {
	keys := []incomingKey{}
	referrers, err := dbschema.ListReferrers(session.ctx, session.db, &entry.ref)
	if err != nil {
		return keys
	}
	provider := session.ref.Scheme
	for _, referrer := range referrers {
		schema := referrer.Collection.Schema()
		if schema == "" {
			schema = defaultSchema(provider, entry.Schema)
		}
		table := referrer.Collection.Name()
		collection := referrer.Collection
		def, describeErr := dbschema.DescribeCollection(session.ctx, session.db, &collection)
		matched := false
		if describeErr == nil {
			for _, key := range def.ForeignKeys {
				if key.ReferencedCollection == entry.Name {
					matched = true
					keys = append(keys, incomingKey{Name: key.Name, Schema: schema, Table: table, Columns: namesOf(key.Fields), ReferencedColumns: namesOf(key.ReferencedFields)})
				}
			}
		}
		if !matched {
			keys = append(keys, incomingKey{Schema: schema, Table: table, Columns: namesOf(referrer.Fields), ReferencedColumns: []string{}})
		}
	}
	return keys
}

// indexesOf lists the indexes of def, from the provider's own catalog when it
// keeps one (partial and expression indexes, the CREATE text), else from the
// portable definition.
func indexesOf(def *dbschema.CollectionDef) []indexInfo {
	indexes := []indexInfo{}
	if def.SourceDefinition != nil && len(def.SourceDefinition.Indexes) > 0 {
		for _, index := range def.SourceDefinition.Indexes {
			info := indexInfo{Name: index.Name, Columns: []string{}, Unique: index.Unique, Partial: index.Partial, Origin: index.Origin, Definition: index.CreateSQL}
			for _, term := range index.Columns {
				if !term.Key {
					continue
				}
				if term.Name == nil {
					info.Expression = true
					continue
				}
				info.Columns = append(info.Columns, *term.Name)
			}
			indexes = append(indexes, info)
		}
		return indexes
	}
	for _, index := range def.Indexes {
		indexes = append(indexes, indexInfo{Name: index.Name, Columns: namesOf(index.Fields), Unique: index.Unique})
	}
	return indexes
}

var typeArguments = regexp.MustCompile(`\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\)`)

// columnDetails builds the columns of def in definition order.
func columnDetails(def *dbschema.CollectionDef, outgoing []keyRef, incoming []incomingKey) []columnDetail {
	source := map[string]dbschema.SourceColumnDef{}
	if def.SourceDefinition != nil {
		for _, column := range def.SourceDefinition.Columns {
			source[column.Name] = column
		}
	}
	columns := make([]columnDetail, 0, len(def.Fields))
	for i, field := range def.Fields {
		column := columnDetail{
			Name: string(field.Name), Ordinal: i + 1, Type: field.Type.String(), Nullable: field.Nullable, AutoIncrement: field.AutoIncrement,
			ForeignKeys: []keyRef{}, ReferencedBy: []incomingKey{},
		}
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
		column.Family = familyOfType(column.Type, field.Type)
		column.Length, column.Precision, column.Scale = typeSizes(column.Type, column.Family)
		for _, key := range outgoing {
			if containsString(key.Columns, column.Name) {
				column.ForeignKeys = append(column.ForeignKeys, key)
			}
		}
		for _, key := range incoming {
			if containsString(key.ReferencedColumns, column.Name) {
				column.ReferencedBy = append(column.ReferencedBy, key)
			}
		}
		columns = append(columns, column)
	}
	return columns
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// typeSizes reads the length, precision and scale out of a declared type:
// NVARCHAR(160) has a length, NUMERIC(5,2) a precision and a scale, and a
// single number on a numeric type is its precision.
func typeSizes(declared, family string) (length, precision, scale *int) {
	match := typeArguments.FindStringSubmatch(declared)
	if match == nil {
		return nil, nil, nil
	}
	first, _ := strconv.Atoi(match[1])
	if match[2] != "" {
		second, _ := strconv.Atoi(match[2])
		return nil, &first, &second
	}
	if family == familyNumeric {
		return nil, &first, nil
	}
	return &first, nil, nil
}

// familyOfType groups a declared type of any provider; the portable type is
// the fallback for text that names none of the known words.
func familyOfType(declared string, portable dbschema.Type) string {
	t := strings.ToLower(declared)
	switch {
	case strings.Contains(t, "json"):
		return familyJSON
	case strings.Contains(t, "bool"):
		return familyBoolean
	case strings.Contains(t, "date"), strings.Contains(t, "time"), strings.Contains(t, "interval"):
		return familyDate
	case strings.Contains(t, "blob"), strings.Contains(t, "binary"), strings.Contains(t, "bytea"):
		return familyBinary
	case strings.Contains(t, "char"), strings.Contains(t, "text"), strings.Contains(t, "clob"), strings.Contains(t, "uuid"):
		return familyText
	case strings.Contains(t, "point"):
		return familyOther
	case strings.Contains(t, "int"), strings.Contains(t, "serial"), strings.Contains(t, "float"), strings.Contains(t, "double"),
		strings.Contains(t, "real"), strings.Contains(t, "numeric"), strings.Contains(t, "decimal"), strings.Contains(t, "money"):
		return familyNumeric
	}
	switch portable {
	case dbschema.Int, dbschema.Float, dbschema.Decimal:
		return familyNumeric
	case dbschema.String:
		return familyText
	case dbschema.Time:
		return familyDate
	case dbschema.Bool:
		return familyBoolean
	case dbschema.Bytes:
		return familyBinary
	}
	return familyOther
}

// writeJSONOrText prints value as JSON, or lets text print its summary.
func writeJSONOrText(w io.Writer, format string, value any, text func(io.Writer)) error {
	if format == "text" {
		text(w)
		return nil
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func columnLine(column columnDetail) string {
	null := "NOT NULL"
	if column.Nullable {
		null = "NULL"
	}
	line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s", column.Ordinal, column.Name, column.Type, column.Family, null)
	if column.PrimaryKeyPosition > 0 {
		line += fmt.Sprintf("\tPK%d", column.PrimaryKeyPosition)
	}
	return line
}

func writeTableText(w io.Writer, detail tableDetail) {
	_, _ = fmt.Fprintf(w, "provider: %s\ntarget: %s\n%s %s.%s\n", detail.Provider, detail.Target, detail.Kind, detail.Schema, detail.Name)
	for _, column := range detail.Columns {
		_, _ = fmt.Fprintln(w, columnLine(column))
	}
	_, _ = fmt.Fprintf(w, "primary key: %s\n", strings.Join(detail.PrimaryKey, ", "))
	for _, key := range detail.ForeignKeys {
		_, _ = fmt.Fprintf(w, "foreign key: (%s) -> %s.%s (%s)\n", strings.Join(key.Columns, ", "), key.ReferencedSchema, key.ReferencedTable, strings.Join(key.ReferencedColumns, ", "))
	}
	for _, key := range detail.ReferencedBy {
		_, _ = fmt.Fprintf(w, "referenced by: %s.%s (%s)\n", key.Schema, key.Table, strings.Join(key.Columns, ", "))
	}
	for _, index := range detail.Indexes {
		_, _ = fmt.Fprintf(w, "index: %s (%s) unique=%t\n", index.Name, strings.Join(index.Columns, ", "), index.Unique)
	}
	if detail.RowCount != nil {
		_, _ = fmt.Fprintf(w, "row count: %s\n", rowCountText(*detail.RowCount))
	}
}

func rowCountText(count rowCount) string {
	if count.n == nil {
		return "unknown"
	}
	return strconv.Itoa(*count.n)
}

func writeColumnText(w io.Writer, out columnOutput) {
	_, _ = fmt.Fprintf(w, "provider: %s\ntarget: %s\n%s.%s\n%s\n", out.Provider, out.Target, out.Schema, out.Table, columnLine(out.Column))
}
