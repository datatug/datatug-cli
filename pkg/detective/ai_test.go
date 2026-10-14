package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestLocalAIPasteMetadataIsPrivateScopedAndPersistent(t *testing.T) {
	ctx := context.Background()
	project, private := t.TempDir(), t.TempDir()
	store, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	a := LocalAIStore{Store: store, Scope: ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}}
	b := LocalAIStore{Store: store, Scope: ai.Scope{Key: "local:p", Project: "p", Actor: "bob"}}
	const paste = "private ticket invoice 7"
	first, err := a.SavePaste(ctx, paste)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.SavePaste(ctx, paste)
	if err != nil || first.ID == second.ID {
		t.Fatalf("cross-actor source identities: %q %q, %v", first.ID, second.ID, err)
	}
	data, err := safeReadFile(store.privateItemDir("ai-sources"), first.ID)
	if err != nil || strings.Contains(string(data), paste) || strings.Contains(string(data), "private ticket") {
		t.Fatalf("private source retained full paste: %v", err)
	}
	if data, err := os.ReadFile(store.privateDir + "/audit.jsonl"); err == nil && strings.Contains(string(data), paste) {
		t.Fatal("audit retained paste")
	}
	digest := sha256.Sum256([]byte(paste))
	evidence := ai.SourceEvidence{ID: first.ID, Kind: "paste", Revision: first.Revision, Digest: hex.EncodeToString(digest[:]), Length: len(paste)}
	restarted, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	a.Store = restarted
	if err := a.AuthorizeSources(ctx, a.Scope, []ai.SourceEvidence{evidence}); err != nil {
		t.Fatalf("metadata authorization after restart: %v", err)
	}
	if err := b.AuthorizeSources(ctx, b.Scope, []ai.SourceEvidence{evidence}); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("other actor authorized source: %v", err)
	}
	evidence.Length++
	if err := a.AuthorizeSources(ctx, a.Scope, []ai.SourceEvidence{evidence}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered source length: %v", err)
	}
}

