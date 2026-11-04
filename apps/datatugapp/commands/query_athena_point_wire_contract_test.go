package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func pointWireContractColumns() []athenasdk.Column {
	return []athenasdk.Column{
		{Name: "id", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"},
		{Name: "amount", Type: "decimal(38,10)", Label: "amount", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: "tbl", Precision: athenaValuesInt64(38), Scale: athenaValuesInt64(10), CaseSensitive: athenaValuesBool(false)},
		{Name: "note", Type: "varchar", Label: "note", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"},
	}
}

func pointWireContractDoc(id string, amount, note *string) *athenasdk.Document {
	idCopy := id
	if amount == nil {
		amount = athenaValuesString("1")
	}
	return &athenasdk.Document{ID: id, Columns: pointWireContractColumns(), Data: map[string]*string{"id": &idCopy, "amount": amount, "note": note}}
}

func pointWireContractRequest(operation string, ids ...string) admittedAthenaRead {
	request := admittedAthenaRead{operation: operation, collection: "coll", ids: ids, table: athenaTableConfig{Name: "tbl", IDColumn: "id"}}
	if operation == "get" {
		request.id = ids[0]
	}
	return request
}

func TestAthenaPointWireContractGetManyOrder(t *testing.T) {
	ctx := context.Background()
	wide := "1234567890123456789012345678.0000000001"
	nulID := "a\x00"
	docB := pointWireContractDoc("b", athenaValuesString(wide), athenaValuesString(""))
	docA1 := pointWireContractDoc(nulID, athenaValuesString("0.0000000001"), nil)
	docA2 := pointWireContractDoc(nulID, athenaValuesString("0.0000000002"), athenaValuesString("second"))
	request := pointWireContractRequest("getMany", "b", "missing", nulID, nulID)
	frame, next, err := athenaGetManyFrame(ctx, request, []*athenasdk.Document{docB, nil, docA1, docA2}, athenaWireBudget{})
	athenaValuesCheck(t, err == nil && frame != nil, "frame: %v", err)
	saved := append([]byte(nil), frame...)
	athenaValuesCheck(t, next.valid() && next.bytes == len(frame) && next.rows == 4, "budget %+v len %d", next, len(frame))
	athenaValuesCheck(t, frame[len(frame)-1] == '\n' && bytes.Count(frame, []byte{'\n'}) == 1 && json.Valid(frame[:len(frame)-1]), "not one JSONL frame")
	envelope := athenaValuesObject(t, frame, "operation", "format", "collection", "documents")
	athenaValuesCheck(t, athenaValuesText(t, envelope["operation"]) == "getMany" && athenaValuesText(t, envelope["format"]) == athenaPointFormat && athenaValuesText(t, envelope["collection"]) == "coll", "header changed")
	var documents []json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(envelope["documents"], &documents) == nil && len(documents) == 4 && string(bytes.TrimSpace(documents[1])) == "null", "documents shape changed")
	first := athenaValuesObject(t, documents[0], "id", "data", "columns")
	data := athenaValuesObject(t, first["data"], "id", "amount", "note")
	athenaValuesCheck(t, athenaValuesText(t, first["id"]) == "b" && athenaValuesText(t, data["id"]) == "b" && athenaValuesText(t, data["amount"]) == wide && athenaValuesText(t, data["note"]) == "", "first document changed")
	third := athenaValuesObject(t, documents[2], "id", "data", "columns")
	thirdData := athenaValuesObject(t, third["data"], "id", "amount", "note")
	athenaValuesCheck(t, athenaValuesText(t, third["id"]) == nulID && athenaValuesText(t, thirdData["id"]) == nulID && string(bytes.TrimSpace(thirdData["note"])) == "null" && athenaValuesText(t, thirdData["amount"]) == "0.0000000001", "NUL document changed")
	fourth := athenaValuesObject(t, documents[3], "id", "data", "columns")
	fourthData := athenaValuesObject(t, fourth["data"], "id", "amount", "note")
	athenaValuesCheck(t, athenaValuesText(t, fourth["id"]) == nulID && athenaValuesText(t, fourthData["amount"]) == "0.0000000002" && athenaValuesText(t, fourthData["note"]) == "second", "duplicate ordering changed")
	var columns []json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(first["columns"], &columns) == nil && len(columns) == 3, "columns shape changed")
	base := []string{"name", "type", "label", "nullable", "catalogName", "schemaName", "tableName"}
	idColumn := athenaValuesObject(t, columns[0], base...)
	amountColumn := athenaValuesObject(t, columns[1], append(append([]string{}, base...), "precision", "scale", "caseSensitive")...)
	noteColumn := athenaValuesObject(t, columns[2], base...)
	athenaValuesCheck(t, athenaValuesText(t, idColumn["nullable"]) == "UNKNOWN" && athenaValuesText(t, amountColumn["nullable"]) == "NULLABLE" && athenaValuesText(t, noteColumn["nullable"]) == "NULLABLE", "nullable text changed")
	athenaValuesCheck(t, athenaValuesDescriptor(t, amountColumn["precision"]) == "38" && athenaValuesDescriptor(t, amountColumn["scale"]) == "10" && !athenaValuesJSONBool(t, amountColumn["caseSensitive"]), "numeric descriptors changed")
	for _, token := range []string{`"receipt"`, `"cursor"`, `"nextCursor"`, `"queryId"`} {
		athenaValuesCheck(t, !bytes.Contains(frame, []byte(token)), "unexpected %s", token)
	}
	docB.Data["id"] = athenaValuesString("MUTATED")
	*docA1.Data["amount"] = "MUTATED"
	docA1.Columns[1].Name = "MUTATED"
	docA1.Columns[1].Precision = athenaValuesInt64(1)
	docA1.Data = nil
	athenaValuesCheck(t, bytes.Equal(frame, saved), "frame changed after native mutation")
}

