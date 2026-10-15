package detective

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// Not parallel: modifies package seams and filesystem.
func TestCoverage100_AIQuery_ConfirmQuery_ValidationAndReplay(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	confirmDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(confirmDir, 0700)

	validDraft := ai.QueryDraft{
		ID:    "orders/summary",
		Title: "Order Summary",
		Type:  "SQL",
		Text:  "SELECT 1",
	}
	validProposal := ai.Proposal{
		ID:    "prop-q1",
		Scope: scope,
		Draft: ai.Draft{Kind: ai.KindQuery, Query: &validDraft},
	}
	validCmd := ai.Command{
		Scope:       scope,
		OperationID: "op-q1",
		Proposal:    validProposal,
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-draft-query",
			ResourceID:    validProposal.ID,
			PayloadDigest: strings.Repeat("a", 64),
		},
	}

	// 1. Validation checks in ConfirmQuery
	invalidStores := []LocalAIStore{
		{Store: nil, Scope: scope},
		{Store: store, Scope: ai.Scope{Key: "other"}},
	}
	for i, badStore := range invalidStores {
		if _, err := badStore.ConfirmQuery(ctx, validCmd); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidStores[%d]: expected ErrInvalid, got: %v", i, err)
		}
	}

	invalidCmds := []ai.Command{
		func() ai.Command { c := validCmd; c.Scope.Key = "wrong"; return c }(),
		func() ai.Command { c := validCmd; c.Proposal.Scope.Key = "wrong"; return c }(),
		func() ai.Command { c := validCmd; c.Proposal.Draft.Kind = ai.KindCheck; return c }(),
		func() ai.Command { c := validCmd; c.Proposal.Draft.Query = nil; return c }(),
		func() ai.Command { c := validCmd; c.OperationID = "../bad"; return c }(),
		func() ai.Command { c := validCmd; c.Binding.Project = "wrong"; return c }(),
		func() ai.Command { c := validCmd; c.Binding.Actor = "wrong"; return c }(),
		func() ai.Command { c := validCmd; c.Binding.Action = "wrong"; return c }(),
		func() ai.Command { c := validCmd; c.Binding.ResourceID = "wrong"; return c }(),
		func() ai.Command { c := validCmd; c.Binding.PayloadDigest = "short"; return c }(),
	}
	for i, badCmd := range invalidCmds {
		if _, err := local.ConfirmQuery(ctx, badCmd); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidCmds[%d]: expected ErrInvalid, got: %v", i, err)
		}
	}

	// 2. Replay checks:
	// a) safeReadFile error (directory in place of file)
	_ = os.MkdirAll(filepath.Join(confirmDir, "op-dir.json"), 0700)
	dirCmd := validCmd
	dirCmd.OperationID = "op-dir"
	if _, err := local.ConfirmQuery(ctx, dirCmd); err == nil {
		t.Fatalf("expected error from safeReadFile on directory")
	}
	_ = os.RemoveAll(filepath.Join(confirmDir, "op-dir.json"))

	// b) Corrupt JSON in prior op
	_ = os.WriteFile(filepath.Join(confirmDir, "op-corrupt.json"), []byte("{"), 0600)
	corruptCmd := validCmd
	corruptCmd.OperationID = "op-corrupt"
	if _, err := local.ConfirmQuery(ctx, corruptCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on corrupt JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-corrupt.json"))

	// c) prior.Kind != "query"
	_ = writeItem(confirmDir, "op-wrong-kind", aiQueryOperation{Kind: "other"})
	wrongKindCmd := validCmd
	wrongKindCmd.OperationID = "op-wrong-kind"
	if _, err := local.ConfirmQuery(ctx, wrongKindCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on prior.Kind != query: %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-wrong-kind.json"))

	// d) !aiReceiptMatchesCommand
	_ = writeItem(confirmDir, "op-mismatch-receipt", aiQueryOperation{Kind: "query", Receipt: ai.Receipt{OperationID: "other"}})
	mismatchCmd := validCmd
	mismatchCmd.OperationID = "op-mismatch-receipt"
	if _, err := local.ConfirmQuery(ctx, mismatchCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on receipt mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-mismatch-receipt.json"))

	// e) prior.Done == false -> calls finishAIQuery (which returns error if seam fails)
	origSave := saveProjectQuery
	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return nil, errors.New("save query failed in replay")
	}
	matchingReceipt := ai.Receipt{
		OperationID:   "op-replay-notdone",
		ScopeKey:      scope.Key,
		Project:       scope.Project,
		Actor:         scope.Actor,
		ProposalID:    validProposal.ID,
		PayloadDigest: validCmd.Binding.PayloadDigest,
	}
	replayReq := dto.SaveQueryRequest{
		ProjectRef:  dto.ProjectRef{StoreID: api.LocalStoreID, ProjectID: scope.Project},
		OperationID: "op-replay-notdone",
	}
	_ = writeItem(confirmDir, "op-replay-notdone", aiQueryOperation{
		Kind:    "query",
		Receipt: matchingReceipt,
		Request: replayReq,
		Done:    false,
	})
	replayCmd := validCmd
	replayCmd.OperationID = "op-replay-notdone"
	if _, err := local.ConfirmQuery(ctx, replayCmd); err == nil {
		t.Fatalf("expected error from finishAIQuery in replay")
	}
	saveProjectQuery = origSave
	_ = os.Remove(filepath.Join(confirmDir, "op-replay-notdone.json"))

	// f) prior.Done == true -> returns prior.Receipt
	matchingReceipt.OperationID = "op-replay-done"
	_ = writeItem(confirmDir, "op-replay-done", aiQueryOperation{
		Kind:    "query",
		Receipt: matchingReceipt,
		Done:    true,
	})
	doneCmd := validCmd
	doneCmd.OperationID = "op-replay-done"
	rcpt, err := local.ConfirmQuery(ctx, doneCmd)
	if err != nil {
		t.Fatalf("unexpected error replaying done op: %v", err)
	}
	if rcpt.OperationID != "op-replay-done" {
		t.Fatalf("expected op-replay-done receipt, got: %s", rcpt.OperationID)
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-replay-done.json"))

	// 3. verifyAIBasisLocked error
	basisCmd := validCmd
	basisCmd.Proposal.Basis.CatalogRevision = "rev-xyz"
	if _, err := local.ConfirmQuery(ctx, basisCmd); !errors.Is(err, ai.ErrUnauthorized) {
		t.Fatalf("expected ai.ErrUnauthorized from verifyAIBasisLocked: %v", err)
	}

	// 4. aiQueryRequest error
	badDraftCmd := validCmd
	badDraftCmd.OperationID = "op-bad-draft"
	badDraftCmd.Proposal.Draft.Query = &ai.QueryDraft{ID: "../invalid"}
	if _, err := local.ConfirmQuery(ctx, badDraftCmd); err == nil {
		t.Fatalf("expected error from aiQueryRequest on invalid query draft")
	}

	// 5. writeItem error on ai-confirm-operations (chmod 0500)
	origBranches := localProjectBranches
	origScope := secureProjectMutationScope
	defer func() {
		localProjectBranches = origBranches
		secureProjectMutationScope = origScope
		saveProjectQuery = origSave
	}()
	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return &api.ProjectBranches{
			CurrentBranch: "main",
			Branches:      []api.ProjectBranch{{Name: "main", Head: strings.Repeat("1", 40)}},
		}, nil
	}
	secureProjectMutationScope = func(ref dto.ProjectRef, branch, action string) (dto.OperationScope, error) {
		return dto.OperationScope{ActorID: "alice", ProjectID: scope.Project}, nil
	}
	writeErrCmd := validCmd
	writeErrCmd.OperationID = "op-werr-q"
	if err := os.Chmod(confirmDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ConfirmQuery(ctx, writeErrCmd); err == nil {
		t.Fatalf("expected error writing op in read-only confirmDir")
	}
	_ = os.Chmod(confirmDir, 0700)

	// 6. finishAIQuery error
	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return nil, errors.New("saveProjectQuery exploded")
	}
	finishErrCmd := validCmd
	finishErrCmd.OperationID = "op-finish-err"
	if _, err := local.ConfirmQuery(ctx, finishErrCmd); err == nil {
		t.Fatalf("expected error from finishAIQuery")
	}
	_ = os.Remove(filepath.Join(confirmDir, "op-finish-err.json"))

	// 7. Happy path
	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return &dto.SaveQueryResponse{Revision: "rev-123"}, nil
	}
	happyCmd := validCmd
	happyCmd.OperationID = "op-happy-q"
	rcpt, err = local.ConfirmQuery(ctx, happyCmd)
	if err != nil {
		t.Fatalf("unexpected error in ConfirmQuery happy path: %v", err)
	}
	if rcpt.OperationID != "op-happy-q" || rcpt.ResourceID != validDraft.ID {
		t.Fatalf("unexpected receipt: %+v", rcpt)
	}
}

