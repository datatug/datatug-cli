package endpoints

import (
	"encoding/json"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestDetectiveVisibilityRetainsArchivedDefinitionsForSelectedDetails(t *testing.T) {
	root := t.TempDir()
	session, err := secureread.NewSession(secureread.SessionOptions{As: "owner", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{"p": root}, api.Capabilities{EvidencePrivateDir: t.TempDir()})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	metadata := []byte(`{"version":1,"archived":[{"kind":"check","id":"kept-check"},{"kind":"case","id":"kept-case"}]}`)
	if err = os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "checks"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "checks", "kept-check.json"), []byte(`{"id":"kept-check","title":"Kept check","version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", map[string]string{"p": root}) }
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	request := httptest.NewRequest(http.MethodGet, "/datatug/detective/state?project=p&securityContextId="+url.QueryEscape(api.SecurityContextID()), nil)
	recorder := httptest.NewRecorder()
	detectiveStateHandler(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"kept-check"`) || !strings.Contains(recorder.Body.String(), `"archived":true`) {
		t.Fatalf("state lost selected archived check: %d %s", recorder.Code, recorder.Body.String())
	}
	state := detective.State{Checks: []detective.Check{{ID: "kept-check", Title: "Kept check", Version: 1}}, Cases: []detective.Case{{ID: "kept-case", Title: "Kept case", Version: 1}}}
	response, err := annotateDetectiveVisibility("p", state)
	if err != nil {
		t.Fatal(err)
	}
	for _, collection := range []string{"checks", "cases"} {
		items := response[collection].([]any)
		if len(items) != 1 || items[0].(map[string]any)["archived"] != true {
			t.Fatalf("selected definition lost: %+v", response)
		}
	}
	before, _ := json.Marshal(state)
	if string(before) == "" || state.Checks[0].ID != "kept-check" {
		t.Fatal("projection mutated owning definitions")
	}
}
