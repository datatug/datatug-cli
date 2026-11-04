package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func TestAthenaPointFramesNativeFidelity(t *testing.T) {
	ctx := context.Background()
	id, collection := "point\x00id", "people"
	table := athenaTableConfig{IDColumn: "id"}
	wide, empty := "12345678901234567890.000100", ""
	columns := []athenasdk.Column{
		{Name: "id", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: collection},
		{Name: "wide", Type: "decimal(38,4)", Label: "wide", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: collection},
		{Name: "empty", Type: "varchar", Label: "empty", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: collection},
		{Name: "sqlNull", Type: "varchar", Label: "sqlNull", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: collection},
	}
	checkFrame := func(t *testing.T, frame []byte, candidate athenaWireBudget) {
		t.Helper()
		athenaValuesCheck(t, candidate.valid() && candidate.bytes == len(frame), "budget %+v, frame %d", candidate, len(frame))
		athenaValuesCheck(t, len(frame) > 1 && frame[len(frame)-1] == '\n' && bytes.Count(frame, []byte{'\n'}) == 1 &&
			json.Valid(frame[:len(frame)-1]), "not one JSON object plus one LF: %q", frame)
	}
	request := admittedAthenaRead{operation: "get", collection: collection, id: id, table: table}

	t.Run("get document native text/NULL/empty/NUL ID", func(t *testing.T) {
		document := &athenasdk.Document{ID: id, Columns: columns, Data: map[string]*string{
			"id": &id, "wide": &wide, "empty": &empty, "sqlNull": nil,
		}}
		frame, candidate, err := athenaGetFrame(ctx, request, document, athenaWireBudget{})
		athenaValuesCheck(t, err == nil, "get frame error: %v", err)
		checkFrame(t, frame, candidate)
		envelope := athenaValuesObject(t, frame, "operation", "format", "collection", "document")
		athenaValuesCheck(t, athenaValuesText(t, envelope["operation"]) == "get" &&
			athenaValuesText(t, envelope["format"]) == athenaPointFormat &&
			athenaValuesText(t, envelope["collection"]) == collection, "get envelope changed")
		wire := athenaValuesObject(t, envelope["document"], "id", "data", "columns")
		athenaValuesCheck(t, athenaValuesText(t, wire["id"]) == id, "NUL ID did not roundtrip: %s", wire["id"])
		data := athenaValuesObject(t, wire["data"], "id", "wide", "empty", "sqlNull")
		athenaValuesCheck(t, athenaValuesText(t, data["id"]) == id && athenaValuesText(t, data["wide"]) == wide &&
			athenaValuesText(t, data["empty"]) == "", "native text values changed")
		athenaValuesCheck(t, string(bytes.TrimSpace(data["sqlNull"])) == "null", "SQL NULL became %s", data["sqlNull"])
		var wireColumns []json.RawMessage
		athenaValuesCheck(t, json.Unmarshal(wire["columns"], &wireColumns) == nil && len(wireColumns) == len(columns), "columns changed")
		for i, raw := range wireColumns {
			column := athenaValuesObject(t, raw, "name", "type", "label", "nullable", "catalogName", "schemaName", "tableName")
			athenaValuesCheck(t, athenaValuesText(t, column["name"]) == columns[i].Name &&
				athenaValuesText(t, column["type"]) == columns[i].Type, "column %d changed", i)
		}
	})

	t.Run("get missing null", func(t *testing.T) {
		frame, candidate, err := athenaGetFrame(ctx, request, nil, athenaWireBudget{})
		athenaValuesCheck(t, err == nil, "missing frame error: %v", err)
		checkFrame(t, frame, candidate)
		envelope := athenaValuesObject(t, frame, "operation", "format", "collection", "document")
		athenaValuesCheck(t, string(bytes.TrimSpace(envelope["document"])) == "null", "missing document is not null: %s", envelope["document"])
	})

	t.Run("exists false vs testConnection false requestCompleted true", func(t *testing.T) {
		existsRequest := request
		existsRequest.operation = "exists"
		frame, candidate, err := athenaExistsFrame(ctx, existsRequest, false, athenaWireBudget{})
		athenaValuesCheck(t, err == nil, "exists frame error: %v", err)
		checkFrame(t, frame, candidate)
		envelope := athenaValuesObject(t, frame, "operation", "format", "collection", "id", "exists")
		athenaValuesCheck(t, athenaValuesText(t, envelope["operation"]) == "exists" && athenaValuesText(t, envelope["id"]) == id, "exists envelope changed")
		athenaValuesFalse(t, envelope["exists"])
		_, hasCompleted := envelope["requestCompleted"]
		athenaValuesCheck(t, !hasCompleted, "ordinary exists emitted requestCompleted")

		testRequest := request
		testRequest.operation = "testConnection"
		frame, candidate, err = athenaExistsFrame(ctx, testRequest, false, athenaWireBudget{})
		athenaValuesCheck(t, err == nil, "testConnection frame error: %v", err)
		checkFrame(t, frame, candidate)
		envelope = athenaValuesObject(t, frame, "operation", "format", "collection", "id", "exists", "requestCompleted")
		athenaValuesCheck(t, athenaValuesText(t, envelope["operation"]) == "testConnection" && athenaValuesText(t, envelope["id"]) == id, "testConnection envelope changed")
		athenaValuesFalse(t, envelope["exists"])
		athenaValuesCheck(t, athenaValuesJSONBool(t, envelope["requestCompleted"]), "testConnection requestCompleted is not true")
	})
}