// Not parallel: modifies package seams.
func TestCoverage100_AIQuery_AIQueryRequest_Branches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	origBranches := localProjectBranches
	defer func() { localProjectBranches = origBranches }()

	cmd := ai.Command{
		Scope:       scope,
		OperationID: "op-test",
		Proposal: ai.Proposal{
			ID:    "prop-1",
			Scope: scope,
			Draft: ai.Draft{
				Kind: ai.KindQuery,
				Query: &ai.QueryDraft{
					ID:    "test-query",
					Title: "Title",
					Type:  "SQL",
					Text:  "SELECT 1",
				},
			},
		},
	}

	// 1. draft validation failures
	invalidDrafts := []*ai.QueryDraft{
		nil,
		{ID: "../bad", Title: "T", Text: "SELECT 1"},
		{ID: "valid", Title: "", Text: "SELECT 1"},
		{ID: "valid", Title: "T", Text: ""},
		func() *ai.QueryDraft {
			d := &ai.QueryDraft{ID: "valid", Title: "T", Text: "SELECT 1"}
			for i := 0; i < 65; i++ {
				d.Inputs = append(d.Inputs, core.Input{Name: "p", Type: investigation.ValueTypeString, Cardinality: "scalar"})
			}
			return d
		}(),
	}
	for i, badDraft := range invalidDrafts {
		badCmd := cmd
		badCmd.Proposal.Draft.Query = badDraft
		if _, err := local.aiQueryRequest(ctx, badCmd); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidDrafts[%d]: expected ErrInvalid, got: %v", i, err)
		}
	}

	// 2. localProjectBranches error
	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return nil, errors.New("branches lookup failed")
	}
	if _, err := local.aiQueryRequest(ctx, cmd); err == nil {
		t.Fatalf("expected error from localProjectBranches")
	}

	// 3. branches == nil or branches.CurrentBranch == ""
	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return nil, nil
	}
	if _, err := local.aiQueryRequest(ctx, cmd); !errors.Is(err, dto.ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability for nil branches: %v", err)
	}

	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return &api.ProjectBranches{CurrentBranch: ""}, nil
	}
	if _, err := local.aiQueryRequest(ctx, cmd); !errors.Is(err, dto.ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability for empty CurrentBranch: %v", err)
	}

	// 4. head == "" (no matching branch in list)
	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return &api.ProjectBranches{
			CurrentBranch: "main",
			Branches:      []api.ProjectBranch{{Name: "other", Head: "123"}},
		}, nil
	}
	if _, err := local.aiQueryRequest(ctx, cmd); !errors.Is(err, dto.ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability for branch without head: %v", err)
	}

	// 5. Input validation loops:
	validBranches := &api.ProjectBranches{
		CurrentBranch: "main",
		Branches:      []api.ProjectBranch{{Name: "main", Head: strings.Repeat("1", 40)}},
	}
	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return validBranches, nil
	}

	invalidInputs := []core.Input{
		{Name: "123invalid", Type: investigation.ValueTypeString, Cardinality: "scalar"},
		{Name: "valid", Type: "unknown_type", Cardinality: "scalar"},
		{Name: "valid", Type: investigation.ValueTypeString, Cardinality: "range"},
		{Name: "valid", Type: investigation.ValueTypeString, Cardinality: "unknown_cardinality"},
	}
	for i, badInput := range invalidInputs {
		badCmd := cmd
		badCmd.Proposal.Draft.Query = &ai.QueryDraft{
			ID:     "test-query",
			Title:  "Title",
			Text:   "SELECT 1",
			Inputs: []core.Input{badInput},
		}
		if _, err := local.aiQueryRequest(ctx, badCmd); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidInputs[%d]: expected ErrInvalid, got: %v", i, err)
		}
	}

	// 6. Valid inputs destructuring (scalar and set) & folder path split
	// a) draft.ID with folder: "reports/monthly/sales"
	folderCmd := cmd
	folderCmd.Proposal.Draft.Query = &ai.QueryDraft{
		ID:    "reports/monthly/sales",
		Title: "Sales",
		Type:  "SQL",
		Text:  "SELECT 1",
		Inputs: []core.Input{
			{Name: "org_id", Type: investigation.ValueTypeString, Cardinality: "scalar", Required: true},
			{Name: "tags", Type: investigation.ValueTypeString, Cardinality: "set", Required: false},
		},
		ExpectedRevision: "rev-1",
	}
	req, err := local.aiQueryRequest(ctx, folderCmd)
	if err != nil {
		t.Fatalf("unexpected error from aiQueryRequest: %v", err)
	}
	if req.Query.FolderPath != "reports/monthly" || req.Query.ID != "sales" {
		t.Fatalf("unexpected folder splitting: FolderPath=%s, ID=%s", req.Query.FolderPath, req.Query.ID)
	}
	if len(req.Query.Parameters) != 2 || req.Query.Parameters[0].IsMultiValue || !req.Query.Parameters[1].IsMultiValue {
		t.Fatalf("unexpected parameters mapping: %+v", req.Query.Parameters)
	}
	if req.IfNoneMatch || req.IfMatch != "rev-1" {
		t.Fatalf("unexpected IfMatch/IfNoneMatch: %+v", req)
	}

	// b) draft.ID without folder: "sales" -> FolderPath == datatug.RootSharedFolderName
	rootCmd := cmd
	rootCmd.Proposal.Draft.Query = &ai.QueryDraft{
		ID:    "sales",
		Title: "Sales",
		Type:  "SQL",
		Text:  "SELECT 1",
	}
	req2, err := local.aiQueryRequest(ctx, rootCmd)
	if err != nil {
		t.Fatalf("unexpected error from aiQueryRequest: %v", err)
	}
	if req2.Query.FolderPath != datatug.RootSharedFolderName || req2.Query.ID != "sales" {
		t.Fatalf("unexpected root folder: FolderPath=%s, ID=%s", req2.Query.FolderPath, req2.Query.ID)
	}
	if !req2.IfNoneMatch || req2.IfMatch != "" {
		t.Fatalf("expected IfNoneMatch true for empty ExpectedRevision")
	}

	// 7. request.Validate() error (e.g. invalid CurrentBranch with surrounding whitespace)
	invalidHeadBranches := &api.ProjectBranches{
		CurrentBranch: "  main  ",
		Branches:      []api.ProjectBranch{{Name: "  main  ", Head: strings.Repeat("1", 40)}},
	}
	localProjectBranches = func(ctx context.Context, ref dto.ProjectRef) (*api.ProjectBranches, error) {
		return invalidHeadBranches, nil
	}
	if _, err := local.aiQueryRequest(ctx, rootCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when request.Validate() fails: %v", err)
	}
}

