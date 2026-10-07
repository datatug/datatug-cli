package dbcopy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dal-go/dalgo/dbschema"
	bqwriter "github.com/dal-go/dalgo2bigquery"
	bq "google.golang.org/api/bigquery/v2"
)

type BigQueryCopySink struct{ Writer *bqwriter.LoadWriter }

type bigQueryMappedField struct {
	sourceName string
	target     bqwriter.Field
	typeCode   dbschema.Type
}

type bigQueryMappedTable struct {
	table       CopyTable
	targetName  string
	fields      []bigQueryMappedField
	schema      []bqwriter.Field
	primaryKey  *bq.TableConstraintsPrimaryKey
	foreignKeys []*bq.TableConstraintsForeignKeys
}

type bigQueryCopyPlan struct {
	writer *bqwriter.LoadWriter
	tables map[string]*bigQueryMappedTable
	order  []string
	warns  []string
}

// Preflight maps every collection and relationship without making a BigQuery
// request. Source rows are validated and spooled by CopyToSink before Prepare
// creates the destination dataset or tables.
func (s BigQueryCopySink) Preflight(_ context.Context, tables []CopyTable) (CopySinkPlan, error) {
	if s.Writer == nil {
		return nil, fmt.Errorf("BigQuery writer is not configured")
	}
	plan := &bigQueryCopyPlan{writer: s.Writer, tables: map[string]*bigQueryMappedTable{}}
	targetNames := map[string]string{}
	for _, table := range tables {
		if table.Ref.Database() != "" || table.Ref.Parent() != nil {
			return nil, fmt.Errorf("source collection %q has a database route or parent that BigQuery copy cannot represent", table.Ref.Path())
		}
		nameInput := table.Ref.Name()
		if table.Ref.Schema() != "" {
			nameInput = table.Ref.Schema() + "__" + nameInput
		}
		targetName, err := bigQueryIdentifier(nameInput, 1024)
		if err != nil {
			return nil, fmt.Errorf("map source collection %q: %w", table.Ref.Path(), err)
		}
		folded := strings.ToLower(targetName)
		if prior, exists := targetNames[folded]; exists {
			return nil, fmt.Errorf("source collections %q and %q map to the same BigQuery table %q", prior, table.Ref.Path(), targetName)
		}
		targetNames[folded] = table.Ref.Path()
		mapped := &bigQueryMappedTable{table: table, targetName: targetName}
		fieldNames := map[string]string{}
		for _, sourceField := range table.Definition.Fields {
			original := string(sourceField.Name)
			fieldName, err := bigQueryIdentifier(original, 300)
			if err != nil {
				return nil, fmt.Errorf("map field %q in %q: %w", original, table.Ref.Path(), err)
			}
			if prior, exists := fieldNames[strings.ToLower(fieldName)]; exists {
				return nil, fmt.Errorf("source fields %q and %q in %q map to the same BigQuery field %q", prior, original, table.Ref.Path(), fieldName)
			}
			fieldNames[strings.ToLower(fieldName)] = original
			mappedType, precision, scale, err := bigQueryFieldType(sourceField, table.Definition.SourceDefinition)
			if err != nil {
				return nil, fmt.Errorf("field %q in %q: %w", original, table.Ref.Path(), err)
			}
			mode := "REQUIRED"
			if sourceField.Nullable {
				mode = "NULLABLE"
			}
			description := "Source field: " + original
			field := bqwriter.Field{Name: fieldName, Type: mappedType, Mode: mode, Description: description, Precision: precision, Scale: scale}
			mapped.schema = append(mapped.schema, field)
			mapped.fields = append(mapped.fields, bigQueryMappedField{sourceName: original, target: field, typeCode: sourceField.Type})
			if sourceField.Default != nil || sourceField.AutoIncrement {
				plan.warns = append(plan.warns, fmt.Sprintf("%s.%s: source default or auto-increment behavior is not reproduced in BigQuery", table.Ref.Path(), original))
			}
		}
		if len(mapped.schema) == 0 {
			return nil, fmt.Errorf("source collection %q has no fields", table.Ref.Path())
		}
		if len(table.Definition.PrimaryKey) > 0 {
			columns := make([]string, len(table.Definition.PrimaryKey))
			for i, sourcePK := range table.Definition.PrimaryKey {
				field, ok := mappedField(mapped, string(sourcePK))
				if !ok {
					return nil, fmt.Errorf("primary key field %q is missing from %q", sourcePK, table.Ref.Path())
				}
				columns[i] = field.target.Name
			}
			mapped.primaryKey = &bq.TableConstraintsPrimaryKey{Columns: columns}
			plan.warns = append(plan.warns, fmt.Sprintf("%s: BigQuery records the primary key as NOT ENFORCED; uniqueness is not checked", table.Ref.Path()))
		}
		if len(table.Definition.Indexes) > 0 {
			plan.warns = append(plan.warns, fmt.Sprintf("%s: BigQuery has no secondary indexes; %d source index definitions are not represented", table.Ref.Path(), len(table.Definition.Indexes)))
		}
		plan.tables[table.Ref.Path()] = mapped
		plan.order = append(plan.order, table.Ref.Path())
	}
	for _, key := range plan.order {
		mapped := plan.tables[key]
		for _, fk := range mapped.table.Definition.ForeignKeys {
			_, referenced, ok := plan.findReferenced(mapped.table, fk.ReferencedNamespace, fk.ReferencedCollection)
			if !ok {
				plan.warns = append(plan.warns, fmt.Sprintf("%s: foreign key %q was not declared because referenced collection %q is outside the selected transfer", key, fk.Name, fk.ReferencedCollection))
				continue
			}
			refFields := fk.ReferencedFields
			if len(refFields) == 0 {
				refFields = referenced.table.Definition.PrimaryKey
			}
			if len(fk.Fields) == 0 || len(fk.Fields) != len(refFields) || len(refFields) != len(referenced.table.Definition.PrimaryKey) {
				plan.warns = append(plan.warns, fmt.Sprintf("%s: foreign key %q was not declared because BigQuery only accepts references to a matching primary key", key, fk.Name))
				continue
			}
			columnRefs := make([]*bq.TableConstraintsForeignKeysColumnReferences, len(fk.Fields))
			valid := true
			for i := range fk.Fields {
				local, localOK := mappedField(mapped, string(fk.Fields[i]))
				foreign, foreignOK := mappedField(referenced, string(refFields[i]))
				if !localOK || !foreignOK || string(referenced.table.Definition.PrimaryKey[i]) != string(refFields[i]) {
					valid = false
					break
				}
				columnRefs[i] = &bq.TableConstraintsForeignKeysColumnReferences{ReferencingColumn: local.target.Name, ReferencedColumn: foreign.target.Name}
			}
			if !valid {
				plan.warns = append(plan.warns, fmt.Sprintf("%s: foreign key %q was not declared because its target columns do not match the referenced primary key", key, fk.Name))
				continue
			}
			mapped.foreignKeys = append(mapped.foreignKeys, &bq.TableConstraintsForeignKeys{
				Name: fk.Name,
				ReferencedTable: &bq.TableConstraintsForeignKeysReferencedTable{
					ProjectId: s.WriterProjectID(), DatasetId: s.WriterDatasetID(), TableId: referenced.targetName,
				},
				ColumnReferences: columnRefs,
			})
			warning := fmt.Sprintf("%s: BigQuery records foreign key %q as NOT ENFORCED; referential integrity is not checked", key, fk.Name)
			if fk.OnDelete != "" || fk.OnUpdate != "" {
				warning += " and source referential actions are not preserved"
			}
			plan.warns = append(plan.warns, warning)
		}
	}
	return plan, nil
}

