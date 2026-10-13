package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

func TestLocalAIConfirmedRunExecutesOnceAndRecoversCaseLink(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.SaveCheck(ctx, Check{ID: "invoice-check", Title: "Invoice", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	proposal := ai.Proposal{ID: "reviewed-run", Scope: scope, Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: "invoice-check", CaseID: "case-1", Environment: "qa"}}}
	command := ai.Command{Scope: scope, OperationID: "run-confirm", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-run-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	calls := 0
	runner := func(_ context.Context, id, env string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if id != "q" || env != "qa" {
			t.Fatalf("unexpected query %s/%s", id, env)
		}
		return apicontract.Result{}, nil
	}
	receipt, err := local.ConfirmRun(ctx, command, runner, nil)
	if err != nil || calls != 1 || !strings.HasPrefix(receipt.ResourceID, "run-") {
		t.Fatalf("confirmed run = %+v, calls=%d, err=%v", receipt, calls, err)
	}
	caseItem, err := store.GetCase(ctx, "case-1")
	if err != nil || len(caseItem.RunRefs) != 1 || caseItem.RunRefs[0] != receipt.ResourceID {
		t.Fatalf("Case run link = %+v, %v", caseItem, err)
	}
	if retry, err := local.ConfirmRun(ctx, command, runner, nil); err != nil || retry != receipt || calls != 1 {
		t.Fatalf("duplicate execution on replay: %+v %v calls=%d", retry, err, calls)
	}
	data, err := safeReadFile(store.privateItemDir("ai-confirm-operations"), command.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var op aiRunOperation
	if err := json.Unmarshal(data, &op); err != nil {
		t.Fatal(err)
	}
	op.Done = false
	if err := writeItem(store.privateItemDir("ai-confirm-operations"), command.OperationID, op); err != nil {
		t.Fatal(err)
	}
	if replay, found, err := local.AIReceipt(ctx, command.OperationID); err != nil || !found || replay != receipt || calls != 1 {
		t.Fatalf("recover produced run = %+v found=%v err=%v calls=%d", replay, found, err, calls)
	}
	command.Binding.PayloadDigest = strings.Repeat("b", 64)
	if _, err := local.ConfirmRun(ctx, command, runner, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed run replay = %v", err)
	}
}

func TestLocalAIConfirmedRunRejectsChangedBasisAndPinsAcceptedCatalog(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	check, err := store.SaveCheck(ctx, Check{ID: "basis-check", Title: "Reviewed", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope, BasisVerifier: func(_ context.Context, basis ai.Snapshot, state State) error {
		if basis.CatalogRevision != "reviewed" || len(state.Checks) != 1 || state.Checks[0].Version != 1 {
			return ai.ErrStale
		}
		return nil
	}}
	proposal := ai.Proposal{ID: "basis-run", Scope: scope, Basis: ai.Snapshot{CatalogRevision: "reviewed"}, Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: check.ID, Environment: "qa"}}}
	command := ai.Command{Scope: scope, OperationID: "basis-run-op", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-run-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	changed := check
	changed.Title = "Changed after review"
	changed, err = store.SaveCheck(ctx, changed, "alice")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(_ context.Context, queryID, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if queryID != "q" {
			t.Fatalf("unreviewed query executed: %s", queryID)
		}
		return apicontract.Result{}, nil
	}
	if _, err := local.ConfirmRun(ctx, command, runner, nil); !errors.Is(err, ai.ErrStale) || calls != 0 {
		t.Fatalf("stale dispatch: %v calls=%d", err, calls)
	}
	if _, err := safeReadFile(store.privateItemDir("ai-confirm-operations"), command.OperationID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale run accepted: %v", err)
	}
	local.BasisVerifier = func(_ context.Context, basis ai.Snapshot, state State) error {
		if basis.CatalogRevision != "current" || len(state.Checks) != 1 || state.Checks[0].Version != changed.Version {
			return ai.ErrStale
		}
		return nil
	}
	command.Proposal.Basis.CatalogRevision = "current"
	command.OperationID = "pinned-run-op"
	draft := *command.Proposal.Draft.Run
	command.Proposal.Draft.Run = &draft
	runner = func(_ context.Context, queryID, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if queryID != "q" {
			t.Fatalf("accepted catalog changed under run: %s", queryID)
		}
		newer := changed
		newer.Steps[0].QueryID = "child-q"
		if _, err := store.SaveCheck(ctx, newer, "alice"); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	if _, err := local.ConfirmRun(ctx, command, runner, nil); err != nil || calls != 1 {
		t.Fatalf("accepted sealed run: %v calls=%d", err, calls)
	}
}

func TestLocalAIConfirmedRunRetainsEvidenceAcrossConcurrentCaseClose(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.SaveCheck(ctx, Check{ID: "invoice-check", Title: "Invoice", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	proposal := ai.Proposal{ID: "reviewed-run", Scope: scope, Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: "invoice-check", CaseID: "case-1", Environment: "qa"}}}
	command := ai.Command{Scope: scope, OperationID: "run-during-close", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-run-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		current, err := store.GetCase(ctx, "case-1")
		if err != nil {
			t.Fatal(err)
		}
		current.Status, current.ClosureReason = "closed", "Reviewed while query ran"
		if _, err := store.SaveCase(ctx, current, "alice", "update-case"); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	receipt, err := local.ConfirmRun(ctx, command, runner, nil)
	if err != nil || calls != 1 || receipt.ResourceID == "" {
		t.Fatalf("accepted AI result lost: %+v, %v, calls=%d", receipt, err, calls)
	}
	caseItem, err := store.GetCase(ctx, "case-1")
	if err != nil || caseItem.Status != "closed" || len(caseItem.RunRefs) != 1 || caseItem.RunRefs[0] != receipt.ResourceID {
		t.Fatalf("closed Case lost AI run evidence: %+v, %v", caseItem, err)
	}
	if _, err := local.ConfirmRun(ctx, command, runner, nil); err != nil || calls != 1 {
		t.Fatalf("replay repeated AI query: %v, calls=%d", err, calls)
	}
}
