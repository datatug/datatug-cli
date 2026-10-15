package detective

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// Not parallel: tests race conditions and file system permission manipulation for ai_run.go.
func TestCoverage100_AIRun_ConfirmRun_ValidationAndReplay(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	proposal := ai.Proposal{ID: "prop-1", Scope: scope, Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: "chk-1", Environment: "qa"}}}
	validCommand := ai.Command{
		Scope:       scope,
		OperationID: "op-run-1",
		Proposal:    proposal,
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-run-check",
			ResourceID:    proposal.ID,
			PayloadDigest: strings.Repeat("a", 64),
		},
	}
	dummyRunner := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}

	// 1. Validation errors in ConfirmRun
	// s.Store == nil
	if _, err := (LocalAIStore{Scope: scope}).ConfirmRun(ctx, validCommand, dummyRunner, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Store")
	}
	// runner == nil
	if _, err := local.ConfirmRun(ctx, validCommand, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil runner")
	}
	// bad slugs / bindings / kinds
	badCommands := []ai.Command{
		func() ai.Command { c := validCommand; c.Scope.Key = "other"; return c }(),
		func() ai.Command { c := validCommand; c.Proposal.Scope.Key = "other"; return c }(),
		func() ai.Command { c := validCommand; c.Proposal.Draft.Kind = "other"; return c }(),
		func() ai.Command { c := validCommand; c.Proposal.Draft.Run = nil; return c }(),
		func() ai.Command { c := validCommand; c.OperationID = "invalid slug!"; return c }(),
		func() ai.Command { c := validCommand; c.Binding.Project = "wrong"; return c }(),
		func() ai.Command { c := validCommand; c.Binding.Actor = "wrong"; return c }(),
		func() ai.Command { c := validCommand; c.Binding.Action = "wrong"; return c }(),
		func() ai.Command { c := validCommand; c.Binding.ResourceID = "wrong"; return c }(),
		func() ai.Command { c := validCommand; c.Binding.PayloadDigest = "short"; return c }(),
		func() ai.Command {
			c := validCommand
			c.Proposal.Draft.Run = &ai.RunDraft{CheckID: "invalid slug!", Environment: "qa"}
			return c
		}(),
		func() ai.Command {
			c := validCommand
			c.Proposal.Draft.Run = &ai.RunDraft{CheckID: "chk-1", Environment: ""}
			return c
		}(),
		func() ai.Command {
			c := validCommand
			c.Proposal.Draft.Run = &ai.RunDraft{CheckID: "chk-1", Environment: "qa", CaseID: "invalid slug!"}
			return c
		}(),
	}
	for i, bad := range badCommands {
		if _, err := local.ConfirmRun(ctx, bad, dummyRunner, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("badCommands[%d]: expected ErrInvalid, got %v", i, err)
		}
	}

	// 2. Lock 1 errors:
	// a) reconcilePendingActions error on lock 1
	actDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	_ = os.WriteFile(filepath.Join(actDir, "bad-action.json"), []byte("{"), 0600)
	caseCommand := validCommand
	caseCommand.Proposal.Draft.Run = &ai.RunDraft{CheckID: "chk-1", Environment: "qa", CaseID: "case-1"}
	if _, err := local.ConfirmRun(ctx, caseCommand, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from reconcilePendingActions in lock 1")
	}
	_ = os.Remove(filepath.Join(actDir, "bad-action.json"))

	// b) safeReadFile error (directory in place of ai-confirm-operations/op-dir.json)
	confirmDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(confirmDir, 0700)
	dirOpPath := filepath.Join(confirmDir, "op-dir.json")
	_ = os.MkdirAll(dirOpPath, 0700)
	dirCommand := validCommand
	dirCommand.OperationID = "op-dir"
	if _, err := local.ConfirmRun(ctx, dirCommand, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from safeReadFile on directory in lock 1")
	}
	_ = os.RemoveAll(dirOpPath)

	// c) replay prior: invalid json / kind != run / receipt mismatch -> ErrConflict
	badPriorCmd := validCommand
	badPriorCmd.OperationID = "op-bad-prior"
	_ = writeItem(confirmDir, "op-bad-prior", map[string]any{"kind": "other"})
	if _, err := local.ConfirmRun(ctx, badPriorCmd, dummyRunner, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for bad prior kind")
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-bad-prior.json"))

	// d) replay prior: !prior.Produced -> ErrOutcomeUncertain
	unprodCmd := validCommand
	unprodCmd.OperationID = "op-unprod"
	unprodReceipt := ai.Receipt{
		OperationID:   "op-unprod",
		ScopeKey:      scope.Key,
		Project:       scope.Project,
		Actor:         scope.Actor,
		ProposalID:    proposal.ID,
		PayloadDigest: validCommand.Binding.PayloadDigest,
	}
	_ = writeItem(confirmDir, "op-unprod", aiRunOperation{
		Kind:     "run",
		Receipt:  unprodReceipt,
		Produced: false,
	})
	if _, err := local.ConfirmRun(ctx, unprodCmd, dummyRunner, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain for !prior.Produced")
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-unprod.json"))

	// e) replay prior: !prior.Done and finishAIRun fails -> return error
	failFinishCmd := validCommand
	failFinishCmd.OperationID = "op-fail-fin"
	failFinishReceipt := ai.Receipt{
		OperationID:   "op-fail-fin",
		ScopeKey:      scope.Key,
		Project:       scope.Project,
		Actor:         scope.Actor,
		ProposalID:    proposal.ID,
		PayloadDigest: validCommand.Binding.PayloadDigest,
		ResourceID:    "run-mismatch",
	}
	_ = writeItem(confirmDir, "op-fail-fin", aiRunOperation{
		Kind:     "run",
		Receipt:  failFinishReceipt,
		Produced: true,
		Done:     false,
		Run:      CheckRun{ID: "different-id"}, // causes finishAIRun to return ErrInvalid
	})
	if _, err := local.ConfirmRun(ctx, failFinishCmd, dummyRunner, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when finishAIRun fails on replay")
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-fail-fin.json"))
}

// Not parallel: tests Lock 2 branches and evaluation errors for ai_run.go.
func TestCoverage100_AIRun_ConfirmRun_Lock2AndExecution(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	confirmDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(confirmDir, 0700)

	// Save a check for valid executions
	check, err := store.SaveCheck(ctx, Check{
		ID:    "eval-check",
		Title: "Eval",
		Inputs: []Input{
			{Name: "req_in", Type: investigation.ValueTypeString, Cardinality: "scalar", Required: true},
		},
		Steps: []Step{
			{ID: "s1", Kind: "query", QueryID: "q", Bindings: []Binding{{Input: "req_in", FromInput: "req_in"}}},
		},
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}

	proposal := ai.Proposal{
		ID:    "prop-eval",
		Scope: scope,
		Draft: ai.Draft{
			Kind: ai.KindRun,
			Run: &ai.RunDraft{
				CheckID:     check.ID,
				Environment: "qa",
				Inputs:      map[string]core.InputValue{"req_in": {Scalar: ptr(investigation.NewStringValue("hello"))}},
			},
		},
	}
	cmd := ai.Command{
		Scope:       scope,
		OperationID: "op-eval-1",
		Proposal:    proposal,
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-run-check",
			ResourceID:    proposal.ID,
			PayloadDigest: strings.Repeat("a", 64),
		},
	}

	dummyRunner := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}

	// 1. Case validation in Lock 2:
	// a) Case does not exist -> getCase error
	caseMissingCmd := cmd
	caseMissingCmd.OperationID = "op-case-missing"
	caseMissingCmd.Proposal.Draft.Run = &ai.RunDraft{CheckID: check.ID, CaseID: "missing-case", Environment: "qa"}
	if _, err := local.ConfirmRun(ctx, caseMissingCmd, dummyRunner, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	// b) Case exists but Status != "active" -> ErrInvalid
	archCase := Case{ID: "c-arch-lock2", Title: "Archived", Status: "archived"}
	_ = writeItem(store.privateItemDir("cases"), "c-arch-lock2", archCase)
	caseArchCmd := cmd
	caseArchCmd.OperationID = "op-case-arch-lock2"
	caseArchCmd.Proposal.Draft.Run = &ai.RunDraft{CheckID: check.ID, CaseID: "c-arch-lock2", Environment: "qa"}
	if _, err := local.ConfirmRun(ctx, caseArchCmd, dummyRunner, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for non-active case: %v", err)
	}

	// c) caseContextDigest error in Lock 2
	origDigest := caseContextDigest
	caseContextDigest = func(values map[string]InputValue) (string, error) {
		return "", errors.New("digest failed")
	}
	activeCase := Case{ID: "c-active-digest-fail", Title: "Active", Status: "active"}
	_ = writeItem(store.privateItemDir("cases"), "c-active-digest-fail", activeCase)
	digestFailCmd := cmd
	digestFailCmd.OperationID = "op-digest-fail"
	digestFailCmd.Proposal.Draft.Run = &ai.RunDraft{CheckID: check.ID, CaseID: "c-active-digest-fail", Environment: "qa"}
	if _, err := local.ConfirmRun(ctx, digestFailCmd, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from caseContextDigest in ConfirmRun")
	}
	caseContextDigest = origDigest

	// d) loadCatalog error and check not in catalog (ok == false)
	origLoadCatalog := loadCatalog
	loadCatalog = func(ctx context.Context, rootCheckID, rootQueryID string, reader core.CatalogReader) (core.Catalog, error) {
		return core.Catalog{}, errors.New("load catalog failed")
	}
	if _, err := local.ConfirmRun(ctx, cmd, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from loadCatalog")
	}
	loadCatalog = func(ctx context.Context, rootCheckID, rootQueryID string, reader core.CatalogReader) (core.Catalog, error) {
		return core.Catalog{Checks: map[string]core.Check{}}, nil
	}
	if _, err := local.ConfirmRun(ctx, cmd, dummyRunner, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound when check missing from catalog: %v", err)
	}
	loadCatalog = origLoadCatalog

	// e) evaluateCheck error
	origEvaluateCheck := evaluateCheck
	evaluateCheck = func(ctx context.Context, req core.RunRequest, catalog core.Catalog, runner core.QueryRunner, metadata core.ExecutionMetadata) (core.CheckRun, error) {
		return core.CheckRun{}, errors.New("evaluateCheck failed")
	}
	evalFailCmd := cmd
	evalFailCmd.OperationID = "op-eval-fail"
	if _, err := local.ConfirmRun(ctx, evalFailCmd, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from evaluateCheck")
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-eval-fail.json"))
	evaluateCheck = origEvaluateCheck

	// d) Revisions lookup error in captureQueryRevisions
	revErrCmd := cmd
	revErrCmd.OperationID = "op-rev-err"
	errLookup := func(ctx context.Context, id string) (string, error) {
		return "", errors.New("lookup failed")
	}
	if _, err := local.ConfirmRun(ctx, revErrCmd, dummyRunner, nil, errLookup); err == nil {
		t.Fatalf("expected error from lookup")
	}

	// e) Input validation error (missing required input)
	noInputCmd := cmd
	noInputCmd.OperationID = "op-no-in"
	noInputCmd.Proposal.Draft.Run = &ai.RunDraft{CheckID: check.ID, Environment: "qa", Inputs: map[string]core.InputValue{}}
	if _, err := local.ConfirmRun(ctx, noInputCmd, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from ValidateInputs")
	}

	// f) writeItem error in Lock 2 (chmod 0500 on ai-confirm-operations)
	writeErrCmd := cmd
	writeErrCmd.OperationID = "op-werr-2"
	if err := os.Chmod(confirmDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ConfirmRun(ctx, writeErrCmd, dummyRunner, nil); err == nil {
		t.Fatalf("expected error from writeItem in lock 2")
	}
	_ = os.Chmod(confirmDir, 0700)

	// g) EvaluateCheck: runner returns error or result, EvaluateCheck succeeds and records in Steps
	runnerErrCmd := cmd
	runnerErrCmd.OperationID = "op-run-fail"
	failingRunner := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, errors.New("query runner exploded")
	}
	if _, err := local.ConfirmRun(ctx, runnerErrCmd, failingRunner, nil); err != nil {
		t.Fatalf("unexpected error from failing runner (should record in steps): %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-run-fail.json"))

	// h) Lock 3: safeReadFile error (directory in place of op file)
	lock3SafeErrCmd := cmd
	lock3SafeErrCmd.OperationID = "op-lock3-safe-err"
	runnerWithSafeErr := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		// Replace operation file with a directory before Lock 3 acquires lock
		p := filepath.Join(confirmDir, "op-lock3-safe-err.json")
		_ = os.Remove(p)
		_ = os.MkdirAll(p, 0700)
		return apicontract.Result{}, nil
	}
	if _, err := local.ConfirmRun(ctx, lock3SafeErrCmd, runnerWithSafeErr, nil); err == nil {
		t.Fatalf("expected safeReadFile error in Lock 3")
	}
	_ = os.RemoveAll(filepath.Join(confirmDir, "op-lock3-safe-err.json"))

	// i) Lock 3: conflict (corrupt or already produced)
	lock3ConflictCmd := cmd
	lock3ConflictCmd.OperationID = "op-lock3-conflict"
	runnerWithConflict := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		// Modify operation file to already be Produced
		p := filepath.Join(confirmDir, "op-lock3-conflict.json")
		data, _ := os.ReadFile(p)
		var op aiRunOperation
		_ = json.Unmarshal(data, &op)
		op.Produced = true
		_ = writeItem(confirmDir, "op-lock3-conflict", op)
		return apicontract.Result{}, nil
	}
	if _, err := local.ConfirmRun(ctx, lock3ConflictCmd, runnerWithConflict, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict in Lock 3: %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-lock3-conflict.json"))

	// j) Lock 3: writeItem error (chmod 0500 on confirmDir during runner)
	lock3WriteErrCmd := cmd
	lock3WriteErrCmd.OperationID = "op-lock3-werr"
	runnerWithWriteErr := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		_ = os.Chmod(confirmDir, 0500)
		return apicontract.Result{}, nil
	}
	if _, err := local.ConfirmRun(ctx, lock3WriteErrCmd, runnerWithWriteErr, nil); err == nil {
		t.Fatalf("expected writeItem error in Lock 3")
	}
	_ = os.Chmod(confirmDir, 0700)
	_ = os.Remove(filepath.Join(confirmDir, "op-lock3-werr.json"))
}

// Not parallel: tests concurrent interlock between Lock 1 and Lock 2 in ConfirmRun.
func TestCoverage100_AIRun_ConfirmRun_InterlockBetweenLock1And2(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	confirmDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(confirmDir, 0700)

	check, err := store.SaveCheck(ctx, Check{
		ID:    "inter-chk",
		Title: "Inter",
		Steps: []Step{{ID: "s1", Kind: "query", QueryID: "q"}},
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	proposal := ai.Proposal{
		ID:    "prop-inter",
		Scope: scope,
		Draft: ai.Draft{Kind: ai.KindRun, Run: &ai.RunDraft{CheckID: check.ID, Environment: "qa"}},
	}
	cmd := ai.Command{
		Scope:       scope,
		OperationID: "op-inter",
		Proposal:    proposal,
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-run-check",
			ResourceID:    proposal.ID,
			PayloadDigest: strings.Repeat("a", 64),
		},
	}
	dummyRunner := func(ctx context.Context, id, env string, params map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}

	defer func() { confirmRunInterlock = nil }()

	// 1. Conflict: between Lock 1 and Lock 2, another operation is inserted with mismatched kind/receipt
	confirmRunInterlock = func() {
		_ = writeItem(confirmDir, "op-inter", aiRunOperation{Kind: "wrong"})
	}
	if _, err := local.ConfirmRun(ctx, cmd, dummyRunner, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict from interlocked Lock 2, got: %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-inter.json"))

	// 2. Matching receipt -> ErrOutcomeUncertain (line 96)
	cmd2 := cmd
	cmd2.OperationID = "op-inter-unc"
	confirmRunInterlock = func() {
		matchingReceipt := ai.Receipt{
			OperationID:   "op-inter-unc",
			ScopeKey:      scope.Key,
			Project:       scope.Project,
			Actor:         scope.Actor,
			ProposalID:    proposal.ID,
			PayloadDigest: cmd2.Binding.PayloadDigest,
		}
		_ = writeItem(confirmDir, "op-inter-unc", aiRunOperation{Kind: "run", Receipt: matchingReceipt})
	}
	if _, err := local.ConfirmRun(ctx, cmd2, dummyRunner, nil); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from interlocked Lock 2, got: %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-inter-unc.json"))

	// 3. Directory in place of op file -> safeReadFile error (line 99)
	cmd3 := cmd
	cmd3.OperationID = "op-inter-dir"
	confirmRunInterlock = func() {
		_ = os.MkdirAll(filepath.Join(confirmDir, "op-inter-dir.json"), 0700)
	}
	if _, err := local.ConfirmRun(ctx, cmd3, dummyRunner, nil); err == nil {
		t.Fatalf("expected safeReadFile error in Lock 2")
	}
	_ = os.RemoveAll(filepath.Join(confirmDir, "op-inter-dir.json"))

	// 4. Bad-action file in action-operations -> reconcilePendingActions error (line 88)
	cmd4 := cmd
	cmd4.OperationID = "op-inter-act"
	cmd4.Proposal.Draft.Run = &ai.RunDraft{CheckID: check.ID, CaseID: "case-inter", Environment: "qa"}
	actDir := store.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	confirmRunInterlock = func() {
		_ = os.WriteFile(filepath.Join(actDir, "bad-action.json"), []byte("{"), 0600)
	}
	if _, err := local.ConfirmRun(ctx, cmd4, dummyRunner, nil); err == nil {
		t.Fatalf("expected reconcilePendingActions error in Lock 2 (line 88)")
	}
	_ = os.Remove(filepath.Join(actDir, "bad-action.json"))
}

// Not parallel: tests all branch paths of finishAIRun directly.
func TestCoverage100_AIRun_FinishAIRun_AllBranches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	runsDir := store.privateItemDir("runs")
	confirmDir := store.privateItemDir("ai-confirm-operations")
	cohortDir := store.privateItemDir("run-cohorts")
	_ = os.MkdirAll(runsDir, 0700)
	_ = os.MkdirAll(confirmDir, 0700)
	_ = os.MkdirAll(cohortDir, 0700)

	c, err := store.SaveCase(ctx, Case{ID: "c-fin", Title: "Case Fin", Status: "active"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	validRun := CheckRun{
		ID:          "run-100",
		CheckID:     "chk-1",
		CaseID:      c.ID,
		Environment: "qa",
		Actor:       "alice",
		StartedAt:   now,
	}
	validReceipt := ai.Receipt{
		OperationID:   "op-fin-1",
		ScopeKey:      scope.Key,
		Project:       scope.Project,
		Actor:         scope.Actor,
		ProposalID:    "prop-1",
		PayloadDigest: strings.Repeat("a", 64),
		ResourceID:    validRun.ID,
		At:            now,
	}
	baseOp := aiRunOperation{
		Kind:              "run",
		Receipt:           validReceipt,
		Draft:             ai.RunDraft{CheckID: "chk-1", CaseID: c.ID, Environment: "qa"},
		CaseContextDigest: strings.Repeat("0", 64),
		Run:               validRun,
		Produced:          true,
	}

	// 1. Invalid shapes for finishAIRun
	invalidOps := []aiRunOperation{
		func() aiRunOperation { o := baseOp; o.Kind = "other"; return o }(),
		func() aiRunOperation { o := baseOp; o.Produced = false; return o }(),
		func() aiRunOperation { o := baseOp; o.Run.ID = "wrong"; return o }(),
		func() aiRunOperation { o := baseOp; o.Run.Actor = "wrong"; return o }(),
		func() aiRunOperation { o := baseOp; o.Run.CaseID = "wrong"; return o }(),
	}
	for i, bad := range invalidOps {
		if err := local.finishAIRun(ctx, "op-bad", &bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidOps[%d]: expected ErrInvalid, got %v", i, err)
		}
	}

	// 2. saveRunCohortProof error (cohortDir chmod 0500)
	if err := os.Chmod(cohortDir, 0500); err != nil {
		t.Fatal(err)
	}
	testOp := baseOp
	if err := local.finishAIRun(ctx, "op-proof-err", &testOp); err == nil {
		t.Fatalf("expected error from saveRunCohortProof in read-only cohortDir")
	}
	_ = os.Chmod(cohortDir, 0700)

	// 3. safeReadFile runs error (directory in place of run file)
	_ = os.MkdirAll(filepath.Join(runsDir, validRun.ID+".json"), 0700)
	testOp = baseOp
	if err := local.finishAIRun(ctx, "op-runs-read-err", &testOp); err == nil {
		t.Fatalf("expected error reading directory run file")
	}
	_ = os.RemoveAll(filepath.Join(runsDir, validRun.ID+".json"))

	// 4. writeItem runs error (runsDir chmod 0500)
	if err := os.Chmod(runsDir, 0500); err != nil {
		t.Fatal(err)
	}
	testOp = baseOp
	if err := local.finishAIRun(ctx, "op-runs-write-err", &testOp); err == nil {
		t.Fatalf("expected error writing run in read-only runsDir")
	}
	_ = os.Chmod(runsDir, 0700)

	// 5. Existing run file conflict (different run on disk)
	conflictRun := validRun
	conflictRun.Environment = "prod"
	_ = writeItem(runsDir, validRun.ID, conflictRun)
	testOp = baseOp
	if err := local.finishAIRun(ctx, "op-runs-conflict", &testOp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for mismatching run on disk")
	}
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// 6. auditRecorded error for runAudit (audit.jsonl chmod 0000)
	auditPath := filepath.Join(store.privateDir, "audit.jsonl")
	_ = os.WriteFile(auditPath, []byte("{}"), 0600)
	_ = os.Chmod(auditPath, 0000)
	testOp = baseOp
	if err := local.finishAIRun(ctx, "op-audit-err", &testOp); err == nil {
		t.Fatalf("expected error from auditRecorded with unreadable audit file")
	}
	_ = os.Chmod(auditPath, 0600)
	_ = os.Remove(auditPath)
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))
	_ = os.Remove(filepath.Join(cohortDir, validRun.ID+".json"))

	// 7. Case linking branches:
	// a) Case unavailable (Draft.CaseID missing)
	testOp = baseOp
	testOp.CaseContextDigest = ""
	testOp.Draft.CaseID = "nonexistent-case"
	testOp.Run.CaseID = "nonexistent-case"
	if err := local.finishAIRun(ctx, "op-case-unavail", &testOp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if testOp.CaseLinkStatus != "case-unavailable" {
		t.Fatalf("expected case-unavailable, got %s", testOp.CaseLinkStatus)
	}
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// b) getCase error (directory in place of case file)
	caseDir := store.privateItemDir("cases")
	_ = os.MkdirAll(filepath.Join(caseDir, "bad-case.json"), 0700)
	testOp = baseOp
	testOp.Draft.CaseID = "bad-case"
	testOp.Run.CaseID = "bad-case"
	if err := local.finishAIRun(ctx, "op-bad-case", &testOp); err == nil {
		t.Fatalf("expected error from getCase on directory")
	}
	_ = os.RemoveAll(filepath.Join(caseDir, "bad-case.json"))
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// c) Case archived -> CaseLinkStatus = "case-archived"
	archivedC := c
	archivedC.ID = "c-arch"
	archivedC.Status = "archived"
	_ = writeItem(caseDir, "c-arch", archivedC)
	testOp = baseOp
	testOp.CaseContextDigest = ""
	testOp.Draft.CaseID = archivedC.ID
	testOp.Run.CaseID = archivedC.ID
	if err := local.finishAIRun(ctx, "op-case-archived", &testOp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if testOp.CaseLinkStatus != "case-archived" {
		t.Fatalf("expected case-archived, got %s", testOp.CaseLinkStatus)
	}
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// d) authorizeCaseRunLink error other than ErrInvalid (unreadable file in run-cohorts/run-id.json)
	cohortPath := filepath.Join(cohortDir, validRun.ID+".json")
	_ = os.WriteFile(cohortPath, []byte("{}"), 0600)
	_ = os.Chmod(cohortPath, 0000)
	testOp = baseOp
	testOp.CaseContextDigest = "" // bypass saveRunCohortProof
	if err := local.finishAIRun(ctx, "op-cohort-read-err", &testOp); err == nil {
		t.Fatalf("expected error from authorizeCaseRunLink reading unreadable cohort file")
	}
	_ = os.Chmod(cohortPath, 0600)
	_ = os.Remove(cohortPath)
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// e) authorizeCaseRunLink returns ErrInvalid -> CaseLinkStatus = "case-changed"
	// Create case with non-empty context, but no proof stored in run-cohorts
	seven := investigation.NewIntegerValue("7")
	cohortCase, err := store.SaveCase(ctx, Case{ID: "c-cohort", Title: "Cohort", Status: "active", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	testOp = baseOp
	testOp.Draft.CaseID = cohortCase.ID
	testOp.Run.CaseID = cohortCase.ID
	testOp.CaseContextDigest = "" // don't write proof
	if err := local.finishAIRun(ctx, "op-case-changed", &testOp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if testOp.CaseLinkStatus != "case-changed" {
		t.Fatalf("expected case-changed, got %s", testOp.CaseLinkStatus)
	}
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// f) AttachRun error -> CaseLinkStatus = "link-unavailable"
	// Case already has validRun.ID in its RunRefs, so AttachRun returns ErrConflict
	caseWithRun := c
	caseWithRun.ID = "c-with-run"
	caseWithRun.RunRefs = []string{validRun.ID}
	_ = writeItem(caseDir, "c-with-run", caseWithRun)
	proofDigest, _ := caseContextDigest(caseWithRun.Context)
	_ = writeItem(cohortDir, validRun.ID, runCohortProof{RunID: validRun.ID, CaseID: caseWithRun.ID, ContextDigest: proofDigest})
	testOp = baseOp
	testOp.Draft.CaseID = caseWithRun.ID
	testOp.Run.CaseID = caseWithRun.ID
	testOp.CaseContextDigest = ""
	if err := local.finishAIRun(ctx, "op-link-unavail", &testOp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if testOp.CaseLinkStatus != "link-unavailable" {
		t.Fatalf("expected link-unavailable, got %s", testOp.CaseLinkStatus)
	}
	_ = os.Remove(filepath.Join(cohortDir, validRun.ID+".json"))
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// g) compactCaseTimeline error (>400 events and case-timeline dir chmod 0500)
	timelineDir := store.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	hugeEvents := make([]CaseEvent, 405)
	for i := range hugeEvents {
		hugeEvents[i] = CaseEvent{ID: fmt.Sprintf("ev-%03d", i), Type: "note"}
	}
	hugeCase := c
	hugeCase.ID = "c-huge"
	hugeCase.Timeline = hugeEvents
	_ = writeItem(caseDir, "c-huge", hugeCase)
	proofDigest, _ = caseContextDigest(hugeCase.Context)
	_ = writeItem(cohortDir, validRun.ID, runCohortProof{RunID: validRun.ID, CaseID: hugeCase.ID, ContextDigest: proofDigest})
	if err := os.Chmod(timelineDir, 0500); err != nil {
		t.Fatal(err)
	}
	testOp = baseOp
	testOp.Draft.CaseID = hugeCase.ID
	testOp.Run.CaseID = hugeCase.ID
	testOp.CaseContextDigest = ""
	if err := local.finishAIRun(ctx, "op-compact-err", &testOp); err == nil {
		t.Fatalf("expected compactCaseTimeline error")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = os.Remove(filepath.Join(cohortDir, validRun.ID+".json"))
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// h) writeItem error for intermediate ai-confirm-operations (chmod 0500)
	if err := os.Chmod(confirmDir, 0500); err != nil {
		t.Fatal(err)
	}
	testOp = baseOp
	testOp.CaseContextDigest = ""
	if err := local.finishAIRun(ctx, "op-mid-werr", &testOp); err == nil {
		t.Fatalf("expected writeItem error on ai-confirm-operations")
	}
	_ = os.Chmod(confirmDir, 0700)
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// 8. Branches when op.After != nil:
	// a) op.Before == nil -> ErrInvalid
	afterCase := c
	afterOp := baseOp
	afterOp.After = &afterCase
	afterOp.Before = nil
	afterOp.CaseAudit = &AuditEvent{}
	if err := local.finishAIRun(ctx, "op-after-nobefore", &afterOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when Before == nil")
	}
	// b) op.CaseAudit == nil -> ErrInvalid
	afterOp = baseOp
	afterOp.After = &afterCase
	afterOp.Before = &c
	afterOp.CaseAudit = nil
	if err := local.finishAIRun(ctx, "op-after-noaudit", &afterOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when CaseAudit == nil")
	}
	// c) op.After.ID != op.Draft.CaseID -> ErrInvalid
	wrongAfter := c
	wrongAfter.ID = "other-case"
	afterOp = baseOp
	afterOp.After = &wrongAfter
	afterOp.Before = &c
	afterOp.CaseAudit = &AuditEvent{}
	if err := local.finishAIRun(ctx, "op-after-wrongid", &afterOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when After.ID != Draft.CaseID")
	}
	// d) getCase error (directory in place of cases/c-fin.json)
	_ = os.Remove(filepath.Join(caseDir, c.ID+".json"))
	_ = os.MkdirAll(filepath.Join(caseDir, c.ID+".json"), 0700)
	afterOp = baseOp
	afterOp.After = &afterCase
	afterOp.Before = &c
	afterOp.CaseAudit = &AuditEvent{ID: "audit-1", Project: scope.Project}
	if err := local.finishAIRun(ctx, "op-after-case-readerr", &afterOp); err == nil {
		t.Fatalf("expected getCase error in op.After")
	}
	_ = os.RemoveAll(filepath.Join(caseDir, c.ID+".json"))
	// Restore case on disk:
	_ = writeItem(caseDir, c.ID, c)

	// e) Conflict: current on disk != After and != Before -> ErrConflict
	conflictC := c
	conflictC.Title = "Conflicting title from concurrent write"
	_ = writeItem(caseDir, c.ID, conflictC)
	afterOp = baseOp
	afterOp.After = &afterCase
	afterOp.Before = &c
	afterOp.CaseAudit = &AuditEvent{ID: "audit-1", Project: scope.Project}
	if err := local.finishAIRun(ctx, "op-after-conflict", &afterOp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict in op.After")
	}
	_ = writeItem(caseDir, c.ID, c)

	// f) writeItem error on cases (caseDir chmod 0500)
	modifiedAfter := c
	modifiedAfter.Title = "Updated title"
	afterOp = baseOp
	afterOp.After = &modifiedAfter
	afterOp.Before = &c
	afterOp.CaseAudit = &AuditEvent{ID: "audit-1", Project: scope.Project}
	if err := os.Chmod(caseDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := local.finishAIRun(ctx, "op-after-werr", &afterOp); err == nil {
		t.Fatalf("expected writeItem error on cases in op.After")
	}
	_ = os.Chmod(caseDir, 0700)

	// g) auditRecorded error on CaseAudit (line 267):
	// audit.jsonl has line 1 matching runAudit completely, line 2 corrupt for CaseAudit
	runAuditForFile := core.AuditForRun(validRun)
	runAuditForFile.Project = scope.Project
	line1, _ := json.Marshal(runAuditForFile)
	auditData := string(line1) + "\n{\n"
	_ = os.WriteFile(auditPath, []byte(auditData), 0600)
	afterOp = baseOp
	afterOp.After = &c
	afterOp.Before = &c
	afterOp.CaseAudit = &AuditEvent{ID: "audit-case-rec-err", Project: scope.Project}
	if err := local.finishAIRun(ctx, "op-case-rec-fail", &afterOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from corrupt JSON in auditRecorded for CaseAudit: %v", err)
	}
	_ = os.Remove(auditPath)
	_ = os.Remove(filepath.Join(runsDir, validRun.ID+".json"))

	// h) auditMutation error on runAudit (line 207) and CaseAudit (line 267)
	failingStoreRun := &failingAuditStore{failRun: true}
	storeFailRun := *store
	storeFailRun.audit = failingStoreRun
	localFailRun := LocalAIStore{Store: &storeFailRun, Scope: scope}
	testOp = baseOp
	testOp.CaseContextDigest = ""
	testOp.Draft.CaseID = ""
	testOp.Run.CaseID = ""
	testOp.Run.ID = "run-mut-fail"
	testOp.Receipt.ResourceID = "run-mut-fail"
	if err := localFailRun.finishAIRun(ctx, "op-audit-mut-fail", &testOp); err == nil {
		t.Fatalf("expected error from auditMutation on runAudit")
	}
	_ = os.Remove(filepath.Join(runsDir, "run-mut-fail.json"))

	failingStoreCase := &failingAuditStore{failCase: true}
	storeFailCase := *store
	storeFailCase.audit = failingStoreCase
	localFailCase := LocalAIStore{Store: &storeFailCase, Scope: scope}
	afterOp = baseOp
	afterOp.After = &c
	afterOp.Before = &c
	afterOp.CaseAudit = &AuditEvent{ID: "audit-case-mut-err", Project: scope.Project}
	afterOp.Run.ID = "run-case-mut-fail"
	afterOp.Receipt.ResourceID = "run-case-mut-fail"
	if err := localFailCase.finishAIRun(ctx, "op-case-mut-fail", &afterOp); err == nil {
		t.Fatalf("expected error from auditMutation on CaseAudit")
	}
	_ = os.Remove(filepath.Join(runsDir, "run-case-mut-fail.json"))

	// 9. Final writeItem error on ai-confirm-operations -> ErrOutcomeUncertain
	defer func() { _ = os.Chmod(confirmDir, 0700) }()
	if err := os.Chmod(confirmDir, 0500); err != nil {
		t.Fatal(err)
	}
	testOp = baseOp
	testOp.Run.ID = "run-final-unc"
	testOp.Receipt.ResourceID = "run-final-unc"
	testOp.Draft.CaseID = ""
	testOp.Run.CaseID = ""
	testOp.CaseContextDigest = ""
	if err := local.finishAIRun(ctx, "op-final-unc", &testOp); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from final writeItem: %v", err)
	}
	_ = os.Chmod(confirmDir, 0700)
	_ = os.Remove(filepath.Join(runsDir, "run-final-unc.json"))
}

type failingAuditStore struct {
	failRun  bool
	failCase bool
}

func (f *failingAuditStore) Append(ctx context.Context, e AuditEvent) error {
	if f.failRun && strings.HasPrefix(e.Action, "run-") {
		return errors.New("fail run audit")
	}
	if f.failCase && !strings.HasPrefix(e.Action, "run-") {
		return errors.New("fail case audit")
	}
	return nil
}
