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
	"github.com/datatug/datatug-core/pkg/investigation"
)

func TestLocalAIConfirmedRunExecutesOnceAndRecoversCaseLink(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.SaveCheck(ctx, Check{ID: "invoice-check", Title: "Invoice", Inputs: []Input{{Name: "invoice_id", Type: investigation.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []Step{{ID: "load", Kind: "query", QueryID: "q", Bindings: []Binding{{Input: "invoice_id", FromInput: "invoice_id"}}}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	seven := investigation.NewIntegerValue("7")
	if _, err := store.SaveCase(ctx, Case{ID: "case-1", Title: "Investigate", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	proposal := ai.Proposal{ID: "reviewed-run", Scope: scope, Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: "invoice-check", CaseID: "case-1", Environment: "qa"}}}
	command := ai.Command{Scope: scope, OperationID: "run-confirm", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-run-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	calls := 0
	lookups := 0
	lookup := func(_ context.Context, id string) (string, error) {
		lookups++
		if id != "q" {
			t.Fatalf("unexpected AI admitted query %q", id)
		}
		return "reviewed-revision", nil
	}
	runner := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if revision, ok := AdmittedQueryRevision(ctx, id); !ok || revision != "reviewed-revision" {
			t.Fatalf("AI runner lost admitted revision: %q, %v", revision, ok)
		}
		if id != "q" || env != "qa" || params["invoice_id"].Scalar == nil || *params["invoice_id"].Scalar != seven {
			t.Fatalf("unexpected query %s/%s params=%+v", id, env, params)
		}
		return apicontract.Result{}, nil
	}
	wrong := investigation.NewIntegerValue("99")
	badCommand := command
	badCommand.OperationID = "run-confirm-conflict"
	badCommand.Proposal.Draft.Run = &ai.RunDraft{CheckID: "invoice-check", CaseID: "case-1", Environment: "qa", Inputs: map[string]core.InputValue{"invoice_id": {Scalar: &wrong}}}
	if _, err := local.ConfirmRun(ctx, badCommand, runner, nil); !errors.Is(err, ErrInvalid) || calls != 0 {
		t.Fatalf("AI caller overrode Case cohort: %v calls=%d", err, calls)
	}
	receipt, err := local.ConfirmRun(ctx, command, runner, nil, lookup)
	if err != nil || calls != 1 || lookups != 1 || !strings.HasPrefix(receipt.ResourceID, "run-") {
		t.Fatalf("confirmed run = %+v, calls=%d lookups=%d, err=%v", receipt, calls, lookups, err)
	}
	caseItem, err := store.GetCase(ctx, "case-1")
	if err != nil || len(caseItem.RunRefs) != 1 || caseItem.RunRefs[0] != receipt.ResourceID {
		t.Fatalf("Case run link = %+v, %v", caseItem, err)
	}
	if retry, err := local.ConfirmRun(ctx, command, runner, nil, lookup); err != nil || retry != receipt || calls != 1 || lookups != 1 {
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
	if op.QueryRevisions["q"] != "reviewed-revision" {
		t.Fatalf("AI journal lost admitted revision: %+v", op.QueryRevisions)
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

func TestLocalAIConfirmedRunCannotLinkChangedCaseCohort(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	check, err := store.SaveCheck(ctx, Check{ID: "cohort-check", Title: "Invoice", Inputs: []Input{{Name: "invoice_id", Type: investigation.ValueTypeInteger, Cardinality: "scalar", Required: true}}, Steps: []Step{{ID: "load", Kind: "query", QueryID: "q", Bindings: []Binding{{Input: "invoice_id", FromInput: "invoice_id"}}}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	seven := investigation.NewIntegerValue("7")
	caseItem, err := store.SaveCase(ctx, Case{ID: "cohort-case", Title: "Seven", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	proposal := ai.Proposal{ID: "reviewed-cohort-run", Scope: scope, Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: check.ID, CaseID: caseItem.ID, Environment: "qa"}}}
	command := ai.Command{Scope: scope, OperationID: "ai-changed-cohort", Proposal: proposal, Binding: core.OperationBinding{Project: scope.Key, Actor: scope.Actor, Action: "ai-confirm-run-check", ResourceID: proposal.ID, PayloadDigest: strings.Repeat("a", 64)}}
	calls := 0
	runner := func(_ context.Context, _ string, _ string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		if params["invoice_id"].Scalar == nil || *params["invoice_id"].Scalar != seven {
			t.Fatalf("AI run omitted accepted Case value: %+v", params)
		}
		current, err := store.GetCase(ctx, caseItem.ID)
		if err != nil {
			t.Fatal(err)
		}
		ninetyNine := investigation.NewIntegerValue("99")
		current.Context = map[string]InputValue{"invoice_id": {Scalar: &ninetyNine}}
		if _, err := store.SaveCase(ctx, current, "alice", "update-case"); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	receipt, err := local.ConfirmRun(ctx, command, runner, nil)
	if err != nil || receipt.ResourceID == "" || calls != 1 {
		t.Fatalf("AI result lost after Case changed: %+v %v calls=%d", receipt, err, calls)
	}
	current, err := store.GetCase(ctx, caseItem.ID)
	if err != nil || len(current.RunRefs) != 0 {
		t.Fatalf("changed Case linked AI run: %+v %v", current, err)
	}
	data, err := safeReadFile(store.privateItemDir("ai-confirm-operations"), command.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var op aiRunOperation
	if json.Unmarshal(data, &op) != nil || op.CaseLinkStatus != "case-changed" || !op.Done {
		t.Fatalf("AI partial link status unavailable: %+v", op)
	}
	if _, err := store.appendServerCaseRun(ctx, caseItem.ID, receipt.ResourceID, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manual link bypassed changed AI cohort: %v", err)
	}
	if _, err := local.ConfirmRun(ctx, command, runner, nil); err != nil || calls != 1 {
		t.Fatalf("AI replay repeated query: %v calls=%d", err, calls)
	}
}
