package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func athenaValuesString(v string) *string { return &v }
func athenaValuesInt64(v int64) *int64    { return &v }
func athenaValuesBool(v bool) *bool       { return &v }

func athenaValuesNativePage() (athenasdk.NativePage, []athenasdk.Column, [][]*string) {
	columns := []athenasdk.Column{
		{Name: "ID", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"},
		{Name: "DECIMAL_VALUE", Type: "decimal(38,4)", Label: "decimal", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: "tbl", Precision: athenaValuesInt64(-9223372036854775808), Scale: athenaValuesInt64(9223372036854775807)},
		{Name: "ZERO", Type: "decimal", Label: "zero", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: "tbl", Precision: athenaValuesInt64(0), Scale: athenaValuesInt64(0), CaseSensitive: athenaValuesBool(false)},
		{Name: "RANGE", Type: "decimal", Label: "range", Nullable: "NULLABLE", CatalogName: "cat", SchemaName: "sch", TableName: "tbl", Precision: athenaValuesInt64(9223372036854775807), Scale: athenaValuesInt64(-9223372036854775808)},
	}
	cells := [][]*string{
		{nil, athenaValuesString(""), athenaValuesString("null"), athenaValuesString("range-0")},
		{athenaValuesString("9007199254740993.0001"), athenaValuesString("12345678901234567890.000100"), athenaValuesString("-1.25e-10"), athenaValuesString("range-1")},
		{athenaValuesString("2024-02-29T23:59:59.123456Z"), athenaValuesString("AQIDBAU="), athenaValuesString(`{"variant":true,"n":[1,null]}`), athenaValuesString("range-2")},
		{athenaValuesString("TRUE"), athenaValuesString("false"), athenaValuesString("0xDEADBEEF"), athenaValuesString("range-3")},
	}
	expires := time.Date(2026, 11, 2, 10, 30, 15, 123456789, time.FixedZone("offset", 5*3600+30*60))
	page := athenasdk.NativePage{
		Columns: columns, Cells: cells, OrdinalStart: 0, HasMore: true,
		NextCursor: athenasdk.Cursor("cursor-sentinel-athena-7f3c"),
		Receipt: athenasdk.Receipt{
			QueryID: "qid-11", SourceDigest: "source-12", PlanDigest: "plan-13",
			PrincipalDigest: "principal-14", ScopeDigest: "scope-15",
			State: "SUCCEEDED", CancelOutcome: "not-requested", ExpiresAt: expires,
			Requests: 21, PagesFetched: 22, RowsFetched: 23, RowsAcknowledged: 24,
			ResponseBytes: 25, Nodes: 26, Deliveries: 27,
			DataScannedInBytes: "9007199254740993", Complete: false,
		},
	}
	return page, columns, cells
}

func athenaValuesCheck(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}
func athenaValuesObject(t *testing.T, raw json.RawMessage, want ...string) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(raw, &object) == nil && len(object) == len(want), "object %s", raw)
	for _, key := range want {
		_, ok := object[key]
		athenaValuesCheck(t, ok, "missing %q in %s", key, raw)
	}
	return object
}
func athenaValuesText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	var text string
	athenaValuesCheck(t, len(trimmed) > 1 && trimmed[0] == '"' && json.Unmarshal(raw, &text) == nil, "not a JSON string: %s", raw)
	return text
}
func athenaValuesJSONBool(t *testing.T, raw json.RawMessage) bool {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	var value bool
	athenaValuesCheck(t, (string(trimmed) == "true" || string(trimmed) == "false") && json.Unmarshal(raw, &value) == nil, "not a JSON boolean: %s", raw)
	return value
}
func athenaValuesFalse(t *testing.T, raw json.RawMessage) {
	t.Helper()
	athenaValuesCheck(t, string(bytes.TrimSpace(raw)) == "false", "not the JSON false token: %s", raw)
}
func athenaValuesInteger(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	trimmed := bytes.TrimSpace(raw)
	var value int
	athenaValuesCheck(t, len(trimmed) > 0 && (trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9')) && json.Unmarshal(raw, &value) == nil, "not a JSON number: %s", raw)
	return value
}
func athenaValuesDescriptor(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	object := athenaValuesObject(t, raw, "kind", "value")
	athenaValuesCheck(t, athenaValuesText(t, object["kind"]) == "int64", "descriptor kind: %s", raw)
	return athenaValuesText(t, object["value"])
}

func TestAthenaValuesNativeFidelityAndCursorExclusion(t *testing.T) {
	page, columns, cells := athenaValuesNativePage()
	frame, candidate, err := athenaPageFrame(context.Background(), page, athenaWireBudget{})
	athenaValuesCheck(t, err == nil, "frame error: %v", err)
	saved := append([]byte(nil), frame...)
	athenaValuesCheck(t, candidate.valid() && candidate.bytes == len(frame) && candidate.rows == len(cells),
		"zero-usage budget %+v, frame bytes %d, rows %d", candidate, len(frame), len(cells))
	athenaValuesCheck(t, frame[len(frame)-1] == '\n' && bytes.Count(frame, []byte{'\n'}) == 1 && json.Valid(frame[:len(frame)-1]),
		"not one JSON object plus one newline: %q", frame)

	document := athenaValuesObject(t, frame, "operation", "format", "columns", "cells", "ordinalStart", "hasMore", "receipt")
	athenaValuesCheck(t, athenaValuesText(t, document["operation"]) == "query" && athenaValuesText(t, document["format"]) == "athena_string_null_v1",
		"operation or format changed")
	ordinal := athenaValuesInteger(t, document["ordinalStart"])
	hasMore := athenaValuesJSONBool(t, document["hasMore"])
	athenaValuesCheck(t, ordinal == 0 && hasMore, "ordinalStart/hasMore changed")
	athenaValuesCheck(t, !bytes.Contains(frame, []byte("cursor-sentinel-athena-7f3c")), "native cursor leaked into frame")

	var wireColumns []json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(document["columns"], &wireColumns) == nil && len(wireColumns) == 4, "columns changed")
	base := []string{"name", "type", "label", "nullable", "catalogName", "schemaName", "tableName"}
	id := athenaValuesObject(t, wireColumns[0], base...)
	decimal := athenaValuesObject(t, wireColumns[1], append(append([]string(nil), base...), "precision", "scale")...)
	zero := athenaValuesObject(t, wireColumns[2], append(append([]string(nil), base...), "precision", "scale", "caseSensitive")...)
	span := athenaValuesObject(t, wireColumns[3], append(append([]string(nil), base...), "precision", "scale")...)
	wantMetadata := [][7]string{
		{"ID", "varchar", "id", "UNKNOWN", "cat", "sch", "tbl"},
		{"DECIMAL_VALUE", "decimal(38,4)", "decimal", "NULLABLE", "cat", "sch", "tbl"},
		{"ZERO", "decimal", "zero", "NULLABLE", "cat", "sch", "tbl"},
		{"RANGE", "decimal", "range", "NULLABLE", "cat", "sch", "tbl"},
	}
	for i, object := range []map[string]json.RawMessage{id, decimal, zero, span} {
		var got [7]string
		for j, key := range base {
			got[j] = athenaValuesText(t, object[key])
		}
		athenaValuesCheck(t, got == wantMetadata[i], "column %d required metadata changed: %v", i, got)
	}
	athenaValuesCheck(t, athenaValuesText(t, id["nullable"]) == "UNKNOWN" && athenaValuesText(t, decimal["nullable"]) == "NULLABLE" &&
		athenaValuesDescriptor(t, decimal["precision"]) == "-9223372036854775808" &&
		athenaValuesDescriptor(t, decimal["scale"]) == "9223372036854775807" &&
		athenaValuesDescriptor(t, zero["precision"]) == "0" && athenaValuesDescriptor(t, zero["scale"]) == "0" &&
		athenaValuesDescriptor(t, span["precision"]) == "9223372036854775807" &&
		athenaValuesDescriptor(t, span["scale"]) == "-9223372036854775808",
		"column metadata or descriptor fidelity changed")
	athenaValuesFalse(t, zero["caseSensitive"])

	var wireCells []json.RawMessage
	athenaValuesCheck(t, json.Unmarshal(document["cells"], &wireCells) == nil && len(wireCells) == len(cells), "cells changed")
	for i, rawRow := range wireCells {
		var row []json.RawMessage
		athenaValuesCheck(t, json.Unmarshal(rawRow, &row) == nil && len(row) == len(cells[i]), "row %d changed", i)
		for j, rawCell := range row {
			if cells[i][j] == nil {
				athenaValuesCheck(t, string(rawCell) == "null", "SQL NULL cell %d,%d became %s", i, j, rawCell)
			} else {
				athenaValuesCheck(t, athenaValuesText(t, rawCell) == *cells[i][j], "cell %d,%d changed", i, j)
			}
		}
	}

	receipt := athenaValuesObject(t, document["receipt"], "queryId", "sourceDigest", "planDigest", "principalDigest", "scopeDigest", "state", "cancelOutcome", "expiresAt", "requests", "pagesFetched", "rowsFetched", "rowsAcknowledged", "responseBytes", "nodes", "deliveries", "dataScannedInBytes", "complete")
	for key, want := range map[string]string{
		"queryId": "qid-11", "sourceDigest": "source-12", "planDigest": "plan-13",
		"principalDigest": "principal-14", "scopeDigest": "scope-15", "state": "SUCCEEDED",
		"cancelOutcome": "not-requested", "expiresAt": "2026-11-02T05:00:15.123456789Z",
		"dataScannedInBytes": "9007199254740993",
	} {
		athenaValuesCheck(t, athenaValuesText(t, receipt[key]) == want, "receipt %s changed", key)
	}
	for key, want := range map[string]int{"requests": 21, "pagesFetched": 22, "rowsFetched": 23, "rowsAcknowledged": 24, "responseBytes": 25, "nodes": 26, "deliveries": 27} {
		athenaValuesCheck(t, athenaValuesInteger(t, receipt[key]) == want, "receipt %s changed", key)
	}
	athenaValuesFalse(t, receipt["complete"])

	columns[0].Name = "MUTATED"
	*columns[1].Precision, *columns[1].Scale, *cells[1][0] = 1, 2, "MUTATED"
	cells[2] = nil
	page.Columns, page.Cells = nil, nil
	page.NextCursor = "MUTATED"
	athenaValuesCheck(t, bytes.Equal(frame, saved), "returned frame changed after mutating original pointers, columns or rows")

	column := athenasdk.Column{Name: "ID", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"}
	for _, ordinal := range []int{0, 1000} {
		empty := athenasdk.NativePage{Columns: []athenasdk.Column{column}, Cells: nil, OrdinalStart: ordinal, HasMore: true}
		frame, _, err := athenaPageFrame(context.Background(), empty, athenaWireBudget{})
		athenaValuesCheck(t, err == nil, "ordinal %d rejected: %v", ordinal, err)
		document := athenaValuesObject(t, frame, "operation", "format", "columns", "cells", "ordinalStart", "hasMore", "receipt")
		athenaValuesCheck(t, string(document["cells"]) == "[]" &&
			athenaValuesInteger(t, document["ordinalStart"]) == ordinal &&
			athenaValuesJSONBool(t, document["hasMore"]), "empty page at ordinal %d changed", ordinal)
	}
}

func TestAthenaValuesExactEscapingAndCumulativeBudget(t *testing.T) {
	ctx := context.Background()
	plainColumn := athenasdk.Column{Name: "ID", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"}
	plainReceipt := athenasdk.Receipt{QueryID: "q", State: "SUCCEEDED", ExpiresAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)}
	plainPage := athenasdk.NativePage{Columns: []athenasdk.Column{plainColumn}, OrdinalStart: 0, HasMore: true, Receipt: plainReceipt}

	t.Run("escaping", func(t *testing.T) {
		metadata := "q\"\\\b\f\n\r\t\x01<>&\u2028\u2029é🚀"
		content := "nul\x00\x01\x1f" + metadata + " tail"
		column := athenasdk.Column{Name: "ID", Type: "varchar", Label: metadata, Nullable: "UNKNOWN", CatalogName: metadata, SchemaName: metadata, TableName: metadata}
		page := athenasdk.NativePage{Columns: []athenasdk.Column{column}, Cells: [][]*string{{athenaValuesString(content)}}, OrdinalStart: 0, HasMore: true,
			Receipt: athenasdk.Receipt{QueryID: content, SourceDigest: metadata, PlanDigest: "p", PrincipalDigest: "pr", ScopeDigest: "sc", State: "SUCCEEDED", CancelOutcome: "c", ExpiresAt: plainReceipt.ExpiresAt, DataScannedInBytes: content}}
		frame, candidate, err := athenaPageFrame(ctx, page, athenaWireBudget{})
		athenaValuesCheck(t, err == nil && frame != nil && candidate.bytes == len(frame) && candidate.bytes > 0 && frame[len(frame)-1] == '\n', "escaping frame: %v", err)
		for _, value := range []string{metadata, content} {
			literal, marshalErr := json.Marshal(value)
			athenaValuesCheck(t, marshalErr == nil && bytes.Contains(frame, literal), "standard escaped literal missing for %q", value)
		}
		document := athenaValuesObject(t, frame, "operation", "format", "columns", "cells", "ordinalStart", "hasMore", "receipt")
		var wireColumns []json.RawMessage
		athenaValuesCheck(t, json.Unmarshal(document["columns"], &wireColumns) == nil && len(wireColumns) == 1, "escaping columns changed")
		wireColumn := athenaValuesObject(t, wireColumns[0], "name", "type", "label", "nullable", "catalogName", "schemaName", "tableName")
		for _, key := range []string{"label", "catalogName", "schemaName", "tableName"} {
			athenaValuesCheck(t, athenaValuesText(t, wireColumn[key]) == metadata, "escaped metadata %s changed", key)
		}
		var wireCells [][]json.RawMessage
		athenaValuesCheck(t, json.Unmarshal(document["cells"], &wireCells) == nil && len(wireCells) == 1 && len(wireCells[0]) == 1 &&
			athenaValuesText(t, wireCells[0][0]) == content, "escaped cell changed")
		receipt := athenaValuesObject(t, document["receipt"], "queryId", "sourceDigest", "planDigest", "principalDigest", "scopeDigest", "state", "cancelOutcome", "expiresAt", "requests", "pagesFetched", "rowsFetched", "rowsAcknowledged", "responseBytes", "nodes", "deliveries", "dataScannedInBytes", "complete")
		athenaValuesCheck(t, athenaValuesText(t, receipt["queryId"]) == content && athenaValuesText(t, receipt["sourceDigest"]) == metadata &&
			athenaValuesText(t, receipt["dataScannedInBytes"]) == content, "escaped receipt changed")
		over := athenaWireBudget{bytes: athenaMaxWireBytes - len(frame) + 1}
		snapshot := over
		refused, next, err := athenaPageFrame(ctx, page, over)
		athenaValuesCheck(t, err == errAthenaOutput && refused == nil && next == snapshot && over == snapshot, "escaped-content overflow: %v", err)
	})

	t.Run("cumulative", func(t *testing.T) {
		page, _, _ := athenaValuesNativePage()
		second := page
		second.Cells, second.OrdinalStart, second.HasMore = page.Cells[:2], 1000, false
		start := athenaWireBudget{}
		first, afterFirst, err := athenaPageFrame(ctx, page, start)
		athenaValuesCheck(t, err == nil && start == (athenaWireBudget{}) && afterFirst.bytes == len(first), "first page: %v", err)
		afterFirstSnapshot := afterFirst
		last, afterLast, err := athenaPageFrame(ctx, second, afterFirst)
		athenaValuesCheck(t, err == nil && last != nil && afterFirst == afterFirstSnapshot, "second page: %v", err)
		athenaValuesCheck(t, afterLast.bytes == start.bytes+len(first)+len(last) && afterLast.rows == len(page.Cells)+len(second.Cells) &&
			afterLast.bytes-afterFirst.bytes == len(last), "cumulative budget %+v", afterLast)
	})

	t.Run("byteEnvelope", func(t *testing.T) {
		baseline, _, err := athenaPageFrame(ctx, plainPage, athenaWireBudget{})
		athenaValuesCheck(t, err == nil && baseline != nil, "baseline: %v", err)
		atMax := athenaWireBudget{bytes: athenaMaxWireBytes - len(baseline)}
		frame, candidate, err := athenaPageFrame(ctx, plainPage, atMax)
		athenaValuesCheck(t, err == nil && len(frame) == len(baseline) && candidate.bytes == athenaMaxWireBytes &&
			candidate.bytes-atMax.bytes == len(frame), "exact 8 MiB envelope: %v", err)
		over := atMax
		over.bytes++
		snapshot := over
		refused, next, err := athenaPageFrame(ctx, plainPage, over)
		athenaValuesCheck(t, err == errAthenaOutput && refused == nil && next == snapshot && over == snapshot, "envelope overflow: %v", err)
	})

	t.Run("nodeBoundary", func(t *testing.T) {
		// Independent shape audit: root object 8 + root texts 2 + columns array 1 +
		// column object 8 + seven column texts 7 + cells array 1 + ordinal 1 +
		// hasMore 1 + receipt object 18 + eight receipt texts 8 + seven receipt
		// integers 7 + dataScanned text 1 + complete boolean 1 = 64 logical nodes.
		receipt := athenasdk.Receipt{QueryID: "q", SourceDigest: "s", PlanDigest: "p", PrincipalDigest: "pr", ScopeDigest: "sc", State: "SUCCEEDED", CancelOutcome: "c", ExpiresAt: plainReceipt.ExpiresAt, Requests: 1, PagesFetched: 2, RowsFetched: 3, RowsAcknowledged: 4, ResponseBytes: 5, Nodes: 6, Deliveries: 7, DataScannedInBytes: "8", Complete: true}
		page := athenasdk.NativePage{Columns: []athenasdk.Column{plainColumn}, OrdinalStart: 0, HasMore: true, Receipt: receipt}
		atMax := athenaWireBudget{nodes: athenaMaxWireNodes - 64}
		frame, candidate, err := athenaPageFrame(ctx, page, atMax)
		athenaValuesCheck(t, err == nil && frame != nil && candidate.nodes == athenaMaxWireNodes && candidate.nodes-atMax.nodes == 64 &&
			candidate.bytes == len(frame), "exact node budget: %v", err)
		over := atMax
		over.nodes++
		snapshot := over
		refused, next, err := athenaPageFrame(ctx, page, over)
		athenaValuesCheck(t, err == errAthenaOutput && refused == nil && next == snapshot && over == snapshot, "node overflow: %v", err)
	})

	t.Run("rowQuota", func(t *testing.T) {
		rows := make([][]*string, athenaMaxWireRows)
		for i := range rows {
			rows[i] = []*string{nil}
		}
		page := athenasdk.NativePage{Columns: []athenasdk.Column{plainColumn}, Cells: rows, OrdinalStart: 0, HasMore: true, Receipt: plainReceipt}
		frame, candidate, err := athenaPageFrame(ctx, page, athenaWireBudget{})
		athenaValuesCheck(t, err == nil && frame != nil && candidate.rows == athenaMaxWireRows && candidate.bytes == len(frame), "1000 null rows: %v", err)
		page.Cells = [][]*string{{nil}, {nil}}
		atMax := athenaWireBudget{rows: athenaMaxWireRows - 1}
		snapshot := atMax
		refused, next, err := athenaPageFrame(ctx, page, atMax)
		athenaValuesCheck(t, err == errAthenaOutput && refused == nil && next == snapshot && atMax == snapshot, "row overflow: %v", err)
	})
}

func TestAthenaValuesMalformedShapesAndCallerPriority(t *testing.T) {
	ctx := context.Background()
	invalidUTF8 := string([]byte{0xff})
	column := athenasdk.Column{Name: "ID", Type: "varchar", Label: "id", Nullable: "UNKNOWN", CatalogName: "cat", SchemaName: "sch", TableName: "tbl"}
	receipt := athenasdk.Receipt{QueryID: "q", SourceDigest: "s", PlanDigest: "p", PrincipalDigest: "pr", ScopeDigest: "sc", State: "SUCCEEDED", CancelOutcome: "c", ExpiresAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), DataScannedInBytes: "8"}
	base := func() athenasdk.NativePage {
		return athenasdk.NativePage{Columns: []athenasdk.Column{column}, Cells: [][]*string{{athenaValuesString("ok")}}, OrdinalStart: 0, HasMore: true, Receipt: receipt}
	}
	used := athenaWireBudget{bytes: 7, nodes: 9, rows: 2}
	athenaValuesCheck(t, used.valid(), "prior budget fixture invalid: %+v", used)
	expectRefusal := func(t *testing.T, page athenasdk.NativePage, prior athenaWireBudget) {
		t.Helper()
		frame, next, err := athenaPageFrame(ctx, page, prior)
		athenaValuesCheck(t, err == errAthenaOutput && frame == nil && next == prior,
			"not a fixed refusal: err=%v frameIsNil=%t next=%+v", err, frame == nil, next)
	}

	t.Run("positiveWitnesses", func(t *testing.T) {
		for _, ordinal := range []int{0, athenaMaxWireRows} {
			page := base()
			page.OrdinalStart = ordinal
			frame, next, err := athenaPageFrame(ctx, page, athenaWireBudget{})
			athenaValuesCheck(t, err == nil && frame != nil && next.bytes == len(frame) && next.rows == 1,
				"positive ordinal %d witness: err=%v next=%+v", ordinal, err, next)
		}
		many := base()
		many.Columns = make([]athenasdk.Column, 128)
		for i := range many.Columns {
			many.Columns[i] = column
		}
		many.Cells = nil
		frame, next, err := athenaPageFrame(ctx, many, athenaWireBudget{})
		athenaValuesCheck(t, err == nil && frame != nil && next.rows == 0,
			"positive 128-column witness: err=%v next=%+v", err, next)
	})

	shapes := []struct {
		name string
		page func() athenasdk.NativePage
	}{
		{"zeroColumns", func() athenasdk.NativePage {
			page := base()
			page.Columns, page.Cells = nil, nil
			return page
		}},
		{"129Columns", func() athenasdk.NativePage {
			page := base()
			page.Columns = make([]athenasdk.Column, 129)
			for i := range page.Columns {
				page.Columns[i] = column
			}
			page.Cells = nil
			return page
		}},
		{"ordinalMinusOne", func() athenasdk.NativePage {
			page := base()
			page.OrdinalStart = -1
			return page
		}},
		{"ordinalOverLimit", func() athenasdk.NativePage {
			page := base()
			page.OrdinalStart = athenaMaxWireRows + 1
			return page
		}},
		{"inconsistentRowWidth", func() athenasdk.NativePage {
			page := base()
			page.Cells = [][]*string{{athenaValuesString("a"), athenaValuesString("b")}}
			return page
		}},
		{"invalidUTF8Cell", func() athenasdk.NativePage {
			page := base()
			page.Cells = [][]*string{{&invalidUTF8}}
			return page
		}},
		{"invalidUTF8Metadata", func() athenasdk.NativePage {
			page := base()
			mutated := column
			mutated.Label = invalidUTF8
			page.Columns = []athenasdk.Column{mutated}
			return page
		}},
		{"invalidUTF8Receipt", func() athenasdk.NativePage {
			page := base()
			mutated := receipt
			mutated.QueryID = invalidUTF8
			page.Receipt = mutated
			return page
		}},
		{"emptyName", func() athenasdk.NativePage {
			page := base()
			mutated := column
			mutated.Name = ""
			page.Columns = []athenasdk.Column{mutated}
			return page
		}},
		{"emptyType", func() athenasdk.NativePage {
			page := base()
			mutated := column
			mutated.Type = ""
			page.Columns = []athenasdk.Column{mutated}
			return page
		}},
		{"nulName", func() athenasdk.NativePage {
			page := base()
			mutated := column
			mutated.Name = "A\x00B"
			page.Columns = []athenasdk.Column{mutated}
			return page
		}},
		{"nulType", func() athenasdk.NativePage {
			page := base()
			mutated := column
			mutated.Type = "varchar\x00"
			page.Columns = []athenasdk.Column{mutated}
			return page
		}},
		{"textAbove4MiB", func() athenasdk.NativePage {
			page := base()
			oversize := strings.Repeat("x", athenaMaxWireText+1)
			page.Cells = [][]*string{{&oversize}}
			return page
		}},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			expectRefusal(t, shape.page(), used)
		})
	}

	negativeCounters := []struct {
		name   string
		mutate func(*athenasdk.Receipt)
	}{
		{"requests", func(r *athenasdk.Receipt) { r.Requests = -1 }},
		{"pagesFetched", func(r *athenasdk.Receipt) { r.PagesFetched = -1 }},
		{"rowsFetched", func(r *athenasdk.Receipt) { r.RowsFetched = -1 }},
		{"rowsAcknowledged", func(r *athenasdk.Receipt) { r.RowsAcknowledged = -1 }},
		{"responseBytes", func(r *athenasdk.Receipt) { r.ResponseBytes = -1 }},
		{"nodes", func(r *athenasdk.Receipt) { r.Nodes = -1 }},
		{"deliveries", func(r *athenasdk.Receipt) { r.Deliveries = -1 }},
	}
	for _, counter := range negativeCounters {
		t.Run("negativeCounter/"+counter.name, func(t *testing.T) {
			page := base()
			counter.mutate(&page.Receipt)
			expectRefusal(t, page, used)
		})
	}
	t.Run("counterAboveSafeInteger", func(t *testing.T) {
		page := base()
		page.Receipt.Requests = int(athenaMaxWireCounter) + 1
		expectRefusal(t, page, used)
	})

	priorBudgets := []struct {
		name  string
		prior athenaWireBudget
	}{
		{"negativeBytes", athenaWireBudget{bytes: -1, nodes: 1, rows: 1}},
		{"overBytes", athenaWireBudget{bytes: athenaMaxWireBytes + 1, nodes: 1, rows: 1}},
		{"negativeNodes", athenaWireBudget{bytes: 1, nodes: -1, rows: 1}},
		{"overNodes", athenaWireBudget{bytes: 1, nodes: athenaMaxWireNodes + 1, rows: 1}},
		{"negativeRows", athenaWireBudget{bytes: 1, nodes: 1, rows: -1}},
		{"overRows", athenaWireBudget{bytes: 1, nodes: 1, rows: athenaMaxWireRows + 1}},
	}
	for _, prior := range priorBudgets {
		t.Run("priorBudget/"+prior.name, func(t *testing.T) {
			expectRefusal(t, base(), prior.prior)
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	malformed := func() athenasdk.NativePage {
		page := base()
		page.Columns, page.Cells = nil, nil
		return page
	}
	contextCases := []struct {
		name    string
		ctx     context.Context
		page    athenasdk.NativePage
		wantErr error
	}{
		{"preCanceledValid", canceled, base(), context.Canceled},
		{"expiredValid", expired, base(), context.DeadlineExceeded},
		{"preCanceledMalformed", canceled, malformed(), context.Canceled},
		{"expiredMalformed", expired, malformed(), context.DeadlineExceeded},
	}
	for _, testCase := range contextCases {
		t.Run(testCase.name, func(t *testing.T) {
			frame, next, err := athenaPageFrame(testCase.ctx, testCase.page, used)
			athenaValuesCheck(t, err == testCase.wantErr && frame == nil && next == used,
				"context did not take priority: err=%v frameIsNil=%t next=%+v", err, frame == nil, next)
		})
	}
	t.Run("nilContext", func(t *testing.T) {
		var noContext context.Context
		frame, next, err := athenaPageFrame(noContext, base(), used)
		athenaValuesCheck(t, err == errAthenaOutput && frame == nil && next == used,
			"nil context not refused: err=%v frameIsNil=%t next=%+v", err, frame == nil, next)
	})
}
