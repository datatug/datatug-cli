package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	knowledge "github.com/datatug/datatug-core/pkg/detectiveknowledge"
)

func TestDetectiveKnowledgeRoutesRequireCurrentProjectSession(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	contextID := api.SecurityContextID()
	store, _, err := detectiveStore("p", contextID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCase(t.Context(), detective.Case{ID: "case-1", Title: "Invoice issue", Evidence: []detective.EvidenceRef{{ID: "source-note", Kind: "external", Ref: "reviewed sample"}}}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	request := knowledge.Action{Storage: "local", Project: "p", OperationID: "proposal-1", Type: "save-draft", Proposal: &knowledge.Proposal{ID: "invoice-rule", CaseID: "case-1", Kind: "invariant", Title: "Customer exists", Statement: "Invoice customer should exist.", EvidenceRefs: []string{"source-note"}}}
	encoded, _ := json.Marshal(request)
	path := "/datatug/detective/knowledge_proposal_action?project=p&securityContextId=" + contextID
	response := httptest.NewRecorder()
	detectiveKnowledgeActionHandler(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)))
	if response.Code != http.StatusOK {
		t.Fatalf("proposal save = %d %s", response.Code, response.Body.String())
	}
	restarted, err := detective.NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	page, err := restarted.KnowledgePage(t.Context(), "")
	if err != nil || len(page.Proposals) != 1 || page.Proposals[0].ID != "invoice-rule" {
		t.Fatalf("restart page: %+v, %v", page, err)
	}
	get := httptest.NewRecorder()
	detectiveKnowledgeProposalsHandler(get, httptest.NewRequest(http.MethodGet, "/datatug/detective/knowledge_proposals?project=p&securityContextId="+contextID, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("proposal page = %d %s", get.Code, get.Body.String())
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "bob"}}, map[string]string{"p": project}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	denied := httptest.NewRecorder()
	detectiveKnowledgeProposalsHandler(denied, httptest.NewRequest(http.MethodGet, "/datatug/detective/knowledge_proposals?project=p&securityContextId="+api.SecurityContextID(), nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("restricted knowledge read = %d %s", denied.Code, denied.Body.String())
	}
	stale := httptest.NewRecorder()
	detectiveKnowledgeActionHandler(stale, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded)))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale knowledge action = %d %s", stale.Code, stale.Body.String())
	}
}