func TestAthenaPointWireContractRefusals(t *testing.T) {
	ctx := context.Background()
	fixture := func() (admittedAthenaRead, []*athenasdk.Document) {
		return pointWireContractRequest("getMany", "null-note", "empty-note"),
			[]*athenasdk.Document{pointWireContractDoc("null-note", nil, nil), pointWireContractDoc("empty-note", nil, athenaValuesString(""))}
	}
	request, documents := fixture()
	frame, prior, err := athenaGetManyFrame(ctx, request, documents, athenaWireBudget{})
	athenaValuesCheck(t, err == nil && frame != nil && prior.valid() && prior.rows == 2 && prior.bytes == len(frame), "valid fixture: %v", err)
	athenaValuesCheck(t, bytes.Contains(frame, []byte(`"note":null`)) && bytes.Contains(frame, []byte(`"note":""`)), "SQL NULL/empty string witnesses changed")
	bad := string([]byte{0xff})
	cases := []struct {
		name   string
		mutate func(*athenasdk.Document)
	}{
		{"finalSlotIDMismatch", func(document *athenasdk.Document) { document.ID = "other" }},
		{"missingDataKey", func(document *athenasdk.Document) { delete(document.Data, "note") }},
		{"extraDataKey", func(document *athenasdk.Document) { document.Data["extra"] = athenaValuesString("x") }},
		{"nilData", func(document *athenasdk.Document) { document.Data = nil }},
		{"nilIDValue", func(document *athenasdk.Document) { document.Data["id"] = nil }},
		{"duplicateColumnName", func(document *athenasdk.Document) { document.Columns[1].Name = document.Columns[0].Name }},
		{"zeroColumns", func(document *athenasdk.Document) { document.Columns = nil }},
		{"invalidUTF8Payload", func(document *athenasdk.Document) { document.Data["note"] = &bad }},
		{"invalidUTF8SchemaName", func(document *athenasdk.Document) { document.Columns[0].SchemaName = bad }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, documents := fixture()
			testCase.mutate(documents[1])
			snapshot := prior
			frame, next, err := athenaGetManyFrame(ctx, request, documents, prior)
			athenaValuesCheck(t, err == errAthenaOutput && frame == nil && next == snapshot && prior == snapshot, "not a fixed refusal: err=%v frameIsNil=%t next=%+v", err, frame == nil, next)
		})
	}
}

func pointWireContractNodeColumns() []athenasdk.Column {
	columns := make([]athenasdk.Column, athenaMaxColumns)
	columns[0] = athenasdk.Column{Name: "id", Type: "varchar"}
	for i := 1; i < len(columns); i++ {
		columns[i] = athenasdk.Column{Name: "c" + strconv.Itoa(i), Type: "decimal(38,10)", Precision: athenaValuesInt64(38), Scale: athenaValuesInt64(10), CaseSensitive: athenaValuesBool(false)}
	}
	return columns
}

func pointWireContractNodeDoc(id string, columns []athenasdk.Column) *athenasdk.Document {
	idCopy := id
	one := "1"
	data := make(map[string]*string, len(columns))
	data["id"] = &idCopy
	for _, column := range columns[1:] {
		data[column.Name] = &one
	}
	return &athenasdk.Document{ID: id, Columns: columns, Data: data}
}

