package endpoints

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GET queries/get_query answers a failure to walk the queries tree of the project with the sentence
// of queries/all_queries, built from the ID of the project, and never with the text of the
// operating system, which quotes the path of the queries folder.
func TestRoutes_GetQueryAnswersTheSentenceOfAllQueriesWhenTheTreeCannotBeWalked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a folder with no read permission is readable by root")
	}
	withApicoreHandle(t)
	dir, project := serveBlankProject(t)
	locked := filepath.Join(dir, "queries", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	for name, handler := range map[string]http.HandlerFunc{"get_query": getQueryHandler, "all_queries": getQueriesHandler} {
		w := sendQuery(handler, http.MethodGet, "/datatug/queries/"+name, url.Values{"project": {project}, "query": {"q1"}})
		body := w.Body.String()
		const wantStatus = http.StatusInternalServerError
		if want := `queries of project "` + project + `" could not be loaded`; w.Code != wantStatus || answerMessage(body) != want {
			t.Errorf("%s: %d %s, want a %d whose message is exactly %q", name, w.Code, body, wantStatus, want)
		}
		for _, leak := range []string{dir, filepath.Base(dir), "permission denied", "list queries under", "locked"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the answer holds %q: %s", name, leak, body)
			}
		}
	}
}