// The writer intentionally exposes only destination identity, not credentials.
func (s BigQueryCopySink) WriterProjectID() string { return s.Writer.ProjectID() }
func (s BigQueryCopySink) WriterDatasetID() string { return s.Writer.DatasetID() }

func (p *bigQueryCopyPlan) Warnings() []string { return append([]string(nil), p.warns...) }

func (p *bigQueryCopyPlan) TargetName(table CopyTable) string {
	if mapped := p.tables[table.Ref.Path()]; mapped != nil {
		return mapped.targetName
	}
	return table.Ref.Path()
}

func (p *bigQueryCopyPlan) EncodeRow(table CopyTable, source dbschema.SourceRow) ([]byte, error) {
	mapped := p.tables[table.Ref.Path()]
	if mapped == nil || len(source.Values) != len(mapped.fields) {
		return nil, fmt.Errorf("source row shape differs from the described fields")
	}
	row := make(map[string]any, len(mapped.fields))
	for _, field := range mapped.fields {
		value, ok := source.Values[field.sourceName]
		if !ok {
			return nil, fmt.Errorf("source row is missing field %q", field.sourceName)
		}
		encoded, err := encodeBigQueryValue(field.target, field.typeCode, value, source.StorageClasses[field.sourceName])
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", field.sourceName, err)
		}
		row[field.target.Name] = encoded
	}
	data, err := json.Marshal(row)
	if err != nil {
		return nil, fmt.Errorf("encode row JSON: %w", err)
	}
	return append(data, '\n'), nil
}