func TestAthenaPointWireContractEncodedBounds(t *testing.T) {
	ctx := context.Background()
	payload := strings.Repeat("<", 768<<10)
	athenaValuesCheck(t, len(payload) < athenaMaxWireText && len(payload) < 1<<20, "native payload inflated")
	columns := []athenasdk.Column{
		{Name: "id", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"},
		{Name: "payload", Type: "varchar", Label: "payload", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"},
	}
	payloadDoc := func(id string) *athenasdk.Document {
		idCopy := id
		return &athenasdk.Document{ID: id, Columns: columns, Data: map[string]*string{"id": &idCopy, "payload": &payload}}
	}
	frame, used, err := athenaGetFrame(ctx, pointWireContractRequest("get", "b1"), payloadDoc("b1"), athenaWireBudget{})
	athenaValuesCheck(t, err == nil && frame != nil && len(frame) < athenaMaxWireBytes && used.bytes == len(frame) && used.rows == 1, "single document: %v", err)
	athenaValuesCheck(t, frame[len(frame)-1] == '\n' && bytes.Count(frame, []byte{'\n'}) == 1 && json.Valid(frame[:len(frame)-1]), "single document JSONL changed")
	escaped, marshalErr := json.Marshal(payload)
	athenaValuesCheck(t, marshalErr == nil && len(escaped) > len(payload) && len(frame)+len(escaped) > athenaMaxWireBytes, "HTML escaping did not expand beyond 8 MiB")
	prior := athenaWireBudget{}
	pairFrame, next, err := athenaGetManyFrame(ctx, pointWireContractRequest("getMany", "b1", "b2"), []*athenasdk.Document{payloadDoc("b1"), payloadDoc("b2")}, prior)
	athenaValuesCheck(t, err == errAthenaOutput && pairFrame == nil && next == prior && prior == (athenaWireBudget{}), "two-document overflow: err=%v", err)

	nodeColumns := pointWireContractNodeColumns()
	athenaValuesCheck(t, len(nodeColumns) == athenaMaxColumns, "column profile changed")
	ids := make([]string, 17)
	documents := make([]*athenasdk.Document, 17)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
		documents[i] = pointWireContractNodeDoc(ids[i], nodeColumns)
	}
	athenaValuesCheck(t, len(ids) < athenaMaxWireRows && len(ids) <= 100, "record fixture inflated")
	frame, used, err = athenaGetManyFrame(ctx, pointWireContractRequest("getMany", ids[:16]...), documents[:16], athenaWireBudget{})
	athenaValuesCheck(t, err == nil && frame != nil && used.rows == 16 && used.nodes <= athenaMaxWireNodes && used.bytes == len(frame) && len(frame) < athenaMaxWireBytes && 2*len(frame) < athenaMaxWireBytes, "16 documents: %v", err)
	envelope := athenaValuesObject(t, frame, "operation", "format", "collection", "documents")
	var wireDocuments []json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(envelope["documents"], &wireDocuments) == nil && len(wireDocuments) == 16, "16-document envelope changed")
	document := athenaValuesObject(t, wireDocuments[0], "id", "data", "columns")
	var wireColumns []json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(document["columns"], &wireColumns) == nil && len(wireColumns) == athenaMaxColumns, "wire columns changed")
	base := []string{"name", "type", "label", "nullable", "catalogName", "schemaName", "tableName"}
	for i, raw := range wireColumns {
		if i == 0 {
			athenaValuesObject(t, raw, base...)
			continue
		}
		expanded := athenaValuesObject(t, raw, append(append([]string{}, base...), "precision", "scale", "caseSensitive")...)
		athenaValuesCheck(t, athenaValuesDescriptor(t, expanded["precision"]) == "38" && athenaValuesDescriptor(t, expanded["scale"]) == "10" && !athenaValuesJSONBool(t, expanded["caseSensitive"]), "node column descriptors changed")
	}
	prior = used
	snapshot := prior
	frame, next, err = athenaGetManyFrame(ctx, pointWireContractRequest("getMany", ids...), documents, athenaWireBudget{})
	athenaValuesCheck(t, err == errAthenaOutput && frame == nil && next == (athenaWireBudget{}), "17-document zero budget: %v", err)
	frame, next, err = athenaGetManyFrame(ctx, pointWireContractRequest("getMany", ids...), documents, prior)
	athenaValuesCheck(t, err == errAthenaOutput && frame == nil && next == snapshot && prior == snapshot, "17-document node overflow: %v", err)
}
