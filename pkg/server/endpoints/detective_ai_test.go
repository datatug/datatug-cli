package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	coreDetective "github.com/datatug/datatug-core/pkg/detective"
	aiCore "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/strongo/aichat/ai"
)

func TestLocalAIConfirmedQueryUsesRevisionedAdapterAndExactReplay(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	private := t.TempDir()
	store, err := detective.NewLocalStore(scope.Project, projectDir, private)
	if err != nil {
		t.Fatal(err)
	}
	aiScope := aiCore.Scope{Key: "local:" + scope.Project, Project: scope.Project, Actor: "admin"}
	local := detective.LocalAIStore{Store: store, Scope: aiScope}
	query := aiCore.QueryDraft{ID: "ai-customer", Title: "Customer audit", Type: "DTQL", Text: "SELECT CustomerId FROM Customer"}
	proposal := aiCore.Proposal{ID: "reviewed-query", Scope: aiScope, Draft: aiCore.Draft{Kind: aiCore.KindQuery, Query: &query}}
	command := aiCore.Command{Scope: aiScope, OperationID: "ai-query-confirm", Proposal: proposal, Binding: coreDetective.OperationBinding{Project: aiScope.Key, Actor: aiScope.Actor, Action: "ai-confirm-draft-query", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	receipt, err := local.ConfirmQuery(context.Background(), command)
	if err != nil || receipt.ResourceID != query.ID {
		t.Fatalf("confirmed query = %+v, %v", receipt, err)
	}
	body, err := os.ReadFile(filepath.Join(projectDir, "queries", query.ID+".query.dtql"))
	if err != nil || string(body) != query.Text {
		t.Fatalf("persisted query = %q, %v", body, err)
	}
	restarted, err := detective.NewLocalStore(scope.Project, projectDir, private)
	if err != nil {
		t.Fatal(err)
	}
	local.Store = restarted
	if replay, found, err := local.AIReceipt(context.Background(), command.OperationID); err != nil || !found || replay != receipt {
		t.Fatalf("restart receipt = %+v found=%v err=%v", replay, found, err)
	}
	if replay, err := local.ConfirmQuery(context.Background(), command); err != nil || replay != receipt {
		t.Fatalf("exact query replay = %+v, %v", replay, err)
	}
	command.Binding.PayloadDigest = strings.Repeat("b", 64)
	if _, err := local.ConfirmQuery(context.Background(), command); err == nil {
		t.Fatal("changed query intent reused operation ID")
	}
}

type detectiveAIEmptyProvider struct{ calls int }

func (*detectiveAIEmptyProvider) Name() string { return "test" }
func (p *detectiveAIEmptyProvider) Stream(context.Context, ai.ChatRequest) iter.Seq2[ai.Event, error] {
	p.calls++
	return func(yield func(ai.Event, error) bool) {
		yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd}, nil)
	}
}

type detectiveAIContextProvider struct{ calls int }

func (*detectiveAIContextProvider) Name() string { return "test-context" }
func (p *detectiveAIContextProvider) Stream(_ context.Context, request ai.ChatRequest) iter.Seq2[ai.Event, error] {
	p.calls++
	return func(yield func(ai.Event, error) bool) {
		if p.calls == 1 {
			var supplied struct {
				Sources []aiCore.Source `json:"sources"`
			}
			if len(request.Context) == 0 || json.Unmarshal([]byte(request.Context[0].Text), &supplied) != nil || len(supplied.Sources) != 1 {
				yield(ai.Event{Type: ai.EventError}, aiCore.ErrInvalid)
				return
			}
			citation := aiCore.Provenance{SourceID: supplied.Sources[0].ID, Start: 0, End: 9, Quote: "invoice 7", Confidence: .9}
			value := investigation.NewIntegerValue("7")
			draft := aiCore.Draft{Kind: aiCore.KindContext, Title: "Invoice 7", Context: map[string]aiCore.ExtractedValue{"invoice_id": {Value: detective.InputValue{Scalar: &value}, Provenance: []aiCore.Provenance{citation}}}, Sources: []aiCore.Provenance{citation}}
			arguments, _ := json.Marshal(struct {
				Proposal aiCore.Draft `json:"proposal"`
			}{Proposal: draft})
			if !yield(ai.Event{Type: ai.EventToolCall, ToolCall: &ai.ToolCall{ID: "tool-1", Name: "submit_proposal", Arguments: arguments}}, nil) {
				return
			}
			yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonToolCalls}, nil)
			return
		}
		yield(ai.Event{Type: ai.EventCompleted, StopReason: ai.StopReasonEnd}, nil)
	}
}

