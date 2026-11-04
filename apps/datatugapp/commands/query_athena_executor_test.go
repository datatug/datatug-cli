package commands

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

type athenaExecutorOutputWriter struct {
	t      *testing.T
	buffer bytes.Buffer
	closes func() int
	writes int
}

func (w *athenaExecutorOutputWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes != 1 {
		w.t.Errorf("finishAthenaOutput performed %d writes, want 1", w.writes)
	}
	if got := w.closes(); got != 1 {
		w.t.Errorf("finishAthenaOutput wrote before exactly one transport close: got %d", got)
	}
	return w.buffer.Write(p)
}

func TestAthenaExecutorSameExecutionPagesAfterCleanup(t *testing.T) {
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
	closes := 0
	closed := func() {
		closes++
		closeTransport()
	}
	candidate, err := executeAthenaRead(context.Background(), client, closed, athenaSessionRequest(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate) == 0 {
		t.Fatal("executeAthenaRead did not return a private JSONL candidate")
	}
	if closes != 1 {
		t.Fatalf("transport close callback ran %d times, want 1", closes)
	}
	if f.count("StartQueryExecution") != 1 || f.count("GetQueryResults") != 3 {
		t.Fatalf("unexpected RPC counts: start=%d results=%d",
			f.count("StartQueryExecution"), f.count("GetQueryResults"))
	}
	frames := athenaSessionFrames(t, bytes.NewBuffer(candidate))
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want 3", len(frames))
	}
	wantOrdinals := []int{0, 1, 1}
	wantMore := []bool{true, true, false}
	for i, frame := range frames {
		if frame.Operation != "query" || frame.Format != "athena_string_null_v1" ||
			frame.OrdinalStart != wantOrdinals[i] || frame.HasMore != wantMore[i] {
			t.Fatalf("frame %d identity, ordinal or continuation changed", i)
		}
		if len(frame.Columns) != 2 || frame.Columns[0].Name != "id" ||
			frame.Columns[0].Type != "varchar" || frame.Columns[1].Name != "amount" ||
			frame.Columns[1].Type != "decimal(38,10)" {
			t.Fatalf("frame %d did not retain ordered native columns", i)
		}
		if frame.Receipt.QueryID != athenaSessionQueryID || frame.Receipt.ExpiresAt == "" ||
			frame.Receipt.ExpiresAt != frames[0].Receipt.ExpiresAt {
			t.Fatalf("frame %d changed original query identity or expiry", i)
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
	rendered := string(candidate)
	for _, private := range []string{"cursor", "nextToken", "owned-page-1", "owned-page-2",
		athenaSessionCredential.AccessKeyID, athenaSessionCredential.SecretAccessKey,
		athenaSessionCredential.SessionToken} {
		if strings.Contains(rendered, private) {
			t.Fatalf("candidate leaked private %q", private)
		}
	}
	observer := &athenaExecutorOutputWriter{t: t, closes: func() int { return closes }}
	if err = finishAthenaOutput(context.Background(), observer, candidate); err != nil {
		t.Fatal(err)
	}
	if observer.writes != 1 {
		t.Fatalf("finishAthenaOutput wrote %d times, want 1", observer.writes)
	}
	if !bytes.Equal(observer.buffer.Bytes(), candidate) {
		t.Fatal("finishAthenaOutput re-encoded the candidate bytes")
	}
}