func (p *bigQueryCopyPlan) Prepare(ctx context.Context) error {
	if err := p.writer.EnsureDataset(ctx); err != nil {
		return err
	}
	tableNames := make([]string, len(p.order))
	for i, key := range p.order {
		tableNames[i] = p.tables[key].targetName
	}
	if err := p.writer.CheckTablesAbsent(ctx, tableNames); err != nil {
		return err
	}
	for _, key := range p.order {
		mapped := p.tables[key]
		constraints := (*bq.TableConstraints)(nil)
		if mapped.primaryKey != nil {
			constraints = &bq.TableConstraints{PrimaryKey: mapped.primaryKey}
		}
		if err := p.writer.CreateTable(ctx, mapped.targetName, mapped.schema, constraints); err != nil {
			return fmt.Errorf("create table %q: %w", mapped.targetName, err)
		}
	}
	for _, key := range p.order {
		mapped := p.tables[key]
		if len(mapped.foreignKeys) == 0 {
			continue
		}
		constraints := &bq.TableConstraints{ForeignKeys: mapped.foreignKeys, PrimaryKey: mapped.primaryKey}
		if err := p.writer.SetConstraints(ctx, mapped.targetName, constraints); err != nil {
			return fmt.Errorf("declare constraints for %q: %w", mapped.targetName, err)
		}
	}
	return nil
}

func (p *bigQueryCopyPlan) LoadTable(ctx context.Context, table CopyTable, rows io.Reader) (string, int64, error) {
	mapped := p.tables[table.Ref.Path()]
	if mapped == nil {
		return "", 0, fmt.Errorf("source table is absent from transfer plan")
	}
	receipt, err := p.writer.LoadTable(ctx, mapped.targetName, mapped.schema, rows)
	return mapped.targetName, int64(receipt.Rows), err
}

func (p *bigQueryCopyPlan) findReferenced(source CopyTable, namespace, collection string) (string, *bigQueryMappedTable, bool) {
	for key, mapped := range p.tables {
		if mapped.table.Ref.Name() != collection {
			continue
		}
		wantNamespace := namespace
		if wantNamespace == "" {
			wantNamespace = source.Ref.Schema()
		}
		if mapped.table.Ref.Schema() == wantNamespace {
			return key, mapped, true
		}
	}
	return "", nil, false
}

func mappedField(table *bigQueryMappedTable, sourceName string) (bigQueryMappedField, bool) {
	for _, field := range table.fields {
		if field.sourceName == sourceName {
			return field, true
		}
	}
	return bigQueryMappedField{}, false
}

func bigQueryIdentifier(source string, maxLength int) (string, error) {
	if !utf8.ValidString(source) {
		return "", fmt.Errorf("source identifier is not valid UTF-8")
	}
	var out strings.Builder
	for _, r := range source {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			out.WriteRune(r)
		} else {
			fmt.Fprintf(&out, "_x%X_", r)
		}
	}
	name := out.String()
	if name == "" {
		return "", fmt.Errorf("source name is empty")
	}
	first, _ := utf8.DecodeRuneInString(name)
	if unicode.IsDigit(first) {
		name = "_" + name
	}
	if len(name) > maxLength {
		return "", fmt.Errorf("normalized identifier exceeds BigQuery's %d-character limit", maxLength)
	}
	return name, nil
}

