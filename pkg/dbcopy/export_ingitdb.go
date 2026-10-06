package dbcopy

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/ingitdb/dalgo2ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingitdb/ingitdb-go/ingitdb/datavalidator"
	"github.com/ingitdb/ingitdb-go/ingitdb/validator"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type exportTable struct {
	ref dal.CollectionRef
	def dbschema.CollectionDef
}

// ExportInGitDB streams a DALgo database into a native inGitDB project. The
// destination must not exist. All files are built in a sibling staging
// directory and become visible together only after the final successful row.
// The exporter writes one row at a time. Memory use also depends on the
// source provider: SourceRowsReader streams, while a generic query reader may
// materialize its collection before returning rows.
func ExportInGitDB(ctx context.Context, source dal.DB, destination string, options ...ExportOptions) (map[string]int64, error) {
	format := ingitdb.RecordFormatJSON
	if len(options) > 1 {
		return nil, fmt.Errorf("only one export options value is supported")
	}
	if len(options) == 1 {
		var err error
		format, err = normalizeExportFormat(options[0].RecordsFormat)
		if err != nil {
			return nil, err
		}
	}
	refs, err := dbschema.ListCollections(ctx, source, nil)
	if err != nil {
		return nil, fmt.Errorf("list source collections: %w", err)
	}
	if len(refs) == 0 {
		return nil, ErrSourceHasNoTables
	}
	defs := make([]exportTable, 0, len(refs))
	seenNames := map[string]bool{}
	for _, ref := range refs {
		if ref.Schema() != "" || ref.Database() != "" || ref.Parent() != nil {
			return nil, fmt.Errorf("source collection %q has a namespace, database, or parent that native root export cannot preserve", ref.Path())
		}
		if err := safeExportName(ref.Name()); err != nil {
			return nil, err
		}
		if format == ingitdb.RecordFormatINGR {
			if err := safeINGRHeaderName(ref.Name()); err != nil {
				return nil, fmt.Errorf("source collection %q: %w", ref.Name(), err)
			}
		}
		if seenNames[ref.Name()] {
			return nil, fmt.Errorf("duplicate source collection name %q", ref.Name())
		}
		seenNames[ref.Name()] = true
		def, err := dbschema.DescribeCollection(ctx, source, &ref)
		if err != nil {
			return nil, fmt.Errorf("describe source collection %q: %w", ref.Name(), err)
		}
		if def == nil || def.Name != ref.Name() {
			return nil, fmt.Errorf("source collection %q returned inconsistent schema", ref.Name())
		}
		for _, field := range def.Fields {
			if err := safeSourceFieldName(string(field.Name)); err != nil {
				return nil, fmt.Errorf("source collection %q: %w", ref.Name(), err)
			}
			if format == ingitdb.RecordFormatINGR {
				if err := safeINGRHeaderName(string(field.Name)); err != nil {
					return nil, fmt.Errorf("source collection %q: %w", ref.Name(), err)
				}
			}
		}
		if format == ingitdb.RecordFormatJSONL || format == ingitdb.RecordFormatINGR || format == ingitdb.RecordFormatCSV {
			for _, field := range def.Fields {
				if string(field.Name) == "$ID" {
					return nil, fmt.Errorf("source collection %q contains reserved transport field $ID", ref.Name())
				}
			}
		}
		if _, err := dalgo2ingitdb.ExportCollectionDefinition(*def); err != nil {
			return nil, fmt.Errorf("map source collection %q: %w", ref.Name(), err)
		}
		defs = append(defs, exportTable{ref: ref, def: *def})
	}
	if err := validateExportRelationships(defs); err != nil {
		return nil, err
	}
	constraintsChecked := false
	if checker, ok := dal.As[dbschema.SourceConstraintChecker](source); ok {
		if err := checker.CheckSourceConstraints(ctx); err != nil {
			return nil, fmt.Errorf("source constraint check: %w", err)
		}
		constraintsChecked = true
	}
	path, err := filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("destination already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); err != nil {
		return nil, fmt.Errorf("destination parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".datatug-export-*")
	if err != nil {
		return nil, fmt.Errorf("create export staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	// Reserve every collection name exclusively before writing any records.
	// The target filesystem decides which names alias (case, Unicode, and
	// platform rules), so an alias cannot overwrite another table's files.
	if err := reserveExportCollectionDirs(stage, defs, os.Mkdir); err != nil {
		return nil, err
	}
	registry := map[string]string{}
	counts := map[string]int64{}
	for _, table := range defs {
		def := table.def
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := exportCollection(ctx, source, stage, table.ref, def, constraintsChecked, format)
		if err != nil {
			return nil, fmt.Errorf("export %q: %w", def.Name, err)
		}
		registry[def.Name] = def.Name
		counts[def.Name] = count
	}
	if err := os.MkdirAll(filepath.Join(stage, ".ingitdb"), 0o755); err != nil {
		return nil, err
	}
	if viewReader, ok := dal.As[dbschema.SourceViewReader](source); ok {
		views, err := viewReader.ListSourceViews(ctx)
		if err != nil {
			return nil, fmt.Errorf("list source views: %w", err)
		}
		for _, view := range views {
			if err := safeExportName(view.Name); err != nil {
				return nil, err
			}
		}
		if len(views) > 0 {
			viewMetadata, err := yaml.Marshal(views)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(stage, ".ingitdb", "source-views.yaml"), viewMetadata, 0o644); err != nil {
				return nil, err
			}
		}
	}
	contents, err := yaml.Marshal(registry)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, ".ingitdb", "root-collections.yaml"), contents, 0o644); err != nil {
		return nil, err
	}
	if _, err := validator.ReadDefinition(stage, ingitdb.Validate()); err != nil {
		return nil, fmt.Errorf("staged native definition invalid: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := publishExportExclusive(stage, path); err != nil {
		return nil, fmt.Errorf("publish export: %w", err)
	}
	return counts, nil
}

func reserveExportCollectionDirs(stage string, tables []exportTable, mkdir func(string, os.FileMode) error) error {
	for _, table := range tables {
		if err := mkdir(filepath.Join(stage, table.def.Name), 0o755); err != nil {
			return fmt.Errorf("reserve source collection %q: %w", table.def.Name, err)
		}
	}
	return nil
}

func validateExportRelationships(tables []exportTable) error {
	byName := make(map[string]dbschema.CollectionDef, len(tables))
	for _, table := range tables {
		byName[table.def.Name] = table.def
	}
	for _, table := range tables {
		for _, fk := range table.def.ForeignKeys {
			if fk.ReferencedNamespace != "" {
				return fmt.Errorf("foreign key %q in %q targets unsupported namespace %q", fk.Name, table.def.Name, fk.ReferencedNamespace)
			}
			target, ok := byName[fk.ReferencedCollection]
			if !ok {
				return fmt.Errorf("foreign key %q in %q targets missing collection %q", fk.Name, table.def.Name, fk.ReferencedCollection)
			}
			targetFields := fk.ReferencedFields
			if len(targetFields) == 0 {
				targetFields = target.PrimaryKey
			}
			if len(targetFields) == 0 {
				return fmt.Errorf("foreign key %q in %q has no resolvable target key", fk.Name, table.def.Name)
			}
			if len(targetFields) != len(fk.Fields) {
				return fmt.Errorf("foreign key %q in %q has mismatched target field tuple", fk.Name, table.def.Name)
			}
			targetNames := map[dal.FieldName]bool{}
			for _, field := range target.Fields {
				targetNames[field.Name] = true
			}
			for _, field := range targetFields {
				if !targetNames[field] {
					return fmt.Errorf("foreign key %q in %q targets missing field %q", fk.Name, table.def.Name, field)
				}
			}
		}
	}
	return nil
}

func safeExportName(name string) error {
	if !utf8.ValidString(name) || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("unsafe collection name %q", name)
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return fmt.Errorf("unsafe collection name %q contains a control character", name)
		}
	}
	return nil
}

