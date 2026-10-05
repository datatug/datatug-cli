package endpoints

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
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
	// Open's error says which source failed and why, in a fixed sentence built from
	// no part of the URL, and is returned as it is: a PostgreSQL source that is not
	// wired is answered by the sentence about PostgreSQL, which names nothing typed.
	if err.Error() != dbcopy.ErrPostgresNotWired.Error() || !errors.Is(err, dbcopy.ErrPostgresNotWired) {
		t.Fatalf("error = %v, want Open's own error, returned as it is", err)
	}
	for _, shown := range []string{"alice", "127.0.0.1", "shop"} {
		if strings.Contains(err.Error(), shown) {
			t.Fatalf("error shows %q of the URL: %v", shown, err)
		}
	}
}

// quotingFailure is what a driver's open error looks like when it formats the
// whole connection URL into its message, as dalgo2postgres does.
func quotingFailure() error {
	return errors.New(`failed to connect to "postgres://alice:` + endpointSecret + `@db.example.com:5432/shop?sslmode=disable": dial tcp: refused`)
}

// captureStderr runs fn with os.Stderr redirected into a buffer.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = saved }()
	fn()
	_ = writer.Close()
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A legacy route returns the error text to the client and prints it to stderr:
// neither may carry a source password.
func TestHandleErrorRedactsBodyAndStderr(t *testing.T) {
	logged := captureAgentLog(t)
	w := httptest.NewRecorder()
	stderr := captureStderr(t, func() {
		if !handleError(quotingFailure(), w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select", nil)) {
			t.Fatal("handleError must report the error")
		}
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	for name, got := range map[string]string{"stderr": stderr, "body": w.Body.String(), "log": logged.String()} {
		if strings.Contains(got, endpointSecret) {
			t.Errorf("%s leaks the password: %s", name, got)
		}
	}
	if !strings.Contains(w.Body.String(), "postgres://alice:xxxxx@db.example.com:5432/shop") {
		t.Errorf("the body should still name the redacted source: %s", w.Body.String())
	}
}

// exec/run_query turns an executor error into INVALID_REQUEST with the error's
// text; every contract error is redacted where it becomes a response.
func TestContractErrorsRedactTheirMessageEverywhere(t *testing.T) {
	for name, build := range map[string]func(string) *contractError{
		"invalid request":    func(m string) *contractError { return newInvalidRequest("", m) },
		"source unavailable": newSourceUnavailable,
		"internal":           func(m string) *contractError { return newContractError(codeInternal, m, "") },
	} {
		t.Run(name, func(t *testing.T) {
			ce := build(quotingFailure().Error())
			if strings.Contains(ce.Error(), endpointSecret) {
				t.Errorf("Error() leaks the password: %s", ce.Error())
			}
			w := httptest.NewRecorder()
			writeContractResponse(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil), ce, nil)
			if strings.Contains(w.Body.String(), endpointSecret) {
				t.Errorf("body leaks the password: %s", w.Body.String())
			}
			compare := httptest.NewRecorder()
			writeCompareError(compare, httptest.NewRequest(http.MethodPost, "/datatug/compare", nil), ce)
			if strings.Contains(compare.Body.String(), endpointSecret) {
				t.Errorf("compare body leaks the password: %s", compare.Body.String())
			}
		})
	}
	harmless := newInvalidRequest("project", `parameter "password" is required; see http://localhost:8080/users/@me`)
	if !strings.Contains(harmless.envelope().Error.Message, `parameter "password" is required; see http://localhost:8080/users/@me`) {
		t.Errorf("harmless text must pass unchanged: %q", harmless.envelope().Error.Message)
	}
}

func TestChatErrorsAndLegacyWriteLogsRedact(t *testing.T) {
	w := httptest.NewRecorder()
	writeChatError(w, http.StatusBadGateway, quotingFailure().Error())
	if strings.Contains(w.Body.String(), endpointSecret) {
		t.Errorf("chat error body leaks the password: %s", w.Body.String())
	}

	logged := captureAgentLog(t)
	recorder := httptest.NewRecorder()
	lrw := newLegacyQueryWriteResponse(recorder, "/datatug/queries/save", "save", "saved")
	_ = lrw.observe(quotingFailure())
	lrw.WriteHeader(http.StatusInternalServerError)
	_, _ = lrw.Write([]byte(quotingFailure().Error()))
	if strings.Contains(logged.String(), endpointSecret) || strings.Contains(recorder.Body.String(), endpointSecret) {
		t.Errorf("the password leaked.\nlog: %s\nbody: %s", logged.String(), recorder.Body.String())
	}

	logged = captureAgentLog(t)
	_ = captureInternal(quotingFailure())
	_ = captureAccessDenied(quotingFailure())
	if strings.Contains(logged.String(), endpointSecret) {
		t.Errorf("capture log leaks the password: %s", logged.String())
	}
}
