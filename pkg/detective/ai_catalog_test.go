package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

func TestLocalAICatalogConfirmationRecoveryAndStaleEdit(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	check := Check{ID: "ai-check", Title: "Check invoice", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}
	proposal := ai.Proposal{ID: "reviewed-check", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: &check}}
	binding := core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-draft-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}
	command := ai.Command{Scope: scope, OperationID: "catalog-confirm", Proposal: proposal, Binding: binding}
	receipt, err := local.ConfirmCatalog(ctx, command)
	if err != nil || receipt.ResourceID != check.ID {
		t.Fatalf("confirm Check = %+v, %v", receipt, err)
	}
	got, err := store.GetCheck(ctx, check.ID)
	if err != nil || got.Version != 1 || got.UpdatedBy != scope.Actor {
		t.Fatalf("saved Check = %+v, %v", got, err)
	}
	data, err := safeReadFile(store.privateItemDir("ai-confirm-operations"), command.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var accepted aiCatalogOperation
	if err := json.Unmarshal(data, &accepted); err != nil {
		t.Fatal(err)
	}
	accepted.Done = false
	if err := writeItem(store.privateItemDir("ai-confirm-operations"), command.OperationID, accepted); err != nil {
		t.Fatal(err)
	}
	if replay, found, err := local.AIReceipt(ctx, command.OperationID); err != nil || !found || replay != receipt {
		t.Fatalf("recovery = %+v, %v, %v", replay, found, err)
	}
	if replay, err := local.ConfirmCatalog(ctx, command); err != nil || replay != receipt {
		t.Fatalf("exact retry = %+v, %v", replay, err)
	}
	command.Binding.PayloadDigest = strings.Repeat("b", 64)
	if _, err := local.ConfirmCatalog(ctx, command); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry = %v", err)
	}
	audit, err := os.ReadFile(store.privateDir + "/audit.jsonl")
	if err != nil || strings.Count(string(audit), `"action":"ai-confirm-draft-check"`) != 1 {
		t.Fatalf("metadata audit = %s, %v", audit, err)
	}

	// A reviewed draft based on the old version cannot replace a subsequent edit.
	edit := got
	edit.Title = "Human edit"
	if _, err := store.SaveCheck(ctx, edit, "alice"); err != nil {
		t.Fatal(err)
	}
	stale := got
	stale.Title = "Stale AI edit"
	staleProposal := ai.Proposal{ID: "stale-check", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: &stale}}
	command = ai.Command{Scope: scope, OperationID: "catalog-stale", Proposal: staleProposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-draft-check", ResourceID: staleProposal.ID, PayloadDigest: strings.Repeat("c", 64)}}
	if _, err := local.ConfirmCatalog(ctx, command); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit = %v", err)
	}
}

func TestLocalAICatalogValidationUsesSavedQueryAndGraph(t *testing.T) {
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	check := Check{ID: "bad-check", Title: "Bad", Steps: []Step{{ID: "missing", Kind: "query", QueryID: "missing-query"}}}
	proposal := ai.Proposal{ID: "reviewed-bad", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: &check}}
	command := ai.Command{Scope: scope, OperationID: "bad-confirm", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-draft-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("d", 64)}}
	if _, err := local.ConfirmCatalog(context.Background(), command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing saved query: %v", err)
	}
	if _, err := store.GetCheck(context.Background(), check.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid Check persisted: %v", err)
	}
}