// Not parallel: modifies package seams and filesystem.
func TestCoverage100_AIQuery_FinishAIQuery_Branches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	confirmDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(confirmDir, 0700)

	baseReq := dto.SaveQueryRequest{
		ProjectRef:  dto.ProjectRef{StoreID: api.LocalStoreID, ProjectID: scope.Project},
		Branch:      "main",
		OperationID: "op-fin-q",
	}
	baseRcpt := ai.Receipt{
		OperationID: "op-fin-q",
		ScopeKey:    scope.Key,
		Project:     scope.Project,
		Actor:       scope.Actor,
		At:          time.Now().UTC(),
	}
	baseOp := aiQueryOperation{
		Kind:    "query",
		Receipt: baseRcpt,
		Request: baseReq,
	}

	// 1. Validation in finishAIQuery
	invalidOps := []aiQueryOperation{
		func() aiQueryOperation { o := baseOp; o.Kind = "other"; return o }(),
		func() aiQueryOperation { o := baseOp; o.Request.ProjectID = "wrong"; return o }(),
		func() aiQueryOperation { o := baseOp; o.Request.StoreID = "wrong"; return o }(),
		func() aiQueryOperation { o := baseOp; o.Request.OperationID = "wrong"; return o }(),
		func() aiQueryOperation { o := baseOp; o.Receipt.OperationID = "wrong"; return o }(),
	}
	for i, bad := range invalidOps {
		if err := local.finishAIQuery(ctx, "op-fin-q", &bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidOps[%d]: expected ErrInvalid, got: %v", i, err)
		}
	}

	// 2. secureProjectMutationScope error
	origScope := secureProjectMutationScope
	origSave := saveProjectQuery
	defer func() {
		secureProjectMutationScope = origScope
		saveProjectQuery = origSave
	}()
	secureProjectMutationScope = func(ref dto.ProjectRef, branch, action string) (dto.OperationScope, error) {
		return dto.OperationScope{}, errors.New("mutation scope failed")
	}
	opTest := baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); err == nil {
		t.Fatalf("expected error from secureProjectMutationScope")
	}

	// 3. scope.ActorID != s.Scope.Actor -> ErrConflict
	secureProjectMutationScope = func(ref dto.ProjectRef, branch, action string) (dto.OperationScope, error) {
		return dto.OperationScope{ActorID: "bob"}, nil
	}
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on actor mismatch: %v", err)
	}

	// 4. saveProjectQuery error
	secureProjectMutationScope = func(ref dto.ProjectRef, branch, action string) (dto.OperationScope, error) {
		return dto.OperationScope{ActorID: "alice"}, nil
	}
	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return nil, errors.New("save query exploded")
	}
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); err == nil {
		t.Fatalf("expected error from saveProjectQuery")
	}

	// 5. result == nil or result.Revision == "" -> ErrOutcomeUncertain
	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return nil, nil
	}
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain when result == nil: %v", err)
	}

	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return &dto.SaveQueryResponse{Revision: ""}, nil
	}
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain when result.Revision == '': %v", err)
	}

	// 6. auditRecorded error (corrupt audit file)
	saveProjectQuery = func(ctx context.Context, scope dto.OperationScope, request dto.SaveQueryRequest) (*dto.SaveQueryResponse, error) {
		return &dto.SaveQueryResponse{Revision: "rev-ok"}, nil
	}
	auditPath := filepath.Join(store.privateDir, "audit.jsonl")
	_ = os.WriteFile(auditPath, []byte("{\n"), 0600)
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from corrupt audit file: %v", err)
	}
	_ = os.Remove(auditPath)

	// 7. auditMutation error
	failAudit := &failingAuditStore{failCase: true}
	storeFail := *store
	storeFail.audit = failAudit
	localFail := LocalAIStore{Store: &storeFail, Scope: scope}
	opTest = baseOp
	if err := localFail.finishAIQuery(ctx, "op-fin-q", &opTest); err == nil {
		t.Fatalf("expected auditMutation error")
	}

	// 8. writeItem error on ai-confirm-operations -> ErrOutcomeUncertain
	if err := os.Chmod(confirmDir, 0500); err != nil {
		t.Fatal(err)
	}
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain from writeItem: %v", err)
	}
	_ = os.Chmod(confirmDir, 0700)

	// 9. Happy path: op.Done set to true and operation saved
	opTest = baseOp
	if err := local.finishAIQuery(ctx, "op-fin-q", &opTest); err != nil {
		t.Fatalf("unexpected error in finishAIQuery happy path: %v", err)
	}
	if !opTest.Done {
		t.Fatalf("expected op.Done to be true")
	}
	// Verify auditRecorded returns true when run again
	opTest2 := baseOp
	opTest2.Receipt.OperationID = "op-fin-q2"
	opTest2.Request.OperationID = "op-fin-q2"
	// pre-record audit
	_ = store.auditMutation(ctx, AuditEvent{ID: "op-fin-q2", At: opTest2.Receipt.At, Actor: scope.Actor, Project: scope.Project, Action: "ai-confirm-draft-query"})
	if err := local.finishAIQuery(ctx, "op-fin-q2", &opTest2); err != nil {
		t.Fatalf("unexpected error when audit already recorded: %v", err)
	}
}
