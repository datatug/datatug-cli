package chat

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	godbf "github.com/LindsayBradford/go-dbf"
	"github.com/ingr-io/ingr-go/ingr"
	"github.com/xuri/excelize/v2"
	"gopkg.in/yaml.v3"
)

type ExportFormat string

const (
	ExportCSV    ExportFormat = "csv"
	ExportJSON   ExportFormat = "json"
	ExportYAML   ExportFormat = "yaml"
	ExportINGR   ExportFormat = "ingr"
	ExportDBF    ExportFormat = "dbf"
	ExportSQLite ExportFormat = "sqlite"
	ExportXLSX   ExportFormat = "xlsx"
)

func ParseExportFormat(value string) (ExportFormat, error) {
	format := ExportFormat(strings.ToLower(strings.TrimSpace(value)))
	switch format {
	case ExportCSV, ExportJSON, ExportYAML, ExportINGR, ExportDBF, ExportSQLite, ExportXLSX:
		return format, nil
	default:
		return "", fmt.Errorf("unsupported export format %q (csv, json, yaml, ingr, dbf, sqlite, xlsx)", value)
	}
}

// ExportRecordSets writes immutable snapshots, never reruns their queries.
// For more than one RecordSet, flat formats are zipped; XLSX and SQLite hold
// all RecordSets in one workbook/database. No caller supplies SQL or a model.
func ExportRecordSets(ctx context.Context, records []RecordSet, format ExportFormat, output io.Writer) error {
	return exportRecordSets(ctx, records, format, output, false)
}

func ExportBucket(ctx context.Context, records []RecordSet, format ExportFormat, output io.Writer) error {
	return exportRecordSets(ctx, records, format, output, true)
}

func exportRecordSets(ctx context.Context, records []RecordSet, format ExportFormat, output io.Writer, bucket bool) error {
	if len(records) == 0 {
		return fmt.Errorf("export bucket is empty")
	}
	if output == nil {
		return fmt.Errorf("export output is unavailable")
	}
	switch format {
	case ExportXLSX:
		return exportXLSX(records, output)
	case ExportSQLite:
		return exportSQLite(ctx, records, output)
	case ExportCSV, ExportJSON, ExportYAML, ExportINGR, ExportDBF:
		if len(records) == 1 && !bucket {
			return exportFlat(records[0], format, output)
		}
		writer := zip.NewWriter(output)
		used := map[string]bool{}
		for index, record := range records {
			name := uniqueExportName(record.Title, index, used, 64) + "." + string(format)
			entry, err := writer.Create(name)
			if err != nil {
				_ = writer.Close()
				return err
			}
			if err := exportFlat(record, format, entry); err != nil {
				_ = writer.Close()
				return err
			}
		}
		return writer.Close()
	default:
		return fmt.Errorf("unsupported export format %q", format)
	}
}

func exportFlat(record RecordSet, format ExportFormat, output io.Writer) error {
	columns, rows := record.Result.Columns, record.Result.Rows
	switch format {
	case ExportCSV:
		writer := csv.NewWriter(output)
		if err := writer.Write(columns); err != nil {
			return err
		}
		for _, row := range rows {
			values := make([]string, len(columns))
			for i, column := range columns {
				if value := row.Data[column]; value != nil {
					values[i] = FormatValue(value)
				}
			}
			if err := writer.Write(values); err != nil {
				return err
			}
		}
		writer.Flush()
		return writer.Error()
	case ExportJSON, ExportYAML:
		data := struct {
			Columns []string `json:"columns" yaml:"columns"`
			Rows    [][]any  `json:"rows" yaml:"rows"`
		}{Columns: append([]string(nil), columns...), Rows: make([][]any, len(rows))}
		for i, row := range rows {
			data.Rows[i] = make([]any, len(columns))
			for j, column := range columns {
				data.Rows[i][j] = exportValue(row.Data[column])
			}
		}
		if format == ExportJSON {
			encoder := json.NewEncoder(output)
			encoder.SetIndent("", "  ")
			return encoder.Encode(data)
		}
		return yaml.NewEncoder(output).Encode(data)
	case ExportINGR:
		writer := ingr.NewRecordsWriter(output)
		definitions := make([]ingr.ColDef, 0, len(columns)+1)
		definitions = append(definitions, ingr.ColDef{Name: "$ID"})
		for _, column := range columns {
			if column != "$ID" {
				definitions = append(definitions, ingr.ColDef{Name: column})
			}
		}
		if _, err := writer.WriteHeader(record.Title, definitions); err != nil {
			return err
		}
		for i, row := range rows {
			values := make(map[string]any, len(columns))
			for _, column := range columns {
				if column != "$ID" {
					values[column] = exportValue(row.Data[column])
				}
			}
			id := any(i + 1)
			if row.Key != "" {
				id = row.Key
			}
			if value, ok := row.Data["$ID"]; ok && value != nil {
				id = value
			}
			if _, err := writer.WriteRecords(0, ingr.NewMapRecordEntry(fmt.Sprint(id), values)); err != nil {
				return err
			}
		}
		return writer.Close()
	case ExportDBF:
		return exportDBF(record, output)
	default:
		return fmt.Errorf("unsupported flat export format %q", format)
	}
}

func exportValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		return typed // preserve decimal precision rather than rounding to float64
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	default:
		return value
	}
}

var invalidExportName = regexp.MustCompile(`[^[:alnum:]_-]+`)

func uniqueExportName(title string, index int, used map[string]bool, maxLength int) string {
	base := strings.Trim(invalidExportName.ReplaceAllString(title, "_"), "_")
	if base == "" {
		base = fmt.Sprintf("RecordSet_%d", index+1)
	}
	if len(base) > maxLength {
		base = base[:maxLength]
	}
	name := base
	for suffix := 2; used[strings.ToLower(name)]; suffix++ {
		end := fmt.Sprintf("_%d", suffix)
		name = base[:min(len(base), maxLength-len(end))] + end
	}
	used[strings.ToLower(name)] = true
	return name
}

func exportXLSX(records []RecordSet, output io.Writer) error {
	book := excelize.NewFile()
	defer func() { _ = book.Close() }()
	used := map[string]bool{}
	for index, record := range records {
		name := uniqueExportName(record.Title, index, used, 31)
		if index == 0 {
			if err := book.SetSheetName("Sheet1", name); err != nil {
				return err
			}
		} else if _, err := book.NewSheet(name); err != nil {
			return err
		}
		for column, title := range record.Result.Columns {
			cell, _ := excelize.CoordinatesToCellName(column+1, 1)
			if err := book.SetCellValue(name, cell, title); err != nil {
				return err
			}
		}
		for rowIndex, row := range record.Result.Rows {
			for columnIndex, column := range record.Result.Columns {
				value := exportValue(row.Data[column])
				if number, ok := value.(json.Number); ok {
					value = number.String()
				}
				if binary, ok := value.([]byte); ok {
					value = FormatValue(binary)
				}
				if value == nil {
					continue
				}
				// Formula-looking strings are data, not executable formulas.
				cell, _ := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+2)
				if err := book.SetCellValue(name, cell, value); err != nil {
					return err
				}
			}
		}
	}
	return book.Write(output)
}

func exportSQLite(ctx context.Context, records []RecordSet, output io.Writer) error {
	file, err := os.CreateTemp("", "datatug-export-*.sqlite")
	if err != nil {
		return err
	}
	path := file.Name()
	_ = file.Close()
	defer func() { _ = os.Remove(path) }()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for index, record := range records {
		name := uniqueExportName(record.Title, index, used, 64)
		fields := make([]string, len(record.Result.Columns))
		for i, column := range record.Result.Columns {
			fields[i] = quoteSQLite(column) + " " + sqliteColumnType(record, column)
		}
		if len(fields) == 0 {
			_ = db.Close()
			return fmt.Errorf("RecordSet %q has no columns", record.Title)
		}
		if _, err := db.ExecContext(ctx, "CREATE TABLE "+quoteSQLite(name)+" ("+strings.Join(fields, ",")+")"); err != nil {
			_ = db.Close()
			return err
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(fields)), ",")
		statement, err := db.PrepareContext(ctx, "INSERT INTO "+quoteSQLite(name)+" VALUES ("+placeholders+")")
		if err != nil {
			_ = db.Close()
			return err
		}
		for _, row := range record.Result.Rows {
			values := make([]any, len(record.Result.Columns))
			for i, column := range record.Result.Columns {
				values[i] = exportValue(row.Data[column])
				if number, ok := values[i].(json.Number); ok {
					values[i] = number.String()
				}
			}
			if _, err := statement.ExecContext(ctx, values...); err != nil {
				_ = statement.Close()
				_ = db.Close()
				return err
			}
		}
		_ = statement.Close()
	}
	if err := db.Close(); err != nil {
		return err
	}
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	_, err = io.Copy(output, input)
	return err
}