func TestDetectiveAIGenerationBindsActorPayloadAndNoModelReplay(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	paths := map[string]string{"p": project}
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	profile := dtconfig.AIProfile{Model: "gpt-4o"}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	provider := new(detectiveAIEmptyProvider)
	previousProvider := newDetectiveAIProvider
	newDetectiveAIProvider = func(string, string, string) (ai.LLMProvider, error) { return provider, nil }
	t.Cleanup(func() { newDetectiveAIProvider = previousProvider })
	contextID := api.SecurityContextID()
	path := "/datatug/detective/ai_proposals?project=p&securityContextId=" + contextID
	post := func(text string, counts map[string]int) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(aiGenerateRequest{Project: "p", SecurityContextID: contextID, OperationID: "operation-1", Text: text, ExpectedCounts: counts})
		response := httptest.NewRecorder()
		detectiveAIProposalsHandler(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		return response
	}
	if response := post("invoice 7", map[string]int{"invoice_ids": 101}); response.Code != http.StatusBadRequest || provider.calls != 0 {
		t.Fatalf("invalid expected count dispatched model: %d %s", response.Code, response.Body.String())
	}
	if response := post("invoice 7", nil); response.Code != http.StatusOK || provider.calls != 1 {
		t.Fatalf("generation = %d %s; calls=%d", response.Code, response.Body.String(), provider.calls)
	}
	if response := post("invoice 7", nil); response.Code != http.StatusOK || provider.calls != 1 {
		t.Fatalf("exact replay = %d %s; calls=%d", response.Code, response.Body.String(), provider.calls)
	}
	if response := post("invoice 8", nil); response.Code != http.StatusConflict || provider.calls != 1 {
		t.Fatalf("changed-payload replay = %d %s; calls=%d", response.Code, response.Body.String(), provider.calls)
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "bob"}}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})
	read := httptest.NewRecorder()
	detectiveAIProposalsHandler(read, httptest.NewRequest(http.MethodGet, "/datatug/detective/ai_proposals?project=p&securityContextId="+api.SecurityContextID(), nil))
	if read.Code != http.StatusForbidden {
		t.Fatalf("restricted actor read = %d %s", read.Code, read.Body.String())
	}
}

func TestDetectiveAIContextReviewConfirmAndReplay(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	paths := map[string]string{"p": project}
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	profile := dtconfig.AIProfile{Model: "gpt-4o"}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private, DetectiveAIProfile: &profile})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	local, _, err := detectiveStore("p", api.SecurityContextID())
	if err != nil {
		t.Fatal(err)
	}
	old, err := local.SaveCase(t.Context(), detective.Case{ID: "case-1", Title: "Invoice case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	provider := new(detectiveAIContextProvider)
	previousProvider := newDetectiveAIProvider
	newDetectiveAIProvider = func(string, string, string) (ai.LLMProvider, error) { return provider, nil }
	t.Cleanup(func() { newDetectiveAIProvider = previousProvider })
	contextID := api.SecurityContextID()
	post := func(route string, payload any, handler http.HandlerFunc) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(payload)
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodPost, "/datatug/detective/"+route+"?project=p", bytes.NewReader(body)))
		return response
	}
	generated := post("ai_proposals", aiGenerateRequest{Project: "p", SecurityContextID: contextID, OperationID: "generate-1", CaseID: old.ID, Text: "invoice 7"}, detectiveAIProposalsHandler)
	var initial aiCore.Response
	if generated.Code != http.StatusOK || json.Unmarshal(generated.Body.Bytes(), &initial) != nil || len(initial.Proposals) != 1 {
		t.Fatalf("generation = %d %s", generated.Code, generated.Body.String())
	}
	original := initial.Proposals[0]
	value := investigation.NewIntegerValue("7")
	reviewed := post("ai_review", aiReviewRequest{Project: "p", SecurityContextID: contextID, Type: "context", Context: &aiCore.ContextReviewRequest{ProposalID: original.ID, Digest: original.Digest, CaseID: old.ID, ExpectedVersion: old.Version, Context: map[string]detective.InputValue{"invoice_id": {Scalar: &value}}}}, detectiveAIReviewHandler)
	var derived aiCore.Proposal
	if reviewed.Code != http.StatusOK || json.Unmarshal(reviewed.Body.Bytes(), &derived) != nil || derived.ParentProposalID != original.ID || derived.Draft.Kind != aiCore.KindCase {
		t.Fatalf("review = %d %s", reviewed.Code, reviewed.Body.String())
	}
	confirm := aiConfirmRequest{Project: "p", SecurityContextID: contextID, ConfirmRequest: aiCore.ConfirmRequest{ProposalID: derived.ID, Digest: derived.Digest, OperationID: "confirm-1"}}
	confirmed := post("ai_confirm", confirm, detectiveAIConfirmHandler)
	var receipt aiCore.Receipt
	if confirmed.Code != http.StatusOK || json.Unmarshal(confirmed.Body.Bytes(), &receipt) != nil || receipt.ResourceID != old.ID {
		t.Fatalf("confirm = %d %s", confirmed.Code, confirmed.Body.String())
	}
	caseAfter, err := local.GetCase(t.Context(), old.ID)
	if err != nil || caseAfter.Version != old.Version+1 || caseAfter.Context["invoice_id"].Scalar == nil {
		t.Fatalf("confirmed Case = %+v, %v", caseAfter, err)
	}
	replay := post("ai_confirm", confirm, detectiveAIConfirmHandler)
	if replay.Code != http.StatusOK || replay.Body.String() != confirmed.Body.String() || provider.calls != 2 {
		t.Fatalf("exact replay = %d %s; model calls=%d", replay.Code, replay.Body.String(), provider.calls)
	}
}