func safeSourceFieldName(name string) error {
	if name == "" || !utf8.ValidString(name) {
		return fmt.Errorf("unsafe source field name %q", name)
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return fmt.Errorf("unsafe source field name %q contains a control character", name)
		}
	}
	return nil
}

func safeINGRHeaderName(name string) error {
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("name %q has unsupported INGR header whitespace", name)
	}
	if strings.ContainsAny(name, ",:") {
		return fmt.Errorf("name %q contains an INGR header delimiter", name)
	}
	return nil
}

func exportCollection(ctx context.Context, source dal.DB, stage string, ref dal.CollectionRef, def dbschema.CollectionDef, constraintsChecked bool, format ingitdb.RecordFormat) (int64, error) {
	colDef, err := dalgo2ingitdb.ExportCollectionDefinition(def)
	if err != nil {
		return 0, err
	}
	colDef.RecordFile = exportRecordFile(format)
	if format == ingitdb.RecordFormatCSV {
		colDef.ColumnsOrder = append([]string{"$ID"}, colDef.ColumnsOrder...)
	}
	if constraintsChecked {
		colDef.SourceSchema.ConstraintValidation = "provider-preflight-passed"
		colDef.SourceSchema.RelationshipComparison = "provider-native"
	} else {
		colDef.SourceSchema.ConstraintValidation = "unverified"
		colDef.SourceSchema.RelationshipComparison = "unverified"
	}
	// Native record IDs are transport identities. For a keyless table they are
	// ordinals; they must never be mistaken for source primary keys.
	if len(def.PrimaryKey) == 0 {
		colDef.PrimaryKey = nil
	}
	if err := colDef.Validate(); err != nil {
		return 0, err
	}
	colDir := filepath.Join(stage, def.Name)
	if err := os.MkdirAll(filepath.Join(colDir, ingitdb.SchemaDir), 0o755); err != nil {
		return 0, err
	}
	y, err := yaml.Marshal(colDef)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(colDir, ingitdb.SchemaDir, ingitdb.CollectionDefFileName), y, 0o644); err != nil {
		return 0, err
	}
	file, err := os.Create(filepath.Join(colDir, colDef.RecordFile.Name))
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	writer, err := newExportRecordsWriter(file, format, def.Name, def.Fields)
	if err != nil {
		return 0, err
	}
	var builder dal.IQueryBuilder = dal.NewQueryBuilder(dal.From(ref))
	order := def.PrimaryKey
	if len(order) == 0 {
		for _, field := range def.Fields {
			order = append(order, field.Name)
		}
	}
	for _, field := range order {
		builder = builder.OrderBy(dal.AscendingField(string(field)))
	}
	var next func() (dbschema.SourceRow, error)
	if native, ok := dal.As[dbschema.SourceRowsReader](source); ok {
		cursor, err := native.OpenSourceRows(ctx, &ref)
		if err != nil {
			return 0, fmt.Errorf("open source rows: %w", err)
		}
		defer func() { _ = cursor.Close() }()
		next = func() (dbschema.SourceRow, error) {
			row, err := cursor.Next()
			return row, err
		}
	} else {
		reader, err := source.ExecuteQueryToRecordsReader(ctx, builder.SelectIntoRecordset())
		if err != nil {
			return 0, fmt.Errorf("read source rows: %w", err)
		}
		defer func() { _ = reader.Close() }()
		next = func() (dbschema.SourceRow, error) {
			rec, err := reader.Next()
			if err != nil {
				return dbschema.SourceRow{}, err
			}
			data, ok := rec.Data().(map[string]any)
			if !ok {
				return dbschema.SourceRow{}, fmt.Errorf("row data has unsupported shape %T", rec.Data())
			}
			return dbschema.SourceRow{Values: data}, nil
		}
	}
	storage := newStorageClassWriter(colDir)
	defer func() { _, _ = storage.Close() }()
	var count int64
	classAvailability := storageClassAvailability{}
	var idIndex *exportIDIndex
	if len(def.PrimaryKey) > 0 {
		idIndex, err = newExportIDIndex(stage)
		if err != nil {
			return 0, err
		}
		defer idIndex.Close()
	}
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		row, err := next()
		if errors.Is(err, io.EOF) || errors.Is(err, dal.ErrNoMoreRecords) {
			break
		}
		if err != nil {
			return count, fmt.Errorf("read row %d: %w", count+1, err)
		}
		if err := classAvailability.Check(def.Fields, row.StorageClasses); err != nil {
			return count, fmt.Errorf("row %d: %w", count+1, err)
		}
		if err := checkPhysicalClasses(def.Fields, row.Values, row.StorageClasses); err != nil {
			return count, fmt.Errorf("row %d: %w", count+1, err)
		}
		transport, id, err := encodeExportRow(def, row.Values, count+1)
		if err != nil {
			return count, fmt.Errorf("row %d: %w", count+1, err)
		}
		if findings := datavalidator.ValidateRecordData(colDef, id, transport); len(findings) > 0 {
			return count, fmt.Errorf("row %d is invalid in native schema: %v", count+1, findings[0])
		}
		if idIndex != nil {
			if err := idIndex.Add(id); err != nil {
				return count, fmt.Errorf("duplicate or invalid source primary key at row %d: %w", count+1, err)
			}
		}
		if err := storage.Append(id, def.Fields, row.StorageClasses); err != nil {
			return count, err
		}
		if err := writer.Write(id, transport); err != nil {
			return count, err
		}
		count++
	}
	if err := writer.Close(); err != nil {
		return count, err
	}
	if err := file.Sync(); err != nil {
		return count, err
	}
	files, err := storage.Close()
	if err != nil {
		return count, err
	}
	colDef.SourceSchema.StorageClassFiles = files
	y, err = yaml.Marshal(colDef)
	if err != nil {
		return count, err
	}
	if err := os.WriteFile(filepath.Join(colDir, ingitdb.SchemaDir, ingitdb.CollectionDefFileName), y, 0o644); err != nil {
		return count, err
	}
	return count, file.Close()
}