func bigQueryFieldType(field dbschema.FieldDef, sourceDef *dbschema.SourceDefinition) (typ, precision, scale string, err error) {
	switch field.Type {
	case dbschema.Bool:
		return "BOOL", "", "", nil
	case dbschema.Int:
		return "INT64", "", "", nil
	case dbschema.Float:
		return "FLOAT64", "", "", nil
	case dbschema.String:
		return "STRING", "", "", nil
	case dbschema.Bytes:
		return "BYTES", "", "", nil
	case dbschema.Decimal:
		if field.Precision == nil {
			return "STRING", "", "", nil
		}
		p, s := field.Precision.Total, field.Precision.Scale
		if p < 1 || s < 0 || s > p {
			return "", "", "", fmt.Errorf("invalid decimal precision/scale")
		}
		if s <= 9 && p-s <= 29 && p <= 38 {
			return "NUMERIC", strconv.Itoa(p), strconv.Itoa(s), nil
		}
		if s <= 38 && p-s <= 38 && p <= 76 {
			return "BIGNUMERIC", strconv.Itoa(p), strconv.Itoa(s), nil
		}
		return "STRING", "", "", nil
	case dbschema.Time:
		declared := sourceDeclaredType(sourceDef, string(field.Name))
		switch {
		case declared == "date":
			return "DATE", "", "", nil
		case declared == "time" || strings.HasPrefix(declared, "time(") || strings.HasPrefix(declared, "time without time zone"):
			return "TIME", "", "", nil
		case strings.HasPrefix(declared, "time with time zone") || declared == "timetz":
			return "", "", "", fmt.Errorf("time values with a time zone cannot be represented without changing their meaning")
		case declared == "datetime":
			return "DATETIME", "", "", nil
		case strings.HasPrefix(declared, "timestamp with time zone"), declared == "timestamptz", strings.HasPrefix(declared, "timestamp without time zone"), declared == "timestamp":
			if strings.Contains(declared, "with time zone") || declared == "timestamptz" {
				return "TIMESTAMP", "", "", nil
			}
			return "DATETIME", "", "", nil
		default:
			return "", "", "", fmt.Errorf("temporal source type %q cannot be mapped without changing its meaning", declared)
		}
	default:
		return "", "", "", fmt.Errorf("source type %q has no lossless BigQuery mapping", field.Type)
	}
}

func sourceDeclaredType(def *dbschema.SourceDefinition, name string) string {
	if def == nil {
		return ""
	}
	for _, column := range def.Columns {
		if column.Name == name {
			return strings.ToLower(strings.TrimSpace(column.DeclaredType))
		}
	}
	return ""
}