func TestLocalAIPasteSourceCapacity(t *testing.T) {
	store, err := NewLocalStore("p", t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner := LocalAIStore{Store: store, Scope: ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}}
	for i := 0; i < maxItems; i++ {
		if _, err := owner.SavePaste(context.Background(), fmt.Sprintf("ticket-%d", i)); err != nil {
			t.Fatalf("source %d: %v", i, err)
		}
	}
	if _, err := owner.SavePaste(context.Background(), "overflow"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	if _, err := owner.SavePaste(context.Background(), "ticket-0"); err != nil {
		t.Fatalf("exact existing source replay at capacity: %v", err)
	}
}

func TestLocalAIGenerationJournalBindsReplayAcrossRestart(t *testing.T) {
	ctx := context.Background()
	project, private := t.TempDir(), t.TempDir()
	store, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	owner := LocalAIStore{Store: store, Scope: ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}}
	binding, err := core.BindOperation(owner.Scope.Key, owner.Scope.Actor, "ai-generate", "operation-1", struct{ Text string }{"invoice 7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, replay, err := owner.BeginAIGeneration(ctx, "operation-1", binding); err != nil || replay {
		t.Fatalf("accept: replay=%v error=%v", replay, err)
	}
	restarted, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	owner.Store = restarted
	if _, _, err := owner.BeginAIGeneration(ctx, "operation-1", binding); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("pending model invocation silently repeated: %v", err)
	}
	result := ai.Response{Text: "Review suggested context."}
	if err := owner.CompleteAIGeneration(ctx, "operation-1", binding, result); err != nil {
		t.Fatal(err)
	}
	got, replay, err := owner.BeginAIGeneration(ctx, "operation-1", binding)
	if err != nil || !replay || got.Text != result.Text {
		t.Fatalf("exact replay = %+v, %v, %v", got, replay, err)
	}
	conflict, err := core.BindOperation(owner.Scope.Key, owner.Scope.Actor, "ai-generate", "operation-1", struct{ Text string }{"invoice 8"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := owner.BeginAIGeneration(ctx, "operation-1", conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed-payload replay: %v", err)
	}
	data, err := os.ReadFile(store.privateDir + "/audit.jsonl")
	if err != nil || strings.Count(string(data), `"action":"ai-generate"`) != 1 || strings.Contains(string(data), "invoice 7") {
		t.Fatalf("audit receipt = %s, %v", data, err)
	}
}

func TestLocalAIConfirmCaseMergesReviewedContextAndRecovers(t *testing.T) {
	ctx := context.Background()
	project, private := t.TempDir(), t.TempDir()
	store, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	one, seven := investigation.NewIntegerValue("1"), investigation.NewIntegerValue("7")
	old, err := store.SaveCase(ctx, Case{ID: "case-1", Title: "Invoice case", Context: map[string]InputValue{"order_id": {Scalar: &one}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	proposal := ai.Proposal{ID: "reviewed-1", Scope: scope, Draft: ai.Draft{Kind: ai.KindCase, Case: &ai.CaseEdit{CaseID: old.ID, ExpectedVersion: old.Version, Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}}}
	binding := core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-edit-case", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}
	command := ai.Command{Scope: scope, OperationID: "confirm-1", Proposal: proposal, Binding: binding}
	receipt, err := local.ConfirmCase(ctx, command)
	if err != nil || receipt.ResourceID != old.ID || receipt.ProposalID != proposal.ID {
		t.Fatalf("confirm = %+v, %v", receipt, err)
	}
	got, err := store.GetCase(ctx, old.ID)
	if err != nil || got.Version != old.Version+1 || got.Context["order_id"].Scalar == nil || got.Context["invoice_id"].Scalar == nil {
		t.Fatalf("merged Case = %+v, %v", got, err)
	}
	if _, err := local.ConfirmCase(ctx, command); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	command.Binding.PayloadDigest = strings.Repeat("b", 64)
	if _, err := local.ConfirmCase(ctx, command); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed confirmation replay: %v", err)
	}
	data, err := safeReadFile(store.privateItemDir("ai-confirm-operations"), "confirm-1")
	if err != nil {
		t.Fatal(err)
	}
	var accepted aiCaseOperation
	if err := json.Unmarshal(data, &accepted); err != nil {
		t.Fatal(err)
	}
	accepted.Done = false
	if err := writeItem(store.privateItemDir("ai-confirm-operations"), "confirm-1", accepted); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLocalStore("p", project, private)
	if err != nil {
		t.Fatal(err)
	}
	local.Store = restarted
	if replay, found, err := local.AIReceipt(ctx, "confirm-1"); err != nil || !found || replay != receipt {
		t.Fatalf("recover prepared receipt = %+v, %v, %v", replay, found, err)
	}
	audit, err := os.ReadFile(store.privateDir + "/audit.jsonl")
	if err != nil || strings.Count(string(audit), `"action":"ai-confirm-edit-case"`) != 1 {
		t.Fatalf("audit duplication: %s, %v", audit, err)
	}
}

func TestAICaseAuditRecoveredBeforeOrdinaryCaseEdit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	created, err := store.SaveCase(ctx, Case{ID: "cross-journal-case", Title: "Initial"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	proposal := ai.Proposal{ID: "reviewed-cross", Scope: scope, Draft: ai.Draft{Kind: ai.KindCase, Case: &ai.CaseEdit{CaseID: created.ID, ExpectedVersion: created.Version, Title: "AI edit"}}}
	command := ai.Command{Scope: scope, OperationID: "ai-cross", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-edit-case", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	store.audit = failingAudit{}
	if _, err := local.ConfirmCase(ctx, command); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("audit interruption: %v", err)
	}
	current, err := store.GetCase(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Title = "Ordinary edit"
	store.audit = localJSONLAudit{path: store.privateDir + "/audit.jsonl"}
	response, err := store.ExecuteAction(ctx, "ordinary-after-ai", "alice", ActionRequest{Type: "update-case", Project: "p", Case: &current}, nil, nil, nil, nil)
	if err != nil || response.Case == nil || response.Case.Version != 3 {
		t.Fatalf("ordinary edit: %+v %v", response, err)
	}
	if receipt, found, err := local.AIReceipt(ctx, command.OperationID); err != nil || !found || receipt.OperationID != command.OperationID {
		t.Fatalf("AI replay: %+v %v %v", receipt, found, err)
	}
	if recorded, err := store.auditRecorded(AuditEvent{ID: command.OperationID, Project: "p", Actor: "alice", Action: "ai-confirm-edit-case"}); err != nil || !recorded {
		t.Fatalf("earlier AI audit: %v %v", recorded, err)
	}
}
