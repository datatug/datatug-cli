package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	athenasdk "github.com/dal-go/dalgo2athena"
)

func TestAthenaExecutorLaterPageFailureKeepsWholeOutputPrivate(t *testing.T) {
	cfg := athenaSessionLifecycleConfig(t)
	f := athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, _ *http.Request, event athenaSessionEvent) bool {
		if event.op != "GetQueryResults" {
			return false
		}
		if !athenaSessionResultRequest(t, event.body) {
			w.WriteHeader(400)
			return true
		}
		token, hasToken := event.body["NextToken"].(string)
		switch {
		case !hasToken:
			if len(event.body) != 3 {
				t.Error("initial result request shape changed")
				w.WriteHeader(400)
				return true
			}
			athenaSessionWrite(t, w, 200, map[string]any{
				"ResultSet": map[string]any{
					"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
					"Rows": []any{
						athenaSessionRow("id", athenaSessionPointer("amount")),
						athenaSessionRow("a", athenaSessionPointer("1")),
					},
				},
				"NextToken": "owned-executor-fail",
			})
		case token == "owned-executor-fail":
			if len(event.body) != 4 {
				t.Error("successor result request shape changed")
				w.WriteHeader(400)
				return true
			}
			athenaSessionWrite(t, w, http.StatusForbidden, map[string]any{"message": "PRIVATE_EXECUTOR_BODY"})
		default:
			t.Error("unexpected owned result token")
			w.WriteHeader(400)
		}
		return true
	})
	client, origClose := athenaSessionClient(t, f, cfg)
	closes := 0
	countedClose := func() {
		closes++
		origClose()
	}
	candidate, err := executeAthenaRead(context.Background(), client, countedClose, athenaSessionRequest(t, cfg))
	if candidate != nil {
		t.Fatalf("failed later page returned %d private output bytes", len(candidate))
	}
	if err == nil {
		t.Fatal("failed later page returned no error")
	}
	var sdkErr *athenasdk.Error
	if !errors.As(err, &sdkErr) {
		t.Fatalf("error is not *athenasdk.Error: %v", err)
	}
	if fmt.Sprint(sdkErr.Code) != "remote" || sdkErr.Status != http.StatusForbidden {
		t.Fatalf("remote error = code %v, status %d, want remote and 403", sdkErr.Code, sdkErr.Status)
	}
	safe := err.Error()
	for _, private := range []string{
		"PRIVATE_EXECUTOR_BODY",
		athenaSessionCredential.AccessKeyID,
		athenaSessionCredential.SecretAccessKey,
		athenaSessionCredential.SessionToken,
	} {
		if strings.Contains(safe, private) {
			t.Fatalf("executor error leaked private %q", private)
		}
	}
	if closes != 1 {
		t.Fatalf("transport close callback ran %d times, want 1", closes)
	}
	if f.count("StartQueryExecution") != 1 || f.count("GetQueryExecution") != 1 ||
		f.count("GetQueryResults") != 2 || f.count("StopQueryExecution") != 0 {
		t.Fatalf("unexpected RPC counts: start=%d execution=%d results=%d stop=%d",
			f.count("StartQueryExecution"), f.count("GetQueryExecution"),
			f.count("GetQueryResults"), f.count("StopQueryExecution"))
	}
}

func TestAthenaExecutorOutputWriterSingleAttempt(t *testing.T) {
	frame := []byte(`{"operation":"query","format":"athena_string_null_v1","columns":[],"cells":[],"ordinalStart":0,"hasMore":false}` + "\n")
	for _, test := range []struct {
		name string
		full bool
	}{
		{name: "full", full: true},
		{name: "short"},
		{name: "error"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var calls int
			var captured bytes.Buffer
			cause := errors.New("PRIVATE_WRITER_CAUSE")
			writer := athenaSessionLifecycleWriterFunc(func(p []byte) (int, error) {
				calls++
				switch test.name {
				case "full":
					return captured.Write(p)
				case "short":
					return len(p) - 1, nil
				default:
					return 0, cause
				}
			})
			err := finishAthenaOutput(context.Background(), writer, frame)
			if calls != 1 {
				t.Fatalf("writer calls = %d, want 1", calls)
			}
			if test.full {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(captured.Bytes(), frame) {
					t.Fatal("finishAthenaOutput changed the candidate bytes")
				}
				return
			}
			if !errors.Is(err, errAthenaSessionOutput) {
				t.Fatalf("writer failure = %v, want errAthenaSessionOutput", err)
			}
			if strings.Contains(err.Error(), "PRIVATE_WRITER_CAUSE") {
				t.Fatal("writer failure leaked the raw cause")
			}
		})
	}
}
