package dbcopy

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dal-go/dalgo/dbschema"
	"github.com/ingitdb/ingitdb-go/ingitdb"
	"github.com/ingr-io/ingr-go/ingr"
)

// ExportOptions applies one records-file format to every exported table.
// The zero value uses JSON, preserving the original CLI behavior.
type ExportOptions struct{ RecordsFormat string }

func normalizeExportFormat(raw string) (ingitdb.RecordFormat, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "json":
		return ingitdb.RecordFormatJSON, nil
	case "jsonl":
		return ingitdb.RecordFormatJSONL, nil
	case "ingr":
		return ingitdb.RecordFormatINGR, nil
	case "csv":
		return ingitdb.RecordFormatCSV, nil
	case "yaml", "yml":
		return ingitdb.RecordFormatYAML, nil
	default:
		return "", fmt.Errorf("unsupported records format %q (choose json, jsonl, ingr, csv, or yaml)", raw)
	}
}

func exportRecordFile(format ingitdb.RecordFormat) *ingitdb.RecordFileDef {
	r := &ingitdb.RecordFileDef{Format: format, RecordType: ingitdb.MapOfRecords}
	switch format {
	case ingitdb.RecordFormatJSON:
		r.Name = "records.json"
	case ingitdb.RecordFormatJSONL:
		r.Name = "records.jsonl"
		r.RecordType = ingitdb.ListOfRecords
	case ingitdb.RecordFormatINGR:
		r.Name = "records.ingr"
	case ingitdb.RecordFormatCSV:
		r.Name = "records.csv"
		r.RecordType = ingitdb.ListOfRecords
		r.CSVCellEncoding = "json-v1"
	case ingitdb.RecordFormatYAML:
		r.Name = "records.yaml"
	}
	return r
}

type exportRecordsWriter struct {
	format   ingitdb.RecordFormat
	buffered *bufio.Writer
	csv      *csv.Writer
	ingr     ingr.RecordsWriter
	fields   []dbschema.FieldDef
	count    int64
}

func newExportRecordsWriter(out io.Writer, format ingitdb.RecordFormat, collection string, fields []dbschema.FieldDef) (*exportRecordsWriter, error) {
	w := &exportRecordsWriter{format: format, buffered: bufio.NewWriterSize(out, 256*1024), fields: fields}
	switch format {
	case ingitdb.RecordFormatJSON:
		if _, err := w.buffered.WriteString("{\n"); err != nil {
			return nil, err
		}
	case ingitdb.RecordFormatINGR:
		w.ingr = ingr.NewRecordsWriter(w.buffered)
		cols := make([]ingr.ColDef, 0, len(fields)+1)
		cols = append(cols, ingr.ColDef{Name: "$ID"})
		for _, field := range fields {
			cols = append(cols, ingr.ColDef{Name: string(field.Name)})
		}
		if _, err := w.ingr.WriteHeader(collection, cols); err != nil {
			return nil, err
		}
	case ingitdb.RecordFormatCSV:
		w.csv = csv.NewWriter(w.buffered)
		header := make([]string, 0, len(fields)+1)
		header = append(header, "$ID")
		for _, field := range fields {
			header = append(header, string(field.Name))
		}
		if err := w.csv.Write(header); err != nil {
			return nil, err
		}
	case ingitdb.RecordFormatJSONL, ingitdb.RecordFormatYAML:
	default:
		return nil, fmt.Errorf("unsupported records format %q", format)
	}
	return w, nil
}

func (w *exportRecordsWriter) Write(id string, row map[string]any) error {
	switch w.format {
	case ingitdb.RecordFormatJSON, ingitdb.RecordFormatYAML:
		key, _ := json.Marshal(id)
		value, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if w.format == ingitdb.RecordFormatJSON {
			if w.count > 0 {
				if _, err := w.buffered.WriteString(",\n"); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w.buffered, "  %s: %s", key, value); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w.buffered, "%s: %s\n", key, value); err != nil {
				return err
			}
		}
	case ingitdb.RecordFormatJSONL:
		withID := make(map[string]any, len(row)+1)
		for k, v := range row {
			withID[k] = v
		}
		withID["$ID"] = id
		data, err := json.Marshal(withID)
		if err != nil {
			return err
		}
		if _, err := w.buffered.Write(data); err != nil {
			return err
		}
		if err := w.buffered.WriteByte('\n'); err != nil {
			return err
		}
	case ingitdb.RecordFormatINGR:
		withID := make(map[string]any, len(row)+1)
		for k, v := range row {
			withID[k] = v
		}
		withID["$ID"] = id
		if _, err := w.ingr.WriteRecords(0, ingr.NewMapRecordEntry(id, withID)); err != nil {
			return err
		}
	case ingitdb.RecordFormatCSV:
		cells := make([]string, len(w.fields)+1)
		idJSON, _ := json.Marshal(id)
		cells[0] = string(idJSON)
		for i, field := range w.fields {
			encoded, err := json.Marshal(row[string(field.Name)])
			if err != nil {
				return err
			}
			cells[i+1] = string(encoded)
		}
		if err := w.csv.Write(cells); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported records format %q", w.format)
	}
	w.count++
	return nil
}

func (w *exportRecordsWriter) Close() error {
	switch w.format {
	case ingitdb.RecordFormatJSON:
		if _, err := w.buffered.WriteString("\n}\n"); err != nil {
			return err
		}
	case ingitdb.RecordFormatINGR:
		if err := w.ingr.Close(); err != nil {
			return err
		}
	case ingitdb.RecordFormatCSV:
		w.csv.Flush()
		if err := w.csv.Error(); err != nil {
			return err
		}
	}
	return w.buffered.Flush()
}