func encodeBigQueryValue(field bqwriter.Field, sourceType dbschema.Type, value any, storageClass string) (any, error) {
	_ = sourceType
	_ = storageClass
	if value == nil {
		if field.Mode == "REQUIRED" {
			return nil, fmt.Errorf("required value is NULL")
		}
		return nil, nil
	}
	switch field.Type {
	case "STRING":
		text, ok := sourceText(value)
		if !ok {
			return nil, fmt.Errorf("value %T cannot be converted to exact text", value)
		}
		return text, nil
	case "BYTES":
		data, ok := value.([]byte)
		if !ok {
			return nil, fmt.Errorf("binary value has unsupported representation %T", value)
		}
		return base64.StdEncoding.EncodeToString(data), nil
	case "INT64":
		text, ok := integerSourceText(value)
		if !ok {
			return nil, fmt.Errorf("integer value has unsupported representation %T", value)
		}
		cell, err := bqwriter.NormalizeScalar(field, text)
		if err != nil {
			return nil, fmt.Errorf("integer is outside BigQuery INT64: %w", err)
		}
		return cell.Value, nil
	case "NUMERIC", "BIGNUMERIC":
		text, ok := exactDecimalText(value)
		if !ok {
			return nil, fmt.Errorf("decimal value has unsupported or lossy representation %T", value)
		}
		cell, err := bqwriter.NormalizeScalar(field, text)
		if err != nil {
			return nil, fmt.Errorf("decimal is outside the declared BigQuery range: %w", err)
		}
		return cell.Value, nil
	case "BOOL":
		text, ok := value.(bool)
		if ok {
			if text {
				return true, nil
			}
			return false, nil
		}
		if str, ok := value.(string); ok {
			cell, err := bqwriter.NormalizeScalar(field, str)
			if err == nil {
				return cell.Value, nil
			}
		}
		return nil, fmt.Errorf("boolean value has unsupported representation %T", value)
	case "FLOAT64":
		f, ok := floatingSource(value)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("float value has unsupported representation %T", value)
		}
		return f, nil
	case "DATE", "TIME", "DATETIME", "TIMESTAMP":
		text, ok := temporalSourceText(field.Type, value)
		if !ok {
			return nil, fmt.Errorf("temporal value has unsupported representation %T", value)
		}
		if field.Type == "TIMESTAMP" {
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil || parsed.Nanosecond()%1000 != 0 {
				return nil, fmt.Errorf("timestamp must be RFC3339 and no more precise than microseconds")
			}
			return parsed.UTC().Format("2006-01-02 15:04:05.999999-07:00"), nil
		}
		cell, err := bqwriter.NormalizeScalar(field, text)
		if err != nil {
			return nil, fmt.Errorf("invalid %s value: %w", field.Type, err)
		}
		return cell.Value, nil
	default:
		return nil, fmt.Errorf("BigQuery field type %q is unsupported", field.Type)
	}
}

func sourceText(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, utf8.ValidString(v)
	case []byte:
		text := string(v)
		return text, utf8.ValidString(text)
	case fmt.Stringer:
		text := v.String()
		return text, utf8.ValidString(text)
	default:
		return "", false
	}
}

func integerSourceText(value any) (string, bool) {
	switch v := value.(type) {
	case int:
		return strconv.FormatInt(int64(v), 10), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		if uint64(v) <= math.MaxInt64 {
			return strconv.FormatUint(uint64(v), 10), true
		}
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		if v <= math.MaxInt64 {
			return strconv.FormatUint(v, 10), true
		}
	case string:
		return v, true
	case json.Number:
		return string(v), true
	case *big.Int:
		if v != nil && v.IsInt64() {
			return v.String(), true
		}
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && math.Trunc(v) == v && math.Abs(v) <= 1<<53 {
			return strconv.FormatInt(int64(v), 10), true
		}
	}
	return "", false
}

func exactDecimalText(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case []byte:
		return string(v), true
	case int:
		return strconv.FormatInt(int64(v), 10), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case json.Number:
		return string(v), true
	case *big.Int:
		if v != nil {
			return v.String(), true
		}
	case *big.Rat:
		if v != nil && v.IsInt() {
			return v.Num().String(), true
		}
	case fmt.Stringer:
		return v.String(), true
	}
	return "", false
}

func floatingSource(value any) (float64, bool) {
	switch v := value.(type) {
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), float64(int64(float64(v))) == float64(v)
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func temporalSourceText(targetType string, value any) (string, bool) {
	if t, ok := value.(time.Time); ok {
		switch targetType {
		case "DATE":
			return t.Format("2006-01-02"), true
		case "TIME":
			return t.Format("15:04:05.999999"), t.Nanosecond()%1000 == 0
		case "DATETIME":
			return t.Format("2006-01-02T15:04:05.999999"), t.Nanosecond()%1000 == 0
		case "TIMESTAMP":
			return t.Format(time.RFC3339Nano), t.Nanosecond()%1000 == 0
		}
	}
	if v, ok := value.(string); ok {
		text := strings.TrimSpace(v)
		if targetType == "DATETIME" && len(text) == len("2006-01-02") {
			if _, err := time.Parse("2006-01-02", text); err == nil {
				// Some SQLite fixtures declare DATETIME but store date-only values.
				// BigQuery DATETIME requires a time component; midnight is the exact
				// lossless extension of that date-only value.
				text += "T00:00:00"
			}
		}
		return text, utf8.ValidString(v)
	}
	return "", false
}
