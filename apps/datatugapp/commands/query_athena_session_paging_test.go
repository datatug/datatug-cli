package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type athenaSessionFrame struct {
	Operation string `json:"operation"`
	Format    string `json:"format"`
	Columns   []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"columns"`
	Cells        [][]*string `json:"cells"`
	OrdinalStart int         `json:"ordinalStart"`
	HasMore      bool        `json:"hasMore"`
	Receipt      struct {
		QueryID   string `json:"queryId"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"receipt"`
}

func athenaSessionResultRequest(t *testing.T, body map[string]any) bool {
	t.Helper()
	return athenaSessionResultRequestExpect(t, body, athenaSessionQueryID, 2)
}

func athenaSessionResultRequestExpect(t *testing.T, body map[string]any, expectedQueryID string, expectedPageSize int) bool {
	t.Helper()
	if body["QueryExecutionId"] != expectedQueryID ||
		body["MaxResults"] != json.Number(strconv.Itoa(expectedPageSize)) ||
		body["QueryResultType"] != "DATA_ROWS" {
		t.Error("result request changed query identity, page size, or result type")
		return false
	}
	_, hasToken := body["NextToken"]
	expectedFields := 3
	if hasToken {
		expectedFields = 4
	}
	if len(body) != expectedFields {
		t.Error("result request closed shape changed")
		return false
	}
	if hasToken {
		token, ok := body["NextToken"].(string)
		if !ok || token == "" {
			t.Error("result request invented a token shape")
			return false
		}
	}
	return true
}

func athenaSessionPointer(value string) *string {
	return &value
}

func athenaSessionRow(id string, amount *string) map[string]any {
	cells := []any{map[string]any{"VarCharValue": id}}
	if amount == nil {
		cells = append(cells, map[string]any{})
	} else {
		cells = append(cells, map[string]any{"VarCharValue": *amount})
	}
	return map[string]any{"Data": cells}
}

func athenaSessionColumns() []any {
	return []any{
		map[string]any{"Name": "id", "Label": "id", "Type": "varchar", "Nullable": "UNKNOWN", "CaseSensitive": true},
		map[string]any{"Name": "amount", "Label": "amount", "Type": "decimal(38,10)", "Precision": 38, "Scale": 10, "Nullable": "UNKNOWN"},
	}
}

func athenaSessionFrames(t *testing.T, output *bytes.Buffer) []athenaSessionFrame {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var frames []athenaSessionFrame
	for {
		var frame athenaSessionFrame
		err := decoder.Decode(&frame)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func TestAthenaSessionSameExecutionPagesAndPendingOutput(t *testing.T) {
	cfg := athenaOptionsTestConfig(t)
	cfg.Identity.SessionTokenEnv = "ATHENA_TEST_SESSION"
	t.Setenv("ATHENA_TEST_ACCESS", athenaSessionCredential.AccessKeyID)
	t.Setenv("ATHENA_TEST_SECRET", athenaSessionCredential.SecretAccessKey)
	t.Setenv("ATHENA_TEST_SESSION", athenaSessionCredential.SessionToken)
	wide := strings.Repeat("宽的", 200)
	f := athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, _ *http.Request, event athenaSessionEvent) bool {
		if event.op != "GetQueryResults" {
			return false
		}
		if !athenaSessionResultRequest(t, event.body) {
			w.WriteHeader(400)
			return true
		}
		token, _ := event.body["NextToken"].(string)
		switch token {
		case "":
			athenaSessionWrite(t, w, 200, map[string]any{
				"ResultSet": map[string]any{
					"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
					"Rows": []any{
						athenaSessionRow("id", athenaSessionPointer("amount")),
						athenaSessionRow("a", nil),
					},
				},
				"NextToken": "owned-page-1",
			})
		case "owned-page-1":
			athenaSessionWrite(t, w, 200, map[string]any{"ResultSet": map[string]any{}, "NextToken": "owned-page-2"})
		case "owned-page-2":
			athenaSessionWrite(t, w, 200, map[string]any{
				"ResultSet": map[string]any{"Rows": []any{
					athenaSessionRow("b", athenaSessionPointer("")),
					athenaSessionRow("b", athenaSessionPointer(wide)),
				}},
			})
		default:
			t.Error("unexpected owned result token")
			w.WriteHeader(400)
		}
		return true
	})
	client, closeTransport := athenaSessionClient(t, f, cfg)
	session, err := newAthenaPageSession(context.Background(), client, closeTransport, athenaSessionRequest(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	if err = session.Next(); err != nil {
		t.Fatal(err)
	}
	if f.count("GetQueryResults") != 1 {
		t.Fatal("first page did not issue exactly one result request")
	}
	before := f.count("GetQueryResults")
	if err = session.Next(); err != errAthenaSessionUnsent || f.count("GetQueryResults") != before {
		t.Fatal("Next while pending dispatched or changed its refusal")
	}
	if err = session.WritePending(output); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	cursor := string(session.cursor)
	session.mu.Unlock()
	if cursor == "" {
		t.Fatal("first page did not retain a private successor cursor")
	}
	if err = session.Next(); err != nil {
		t.Fatal(err)
	}
	if f.count("GetQueryResults") != 2 {
		t.Fatal("empty page auto-looped or was not explicit")
	}
	before = f.count("GetQueryResults")
	if err = session.Next(); err != errAthenaSessionUnsent || f.count("GetQueryResults") != before {
		t.Fatal("unsent empty page changed refusal or dispatched")
	}
	if err = session.WritePending(output); err != nil {
		t.Fatal(err)
	}
	if f.count("GetQueryResults") != 2 {
		t.Fatal("writing an empty page dispatched automatically")
	}
	if err = session.Next(); err != nil {
		t.Fatal(err)
	}
	if f.count("GetQueryResults") != 3 {
		t.Fatal("terminal page did not issue exactly one result request")
	}
	before = f.count("GetQueryResults")
	if err = session.Next(); err != errAthenaSessionUnsent || f.count("GetQueryResults") != before {
		t.Fatal("Next while terminal frame pending dispatched or changed its refusal")
	}
	if err = session.Dispose(); err != nil {
		t.Fatal("disposal before terminal flush failed: ", err)
	}
	if err = session.WritePending(output); err != nil {
		t.Fatal("acknowledged EOF frame was not flushed after cleanup: ", err)
	}
	if err = session.Dispose(); err != nil {
		t.Fatal(err)
	}
	frames := athenaSessionFrames(t, output)
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	wantOrdinals := []int{0, 1, 1}
	for i, frame := range frames {
		if frame.Operation != "query" || frame.Format != "athena_string_null_v1" ||
			frame.OrdinalStart != wantOrdinals[i] {
			t.Fatalf("frame %d identity or ordinal changed", i)
		}
	}
	if !frames[0].HasMore || !frames[1].HasMore || frames[2].HasMore {
		t.Fatal("page continuation state changed")
	}
	for i, frame := range frames {
		if len(frame.Columns) != 2 || frame.Columns[0].Name != "id" ||
			frame.Columns[0].Type != "varchar" || frame.Columns[1].Name != "amount" ||
			frame.Columns[1].Type != "decimal(38,10)" {
			t.Fatalf("frame %d did not retain ordered native columns", i)
		}
	}
	var rows [][]*string
	for _, frame := range frames {
		rows = append(rows, frame.Cells...)
	}
	if len(rows) != 3 {
		t.Fatalf("data rows = %d, want 3", len(rows))
	}
	if len(rows[0]) != 2 || rows[0][0] == nil || *rows[0][0] != "a" || rows[0][1] != nil {
		t.Fatal("SQL NULL row changed")
	}
	if len(rows[1]) != 2 || rows[1][0] == nil || *rows[1][0] != "b" ||
		rows[1][1] == nil || *rows[1][1] != "" {
		t.Fatal("empty STRING row changed")
	}
	if len(rows[2]) != 2 || rows[2][0] == nil || *rows[2][0] != "b" ||
		rows[2][1] == nil || *rows[2][1] != wide {
		t.Fatal("wide STRING or duplicate row changed")
	}
	for i, frame := range frames {
		if frame.Receipt.QueryID != athenaSessionQueryID {
			t.Fatalf("frame %d changed original query identity", i)
		}
	}
	if frames[0].Receipt.ExpiresAt == "" || frames[0].Receipt.ExpiresAt != frames[1].Receipt.ExpiresAt ||
		frames[0].Receipt.ExpiresAt != frames[2].Receipt.ExpiresAt {
		t.Fatal("cursor expiry renewed or changed")
	}
	session.mu.Lock()
	receipt := session.receipt
	session.mu.Unlock()
	if receipt.RowsAcknowledged != 3 || !receipt.Complete {
		t.Fatal("terminal acknowledgement or completeness changed")
	}
	if receipt.DataScannedInBytes != "9007199254740993" {
		t.Fatal("original scanned byte text was rounded")
	}
	rendered := output.String()
	if strings.Contains(rendered, `"cursor"`) || strings.Contains(rendered, `"nextToken"`) ||
		(len(cursor) > 8 && strings.Contains(rendered, cursor)) {
		t.Fatal("private cursor or native token was serialized")
	}
	if f.count("StartQueryExecution") != 1 || f.count("GetQueryResults") != 3 ||
		f.count("StopQueryExecution") > 1 || f.count("GetWorkGroup") > 1 ||
		f.count("GetQueryExecution") > 1 {
		t.Fatalf("unexpected RPC counts: start=%d results=%d stop=%d workgroup=%d execution=%d",
			f.count("StartQueryExecution"), f.count("GetQueryResults"), f.count("StopQueryExecution"),
			f.count("GetWorkGroup"), f.count("GetQueryExecution"))
	}
}