type exportIDIndex struct {
	path   string
	db     *sql.DB
	tx     *sql.Tx
	insert *sql.Stmt
}

func newExportIDIndex(stage string) (*exportIDIndex, error) {
	f, err := os.CreateTemp(stage, ".export-ids-*.sqlite")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; CREATE TABLE ids (id TEXT PRIMARY KEY) WITHOUT ROWID`); err != nil {
		_ = db.Close()
		_ = os.Remove(path)
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		_ = os.Remove(path)
		return nil, err
	}
	insert, err := tx.Prepare(`INSERT INTO ids (id) VALUES (?)`)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &exportIDIndex{path: path, db: db, tx: tx, insert: insert}, nil
}

func (idx *exportIDIndex) Add(id string) error {
	_, err := idx.insert.Exec(id)
	return err
}

func (idx *exportIDIndex) Close() {
	_ = idx.insert.Close()
	_ = idx.tx.Rollback()
	_ = idx.db.Close()
	_ = os.Remove(idx.path)
}

func encodeExportRow(def dbschema.CollectionDef, data map[string]any, ordinal int64) (map[string]any, string, error) {
	transport := make(map[string]any, len(def.Fields))
	for _, field := range def.Fields {
		value, ok := data[string(field.Name)]
		if !ok {
			return nil, "", fmt.Errorf("missing field %q", field.Name)
		}
		encoded, err := encodeExportValue(field, value)
		if err != nil {
			return nil, "", fmt.Errorf("field %q: %w", field.Name, err)
		}
		transport[string(field.Name)] = encoded
	}
	if len(data) != len(def.Fields) {
		return nil, "", fmt.Errorf("row field count %d differs from described schema %d", len(data), len(def.Fields))
	}
	if len(def.PrimaryKey) == 0 {
		return transport, fmt.Sprintf("row-%012d", ordinal), nil
	}
	components := make([][2]any, len(def.PrimaryKey))
	for i, field := range def.PrimaryKey {
		value := transport[string(field)]
		if value == nil {
			return nil, "", fmt.Errorf("primary key field %q is null", field)
		}
		components[i] = [2]any{fmt.Sprintf("%T", value), value}
	}
	b, err := json.Marshal(components)
	if err != nil {
		return nil, "", err
	}
	return transport, "pk-" + base64.RawURLEncoding.EncodeToString(b), nil
}

var decimalLiteral = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func encodeExportValue(field dbschema.FieldDef, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch field.Type {
	case dbschema.Decimal:
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case json.Number:
			text = v.String()
		default:
			return nil, fmt.Errorf("decimal arrived as %T; exact text required", value)
		}
		if !decimalLiteral.MatchString(text) {
			return nil, fmt.Errorf("decimal text %q is not numeric", text)
		}
		return text, nil
	case dbschema.Bytes:
		if v, ok := value.([]byte); ok {
			return base64.StdEncoding.EncodeToString(v), nil
		}
		return nil, fmt.Errorf("bytes arrived as %T", value)
	case dbschema.String:
		if v, ok := value.([]byte); ok {
			if !utf8.Valid(v) {
				return nil, fmt.Errorf("invalid UTF-8 string")
			}
			return string(v), nil
		}
		if v, ok := value.(string); ok {
			if !utf8.ValidString(v) {
				return nil, fmt.Errorf("invalid UTF-8 string")
			}
			return v, nil
		}
		return nil, fmt.Errorf("string arrived as %T", value)
	case dbschema.Time:
		if v, ok := value.(time.Time); ok {
			return v.Format(time.RFC3339Nano), nil
		}
		if v, ok := value.(string); ok {
			if !utf8.ValidString(v) {
				return nil, fmt.Errorf("invalid UTF-8 time text")
			}
			return v, nil
		}
		return nil, fmt.Errorf("time arrived as %T", value)
	case dbschema.Int:
		switch v := value.(type) {
		case int:
			return int64(v), nil
		case int8:
			return int64(v), nil
		case int16:
			return int64(v), nil
		case int32:
			return int64(v), nil
		case int64:
			return v, nil
		case uint, uint8, uint16, uint32, uint64:
			text := fmt.Sprint(v)
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("integer %s exceeds int64 range", text)
			}
			return n, nil
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				return nil, fmt.Errorf("integer %q is invalid or out of range", v)
			}
			return n, nil
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || math.Abs(v) > 9007199254740991 {
				return nil, fmt.Errorf("float cannot be represented as exact integer")
			}
			return int64(v), nil
		default:
			return nil, fmt.Errorf("integer arrived as %T", value)
		}
	case dbschema.Bool:
		if v, ok := value.(bool); ok {
			return v, nil
		}
		switch v := value.(type) {
		case int:
			if v == 0 || v == 1 {
				return v == 1, nil
			}
		case int64:
			if v == 0 || v == 1 {
				return v == 1, nil
			}
		}
		return nil, fmt.Errorf("boolean arrived as %T with value %v; expected bool or 0/1", value, value)
	case dbschema.Float:
		switch v := value.(type) {
		case float32:
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, fmt.Errorf("non-finite float")
			}
			return float64(v), nil
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("non-finite float")
			}
			return v, nil
		default:
			return nil, fmt.Errorf("float arrived as %T", value)
		}
	default:
		return nil, fmt.Errorf("unsupported source type %s", field.Type)
	}
}

type storageClassAvailability struct {
	seen      bool
	available bool
}

func (a *storageClassAvailability) Check(fields []dbschema.FieldDef, classes map[string]string) error {
	hasDecimal := false
	for _, field := range fields {
		if field.Type == dbschema.Decimal {
			hasDecimal = true
			break
		}
	}
	if !hasDecimal {
		return nil
	}
	available := classes != nil
	if !a.seen {
		a.seen = true
		a.available = available
		return nil
	}
	if a.available != available {
		return fmt.Errorf("physical storage-class metadata availability changed within collection")
	}
	return nil
}

func checkPhysicalClasses(fields []dbschema.FieldDef, values map[string]any, classes map[string]string) error {
	if classes == nil {
		return nil
	} // provider has no physical-storage metadata
	for _, field := range fields {
		name := string(field.Name)
		class := classes[name]
		switch class {
		case "", "null", "integer", "real", "text", "blob":
		default:
			return fmt.Errorf("field %q has invalid physical storage class %q", name, class)
		}
		if field.Type == dbschema.Decimal {
			if values[name] != nil && (class == "" || class == "null") {
				return fmt.Errorf("field %q is missing a physical storage class for a non-null decimal", name)
			}
			if values[name] == nil && class != "" && class != "null" {
				return fmt.Errorf("field %q has physical storage class %q for null decimal", name, class)
			}
			if class == "blob" {
				return fmt.Errorf("field %q has blob decimal value that exact string transport cannot preserve", name)
			}
			continue
		}
		if class == "" || class == "null" {
			continue
		}
		var expected string
		switch field.Type {
		case dbschema.Int, dbschema.Bool:
			expected = "integer"
		case dbschema.Float:
			expected = "real"
		case dbschema.String, dbschema.Time:
			expected = "text"
		case dbschema.Bytes:
			expected = "blob"
		default:
			return fmt.Errorf("field %q has unsupported logical type %s", field.Name, field.Type)
		}
		if class != expected {
			return fmt.Errorf("field %q has physical storage class %q, incompatible with logical %s transport; export cannot preserve this mixed-affinity value", field.Name, class, field.Type)
		}
	}
	return nil
}
