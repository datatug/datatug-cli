package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The request line of the agent log names the method, the length of the body and the
// path. The query string is where a client can put a source string (an environment, a
// database, a source), and percent-encoding hides its punctuation, not its password,
// so it is never logged. The agent-info poll is not logged at all.
func TestLogRequests_NamesTheMethodAndThePathNeverTheQueryString(t *testing.T) {
	const secret = "s3cr3t-Zk39xq"
	var logged bytes.Buffer
	saved := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(saved) })

	served := 0
	handler := logRequests(func(w http.ResponseWriter, r *http.Request) { served++ })

	handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project=demo&environment=postgres%3A%2F%2Falice%3A"+secret+"%40db.example.com%2Fshop", nil))
	line := logged.String()
	if !strings.Contains(line, "GET 0 /datatug/catalog-tables\n") {
		t.Errorf("the log line %q does not name the method, the length and the path", line)
	}
	for _, leaked := range []string{secret, "alice", "db.example.com", "?", "project="} {
		if strings.Contains(line, leaked) {
			t.Errorf("the log line %q holds %q from the query string", line, leaked)
		}
	}

	logged.Reset()
	handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agent-info?x=1", nil))
	if logged.Len() != 0 {
		t.Errorf("the agent-info poll was logged: %q", logged.String())
	}
	if served != 2 {
		t.Errorf("the wrapped handler ran %d times, want 2", served)
	}
}