func quoteSQLite(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func sqliteColumnType(record RecordSet, column string) string {
	typeName := ""
	for _, row := range record.Result.Rows {
		value := row.Data[column]
		if value == nil {
			continue
		}
		candidate := "TEXT"
		switch value.(type) {
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			candidate = "INTEGER"
		case float32, float64:
			candidate = "REAL"
		case []byte:
			candidate = "BLOB"
		case json.Number:
			candidate = "TEXT"
			if _, err := value.(json.Number).Int64(); err == nil {
				candidate = "INTEGER"
			}
		}
		if typeName != "" && typeName != candidate {
			return "BLOB"
		}
		typeName = candidate
	}
	if typeName == "" {
		return "BLOB"
	}
	return typeName
}

func exportDBF(record RecordSet, output io.Writer) error {
	table := godbf.New("UTF8")
	used := map[string]bool{}
	names := make([]string, len(record.Result.Columns))
	kinds := make([]string, len(record.Result.Columns))
	for i, column := range record.Result.Columns {
		name := uniqueExportName(column, i, used, 10)
		names[i] = name
		kind, scale := dbfColumnKind(record, column)
		kinds[i] = kind
		var err error
		switch kind {
		case "integer":
			err = table.AddNumberField(name, 20, 0)
		case "float":
			err = table.AddFloatField(name, 20, scale)
		case "boolean":
			err = table.AddBooleanField(name)
		case "date":
			err = table.AddDateField(name)
		default:
			err = table.AddTextField(name, 254)
		}
		if err != nil {
			return err
		}
	}
	for _, row := range record.Result.Rows {
		index, err := table.AddNewRecord()
		if err != nil {
			return err
		}
		for i, column := range record.Result.Columns {
			value := row.Data[column]
			if value == nil {
				continue
			}
			text := dbfValue(value, kinds[i])
			limit := 254
			if kinds[i] == "integer" || kinds[i] == "float" {
				limit = 20
			}
			if len([]byte(text)) > limit {
				return fmt.Errorf("DBF cannot store %s value longer than %d bytes", column, limit)
			}
			if err := table.SetFieldValueByName(index, names[i], text); err != nil {
				return err
			}
		}
	}
	file, err := os.CreateTemp("", "datatug-export-*.dbf")
	if err != nil {
		return err
	}
	path := file.Name()
	_ = file.Close()
	defer func() { _ = os.Remove(path) }()
	if err := godbf.SaveToFile(table, path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// godbf's from-scratch writer omits the dBase EOF marker after records,
	// although its own reader requires it. Complete the file before export.
	data = append(data, 0x1a)
	_, err = io.Copy(output, bytes.NewReader(data))
	return err
}

func dbfValue(value any, kind string) string {
	if kind == "boolean" {
		if value == true {
			return "T"
		}
		return "F"
	}
	if kind == "date" {
		if date, ok := value.(time.Time); ok {
			return date.Format("20060102")
		}
	}
	return FormatValue(value)
}

func dbfColumnKind(record RecordSet, column string) (kind string, scale uint8) {
	for _, row := range record.Result.Rows {
		value := row.Data[column]
		if value == nil {
			continue
		}
		candidate := "text"
		switch typed := value.(type) {
		case bool:
			candidate = "boolean"
		case time.Time:
			if typed.Hour() == 0 && typed.Minute() == 0 && typed.Second() == 0 && typed.Nanosecond() == 0 {
				candidate = "date"
			}
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			candidate = "integer"
		case float32, float64, json.Number:
			candidate = "float"
			if number, ok := typed.(json.Number); ok {
				if _, err := number.Int64(); err == nil {
					candidate = "integer"
				}
			}
		}
		if candidate == "integer" || candidate == "float" {
			text := FormatValue(value)
			if len(text) > 20 || strings.ContainsAny(text, "eE") {
				candidate = "text"
			}
			if dot := strings.IndexByte(text, '.'); dot >= 0 {
				places := len(text) - dot - 1
				if places > 8 {
					candidate = "text"
				} else {
					scale = max(scale, uint8(places))
				}
			}
		}
		if kind == "" {
			kind = candidate
			continue
		}
		if kind == candidate {
			continue
		}
		if (kind == "integer" && candidate == "float") || (kind == "float" && candidate == "integer") {
			kind = "float"
			continue
		}
		kind = "text"
	}
	if kind == "" {
		kind = "text"
	}
	return kind, scale
}

// ExportBucketFile writes atomically: a failed conversion leaves an existing
// target untouched. The caller selects explicit path and format.
func ExportBucketFile(ctx context.Context, records []RecordSet, format ExportFormat, path string) error {
	return exportFile(ctx, records, format, path, true)
}

func ExportRecordSetFile(ctx context.Context, record RecordSet, format ExportFormat, path string) error {
	return exportFile(ctx, []RecordSet{record}, format, path, false)
}

func exportFile(ctx context.Context, records []RecordSet, format ExportFormat, path string, bucket bool) error {
	if path == "" {
		return fmt.Errorf("export path is required")
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("export target already exists: %s", path)
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".datatug-export-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	var exportErr error
	if bucket {
		exportErr = ExportBucket(ctx, records, format, temp)
	} else {
		exportErr = ExportRecordSets(ctx, records, format, temp)
	}
	if exportErr != nil {
		_ = temp.Close()
		return exportErr
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Hard-link creation is exclusive: a target created after the earlier
	// existence check cannot be overwritten by a racing export.
	return os.Link(temp.Name(), path)
}
