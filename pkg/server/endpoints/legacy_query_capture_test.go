package endpoints

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
)

// A create_query body carrying a capture block that claims someone else
// captured the query: what each build does with it is
// assertClientCaptureOutcome, one per build.
func TestLegacyCreateQuery_ClientCaptureBlock(t *testing.T) {
	withApicoreHandle(t)
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	projectDir, ok := api.ProjectDir(scope.Project)
	if !ok {
		t.Fatalf("project %s is not served", scope.Project)
	}
	body := `{"storage":"local","project":"` + scope.Project + `","query":{"id":"forged","folderPath":"~","title":"forged","type":"SQL",` +
		`"capture":{"author":"mallory","environment":"` + semanticTestEnv + `","source":"` + semanticTestSource + `"}}}`
	w := httptest.NewRecorder()
	createQuery(w, httptest.NewRequest(http.MethodPost, "/datatug/queries/create_query?project="+scope.Project, strings.NewReader(body)))
	assertClientCaptureOutcome(t, w, filepath.Join(projectDir, "queries", "forged.query.json"))
}
