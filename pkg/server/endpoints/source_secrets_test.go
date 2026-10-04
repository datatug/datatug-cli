package endpoints

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

const endpointSecret = "s3cr3t-DT01"

func captureAgentLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(saved) })
	return &buf
}

// The agent log carries an unclassified error's own text; that text must not
// carry a source password even when a driver quotes the URL it dialled.
func TestUnclassifiedErrorLogsRedactSourceURLs(t *testing.T) {
	failure := errors.New(`dial "postgres://alice:` + endpointSecret + `@db.example.com/shop": refused; password=` + endpointSecret)
	for name, write := range map[string]func(http.ResponseWriter, *http.Request, error){
		"contract": writeContractError,
		"compare":  writeCompareError,
	} {
		t.Run(name, func(t *testing.T) {
			logged := captureAgentLog(t)
			w := httptest.NewRecorder()
			write(w, httptest.NewRequest(http.MethodPost, "/datatug/x", nil), failure)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(logged.String(), endpointSecret) || strings.Contains(w.Body.String(), endpointSecret) {
				t.Fatalf("the password leaked.\nlog: %s\nbody: %s", logged.String(), w.Body.String())
			}
			if !strings.Contains(logged.String(), "postgres://alice:xxxxx@db.example.com/shop") {
				t.Errorf("the agent log should still name the redacted source: %q", logged.String())
			}
		})
	}
}

func TestResolveSQLSourceURLNeverEchoesAPassword(t *testing.T) {
	_, err := resolveSQLSourceURL(context.Background(), "postgres://alice:"+endpointSecret+"@127.0.0.1:1/shop", "customers")
	if err == nil {
		t.Fatal("postgres is not wired yet; the open must fail")
	}
	if strings.Contains(err.Error(), endpointSecret) {
		t.Fatalf("error leaks the password: %v", err)
	}
	if !strings.Contains(err.Error(), "postgres://alice:xxxxx@127.0.0.1:1/shop") || !errors.Is(err, dbcopy.ErrPostgresNotWired) {
		t.Fatalf("error = %v, want the redacted source and the wrapped cause", err)
	}
}
