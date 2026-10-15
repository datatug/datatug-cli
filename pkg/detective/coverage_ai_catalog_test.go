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

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

// Not parallel: tests filesystem modifications and permissions on store dirs.
func TestCoverage100_AICatalog_Confirm_Validation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	check := Check{ID: "chk-1", Title: "Check 1"}
	proposal := ai.Proposal{ID: "prop-chk", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: &check}}
	binding := core.OperationBinding{
		Project:       scope.Key,
		Actor:         scope.Actor,
		Action:        "ai-confirm-" + ai.KindCheck,
		ResourceID:    proposal.ID,
		PayloadDigest: strings.Repeat("a", 64),
	}
	validCmd := ai.Command{Scope: scope, OperationID: "op-valid", Proposal: proposal, Binding: binding}

	// 1. ConfirmCatalog parameter validations
	// Nil store
	if _, err := (LocalAIStore{Store: nil, Scope: scope}).ConfirmCatalog(ctx, validCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil store: %v", err)
	}
	// Command scope mismatch
	badCmd := validCmd
	badCmd.Scope.Key = "other"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for scope mismatch: %v", err)
	}
	// Proposal scope mismatch
	badCmd = validCmd
	badCmd.Proposal.Scope.Key = "other"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for proposal scope mismatch: %v", err)
	}
	// Invalid operation ID slug
	badCmd = validCmd
	badCmd.OperationID = "../bad"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad op ID: %v", err)
	}
	// Invalid proposal ID slug
	badCmd = validCmd
	badCmd.Proposal.ID = "../bad"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad proposal ID: %v", err)
	}
	// Binding project mismatch
	badCmd = validCmd
	badCmd.Binding.Project = "other"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for binding project mismatch: %v", err)
	}
	// Binding actor mismatch
	badCmd = validCmd
	badCmd.Binding.Actor = "bob"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for binding actor mismatch: %v", err)
	}
	// Binding resource ID mismatch (must match Proposal.ID)
	badCmd = validCmd
	badCmd.Binding.ResourceID = "other"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for binding resourceID mismatch: %v", err)
	}
	// Binding payload digest length != 64
	badCmd = validCmd
	badCmd.Binding.PayloadDigest = "tooshort"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for short payload digest: %v", err)
	}
	// Unsupported kind (e.g. "unknown" or "case")
	badCmd = validCmd
	badCmd.Proposal.Draft.Kind = "case"
	badCmd.Binding.Action = "ai-confirm-case"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for unsupported kind: %v", err)
	}
	// Binding action mismatch (not matching "ai-confirm-" + kind)
	badCmd = validCmd
	badCmd.Binding.Action = "wrong-action"
	if _, err := local.ConfirmCatalog(ctx, badCmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for action mismatch: %v", err)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AICatalog_Confirm_Replay_And_Errors(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)

	check := Check{
		ID:    "chk-replay",
		Title: "Check Replay",
		Steps: []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	proposal := ai.Proposal{ID: "prop-chk", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: &check}}
	binding := core.OperationBinding{
		Project:       scope.Key,
		Actor:         scope.Actor,
		Action:        "ai-confirm-" + ai.KindCheck,
		ResourceID:    proposal.ID,
		PayloadDigest: strings.Repeat("a", 64),
	}
	cmd := ai.Command{Scope: scope, OperationID: "op-chk-replay", Proposal: proposal, Binding: binding}

	opsDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(opsDir, 0700)

	// 1. reconcilePendingActions error (e.g. corrupt file in ai-confirm-operations)
	_ = os.WriteFile(filepath.Join(opsDir, "corrupt-pending.json"), []byte("{"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(opsDir, "corrupt-pending.json"))

	// 2. safeReadFile error other than ErrNotExist (directory)
	_ = os.MkdirAll(filepath.Join(opsDir, cmd.OperationID+".json"), 0700)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(opsDir, cmd.OperationID+".json"))

	// 3. Existing prior op with mismatched kind or receipt
	priorReceipt := ai.Receipt{
		OperationID:   cmd.OperationID,
		ScopeKey:      scope.Key,
		Project:       scope.Project,
		Actor:         scope.Actor,
		ProposalID:    cmd.Proposal.ID,
		PayloadDigest: cmd.Binding.PayloadDigest,
		ResourceID:    check.ID,
	}
	// Kind mismatch
	_ = writeItem(opsDir, cmd.OperationID, aiCatalogOperation{Kind: ai.KindValidator, Receipt: priorReceipt})
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on kind mismatch: %v", err)
	}

	// 4. Existing prior op !prior.Done: finishAICatalog error (e.g. corrupt audit)
	auditPath := filepath.Join(store.privateDir, "audit.jsonl")
	_ = os.WriteFile(auditPath, []byte("{\n"), 0600)
	_ = writeItem(opsDir, cmd.OperationID, aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: priorReceipt,
		After:   json.RawMessage(`{"id":"chk-replay","title":"Check Replay","version":1,"steps":[{"id":"s1","kind":"query","queryId":"valid-q"}]}`),
		Done:    false,
	})
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected error from finishAICatalog when audit is corrupt")
	}
	_ = os.Remove(auditPath)

	// 4b. Existing prior op with Done == true -> idempotent replay
	_ = writeItem(opsDir, cmd.OperationID, aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: priorReceipt,
		After:   json.RawMessage(`{"id":"chk-replay","title":"Check Replay","version":1,"steps":[{"id":"s1","kind":"query","queryId":"valid-q"}]}`),
		Done:    true,
	})
	if replayReceipt, err := local.ConfirmCatalog(ctx, cmd); err != nil || replayReceipt.OperationID != cmd.OperationID {
		t.Fatalf("expected replay of done catalog operation: %v, %+v", err, replayReceipt)
	}
	_ = os.Remove(filepath.Join(opsDir, cmd.OperationID+".json"))

	// 5. verifyAIBasisLocked error
	cmdWithBasis := cmd
	cmdWithBasis.Proposal.Basis = ai.Snapshot{CatalogRevision: "rev-1"}
	// With nil BasisVerifier, returns ai.ErrUnauthorized
	if _, err := local.ConfirmCatalog(ctx, cmdWithBasis); !errors.Is(err, ai.ErrUnauthorized) {
		t.Fatalf("expected ai.ErrUnauthorized: %v", err)
	}

	// 6. writeItem error on ai-confirm-operations (chmod 0500)
	checkWerr := Check{
		ID:    "chk-werr",
		Title: "Check Werr",
		Steps: []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	propWerr := ai.Proposal{ID: "prop-werr", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: &checkWerr}}
	bindingWerr := core.OperationBinding{
		Project:       scope.Key,
		Actor:         scope.Actor,
		Action:        "ai-confirm-" + ai.KindCheck,
		ResourceID:    propWerr.ID,
		PayloadDigest: strings.Repeat("e", 64),
	}
	cmdWerr := ai.Command{Scope: scope, OperationID: "op-chk-werr", Proposal: propWerr, Binding: bindingWerr}
	if err := os.Chmod(opsDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := local.ConfirmCatalog(ctx, cmdWerr); err == nil {
		t.Fatalf("expected writeItem error on read-only opsDir")
	}
	_ = os.Chmod(opsDir, 0700)
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AICatalog_Prepare_Check(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	// 1. Draft.Check is nil -> ErrInvalid
	cmd := ai.Command{
		Scope:       scope,
		OperationID: "op-check-nil",
		Proposal:    ai.Proposal{ID: "prop-nil", Scope: scope, Draft: ai.Draft{Kind: ai.KindCheck, Check: nil}},
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-" + ai.KindCheck,
			ResourceID:    "prop-nil",
			PayloadDigest: strings.Repeat("a", 64),
		},
	}
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Check: %v", err)
	}

	// 2. ValidateCheck error (e.g. invalid ID slug)
	badCheck := Check{ID: "../bad", Title: "Bad Check"}
	cmd.Proposal.Draft.Check = &badCheck
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected ValidateCheck error")
	}

	// 3. Query resolution in Check steps:
	// a) resolveQueryRef error (missing query)
	checkWithQuery := Check{
		ID:    "chk-q-err",
		Title: "Check with Query",
		Steps: []Step{{ID: "s1", Kind: "query", QueryID: "missing-q"}},
	}
	cmd.Proposal.Draft.Check = &checkWithQuery
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing query: %v", err)
	}

	// Save query for valid resolution
	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)

	validCheckWithQuery := Check{
		ID:    "chk-q-ok",
		Title: "Check with Query OK",
		Steps: []Step{{ID: "s1", Kind: "query", QueryID: "valid-q"}},
	}
	cmd.Proposal.Draft.Check = &validCheckWithQuery

	// 4. safeReadFile error on checks (directory)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	_ = os.MkdirAll(filepath.Join(checksDir, validCheckWithQuery.ID+".json"), 0700)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(checksDir, validCheckWithQuery.ID+".json"))

	// 5. Existing check: corrupt JSON -> ErrInvalid
	_ = os.WriteFile(filepath.Join(checksDir, validCheckWithQuery.ID+".json"), []byte("{"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt check JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(checksDir, validCheckWithQuery.ID+".json"))

	// 6. New check with Version != 0 -> ErrConflict
	badVerCheck := validCheckWithQuery
	badVerCheck.Version = 2
	cmd.Proposal.Draft.Check = &badVerCheck
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new check with version != 0: %v", err)
	}
	cmd.Proposal.Draft.Check = &validCheckWithQuery

	// 7. readItems[Check] error (bad slug)
	_ = os.WriteFile(filepath.Join(checksDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[Check] error")
	}
	_ = os.Remove(filepath.Join(checksDir, "bad..slug.json"))

	// 8. Max capacity exceeded
	for i := 0; i < maxItems; i++ {
		id := fmt.Sprintf("chk-cap-%03d", i)
		_ = writeItem(checksDir, id, Check{ID: id, Title: id, Version: 1})
	}
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity on max checks: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		_ = os.Remove(filepath.Join(checksDir, fmt.Sprintf("chk-cap-%03d.json", i)))
	}

	// 9. readItems[PublishedValidator] error
	valDir := store.definitionDir("validators")
	_ = os.MkdirAll(valDir, 0700)
	_ = os.WriteFile(filepath.Join(valDir, "bad..val.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[PublishedValidator] error")
	}
	_ = os.Remove(filepath.Join(valDir, "bad..val.json"))

	// 10. readItems[QueryValidators] error
	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	_ = os.WriteFile(filepath.Join(qvDir, "bad..qv.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[QueryValidators] error")
	}
	_ = os.Remove(filepath.Join(qvDir, "bad..qv.json"))

	// 11. validateGraph error (e.g. check dependency cycle)
	_ = writeItem(checksDir, "chk-cycle", Check{
		ID: "chk-cycle", Title: "Cycle", Version: 1,
		Steps: []Step{{ID: "s1", Kind: "check", CheckID: "chk-bad-graph"}},
	})
	checkBadGraph := Check{
		ID:    "chk-bad-graph",
		Title: "Bad Graph",
		Steps: []Step{{ID: "s1", Kind: "check", CheckID: "chk-cycle"}},
	}
	cmd.Proposal.Draft.Check = &checkBadGraph
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected validateGraph error")
	}
	_ = os.Remove(filepath.Join(checksDir, "chk-cycle.json"))

	// 12. ensureQueryRefs error: create a query-validator referencing a missing query
	_ = writeItem(qvDir, "q-missing", QueryValidators{
		QueryID: "q-missing",
		Version: 1,
		Validators: []Validator{{
			ID:            "v1",
			Kind:          "non-null",
			Scope:         "row",
			Severity:      "error",
			OnUnavailable: "fail",
			Field:         "f1",
		}},
	})
	cmd.Proposal.Draft.Check = &validCheckWithQuery
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from ensureQueryRefs: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, "q-missing.json"))

	// 13. Happy path: create new check
	receipt, err := local.ConfirmCatalog(ctx, cmd)
	if err != nil || receipt.ResourceID != validCheckWithQuery.ID {
		t.Fatalf("expected success creating check: %v, %+v", err, receipt)
	}

	// Write existing validator to cover vm[v.ID] = v
	_ = writeItem(valDir, "val-existing", PublishedValidator{
		ID:      "val-existing",
		Title:   "Val Existing",
		Version: 1,
		Validator: Validator{
			ID:            "val-existing",
			Kind:          "non-null",
			Scope:         "row",
			Severity:      "error",
			OnUnavailable: "fail",
			Field:         "f1",
		},
	})

	// 14. Happy path: update existing check
	existingCheck := validCheckWithQuery
	existingCheck.Version = 1
	existingCheck.Title = "Updated title"
	updateCmd := cmd
	updateCmd.OperationID = "op-check-update"
	updateCmd.Proposal.Draft.Check = &existingCheck
	updateCmd.Binding.PayloadDigest = strings.Repeat("b", 64)
	receipt, err = local.ConfirmCatalog(ctx, updateCmd)
	if err != nil || receipt.ResourceID != existingCheck.ID {
		t.Fatalf("expected success updating check: %v, %+v", err, receipt)
	}

	// 15. Update existing check version mismatch -> ErrConflict
	badVerCheck = existingCheck
	badVerCheck.Version = 99
	badVerCmd := updateCmd
	badVerCmd.OperationID = "op-check-bad-ver"
	badVerCmd.Proposal.Draft.Check = &badVerCheck
	badVerCmd.Binding.PayloadDigest = strings.Repeat("c", 64)
	if _, err = local.ConfirmCatalog(ctx, badVerCmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on check version mismatch: %v", err)
	}

	// 16. prepareAICatalog invalid resourceID/version
	if _, _, _, _, err = local.prepareAICatalog(ai.Command{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid from prepareAICatalog on empty command: %v", err)
	}

	// 17. prepareAICatalog marshal error
	existingCheck.Version = 2
	updateCmd.Proposal.Draft.Check = &existingCheck
	aiCatalogJSONMarshal = func(v any) ([]byte, error) { return nil, errors.New("marshal error") }
	if _, _, _, _, err = local.prepareAICatalog(updateCmd); err == nil {
		t.Fatalf("expected error from json.Marshal in prepareAICatalog")
	}
	aiCatalogJSONMarshal = json.Marshal
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AICatalog_Prepare_Validator(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	valDir := store.definitionDir("validators")
	_ = os.MkdirAll(valDir, 0700)

	// 1. Validator is nil -> ErrInvalid
	cmd := ai.Command{
		Scope:       scope,
		OperationID: "op-val-nil",
		Proposal:    ai.Proposal{ID: "prop-val-nil", Scope: scope, Draft: ai.Draft{Kind: ai.KindValidator, Validator: nil}},
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-" + ai.KindValidator,
			ResourceID:    "prop-val-nil",
			PayloadDigest: strings.Repeat("a", 64),
		},
	}
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Validator: %v", err)
	}

	// 2. ValidatePublishedValidator error (invalid ID slug)
	badVal := PublishedValidator{ID: "../bad", Title: "Bad"}
	cmd.Proposal.Draft.Validator = &badVal
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected ValidatePublishedValidator error")
	}

	// 3. safeReadFile error other than ErrNotExist (directory)
	validVal := PublishedValidator{
		ID:    "val-1",
		Title: "Validator 1",
		Validator: Validator{
			ID:            "val-1",
			Kind:          "non-null",
			Scope:         "row",
			Severity:      "error",
			OnUnavailable: "fail",
			Field:         "f1",
		},
	}
	cmd.Proposal.Draft.Validator = &validVal
	_ = os.MkdirAll(filepath.Join(valDir, validVal.ID+".json"), 0700)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(valDir, validVal.ID+".json"))

	// 4. Existing validator: corrupt JSON -> ErrInvalid
	_ = os.WriteFile(filepath.Join(valDir, validVal.ID+".json"), []byte("{"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt validator JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(valDir, validVal.ID+".json"))

	// 5. Existing validator: Version mismatch -> ErrConflict
	_ = writeItem(valDir, validVal.ID, PublishedValidator{ID: validVal.ID, Title: validVal.Title, Version: 2, Validator: validVal.Validator})
	valMismatch := validVal
	valMismatch.Version = 1
	cmd.Proposal.Draft.Validator = &valMismatch
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on version mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(valDir, validVal.ID+".json"))

	// 6. New validator with Version != 0 -> ErrConflict
	valNewBadVer := validVal
	valNewBadVer.Version = 1
	cmd.Proposal.Draft.Validator = &valNewBadVer
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new validator with version != 0: %v", err)
	}
	cmd.Proposal.Draft.Validator = &validVal

	// 7. readItems[Check] error
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	_ = os.WriteFile(filepath.Join(checksDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[Check] error")
	}
	_ = os.Remove(filepath.Join(checksDir, "bad..slug.json"))

	// 8. readItems[PublishedValidator] error
	_ = os.WriteFile(filepath.Join(valDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[PublishedValidator] error")
	}
	_ = os.Remove(filepath.Join(valDir, "bad..slug.json"))

	// 9. Max capacity exceeded
	for i := 0; i < maxItems; i++ {
		id := fmt.Sprintf("val-cap-%03d", i)
		_ = writeItem(valDir, id, PublishedValidator{ID: id, Title: id, Version: 1, Validator: validVal.Validator})
	}
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity on max validators: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		_ = os.Remove(filepath.Join(valDir, fmt.Sprintf("val-cap-%03d.json", i)))
	}

	// 10. readItems[QueryValidators] error
	qvDir := store.definitionDir("query-validators")
	_ = os.MkdirAll(qvDir, 0700)
	_ = os.WriteFile(filepath.Join(qvDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[QueryValidators] error")
	}
	_ = os.Remove(filepath.Join(qvDir, "bad..slug.json"))

	// 11. validateGraph error
	_ = writeItem(checksDir, "chk-c1", Check{
		ID: "chk-c1", Title: "C1", Version: 1,
		Steps: []Step{{ID: "s1", Kind: "check", CheckID: "chk-c2"}},
	})
	_ = writeItem(checksDir, "chk-c2", Check{
		ID: "chk-c2", Title: "C2", Version: 1,
		Steps: []Step{{ID: "s1", Kind: "check", CheckID: "chk-c1"}},
	})
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected validateGraph error")
	}
	_ = os.Remove(filepath.Join(checksDir, "chk-c1.json"))
	_ = os.Remove(filepath.Join(checksDir, "chk-c2.json"))

	// 12. ensureQueryRefs error
	_ = writeItem(qvDir, "q-missing", QueryValidators{
		QueryID: "q-missing",
		Version: 1,
		Validators: []Validator{{
			ID:            "v1",
			Kind:          "non-null",
			Scope:         "row",
			Severity:      "error",
			OnUnavailable: "fail",
			Field:         "f1",
		}},
	})
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from ensureQueryRefs: %v", err)
	}
	_ = os.Remove(filepath.Join(qvDir, "q-missing.json"))

	// 13. Happy path: create validator
	receipt, err := local.ConfirmCatalog(ctx, cmd)
	if err != nil || receipt.ResourceID != validVal.ID {
		t.Fatalf("expected success creating validator: %v, %+v", err, receipt)
	}

	// 14. Happy path: update existing validator
	existingVal := validVal
	existingVal.Version = 1
	existingVal.Title = "Updated Val"
	updateCmd := cmd
	updateCmd.OperationID = "op-val-update"
	updateCmd.Proposal.Draft.Validator = &existingVal
	updateCmd.Binding.PayloadDigest = strings.Repeat("b", 64)
	receipt, err = local.ConfirmCatalog(ctx, updateCmd)
	if err != nil || receipt.ResourceID != existingVal.ID {
		t.Fatalf("expected success updating validator: %v, %+v", err, receipt)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AICatalog_Prepare_Scenario(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	scenDir := store.definitionDir("scenarios")
	_ = os.MkdirAll(scenDir, 0700)

	// 1. Scenario is nil -> ErrInvalid
	cmd := ai.Command{
		Scope:       scope,
		OperationID: "op-scen-nil",
		Proposal:    ai.Proposal{ID: "prop-scen-nil", Scope: scope, Draft: ai.Draft{Kind: ai.KindScenario, Scenario: nil}},
		Binding: core.OperationBinding{
			Project:       scope.Key,
			Actor:         scope.Actor,
			Action:        "ai-confirm-" + ai.KindScenario,
			ResourceID:    "prop-scen-nil",
			PayloadDigest: strings.Repeat("a", 64),
		},
	}
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil Scenario: %v", err)
	}

	// 2. ValidateScenario error
	badScen := Scenario{ID: "../bad", Title: "Bad"}
	cmd.Proposal.Draft.Scenario = &badScen
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected ValidateScenario error")
	}

	// 3. Step validation:
	// a) query step resolveQueryRef error
	scenWithBadQuery := Scenario{
		ID:    "scen-bad-q",
		Title: "Scen Bad Query",
		Steps: []ScenarioStep{{ID: "s1", Kind: "query", Ref: "missing-q"}},
	}
	cmd.Proposal.Draft.Scenario = &scenWithBadQuery
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing query: %v", err)
	}

	// b) check step getCheck error
	scenWithBadCheck := Scenario{
		ID:    "scen-bad-chk",
		Title: "Scen Bad Check",
		Steps: []ScenarioStep{{ID: "s1", Kind: "check", Ref: "missing-chk"}},
	}
	cmd.Proposal.Draft.Scenario = &scenWithBadCheck
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing check: %v", err)
	}

	// Prepare valid query and valid check
	queriesDir := filepath.Join(store.projectDir, "queries")
	_ = os.MkdirAll(queriesDir, 0700)
	_ = os.WriteFile(filepath.Join(queriesDir, "valid-q.query.json"), []byte(`{"id":"valid-q"}`), 0600)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	_ = writeItem(checksDir, "valid-chk", Check{ID: "valid-chk", Title: "Valid Check", Version: 1})

	validScen := Scenario{
		ID:    "scen-1",
		Title: "Scenario 1",
		Steps: []ScenarioStep{
			{ID: "s1", Kind: "query", Ref: "valid-q"},
			{ID: "s2", Kind: "check", Ref: "valid-chk"},
		},
	}
	cmd.Proposal.Draft.Scenario = &validScen

	// 4. safeReadFile error other than ErrNotExist (directory)
	_ = os.MkdirAll(filepath.Join(scenDir, validScen.ID+".json"), 0700)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(scenDir, validScen.ID+".json"))

	// 5. Existing scenario: corrupt JSON -> ErrInvalid
	_ = os.WriteFile(filepath.Join(scenDir, validScen.ID+".json"), []byte("{"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt scenario JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(scenDir, validScen.ID+".json"))

	// 6. Existing scenario: version mismatch -> ErrConflict
	_ = writeItem(scenDir, validScen.ID, Scenario{ID: validScen.ID, Title: validScen.Title, Version: 2})
	scenMismatch := validScen
	scenMismatch.Version = 1
	cmd.Proposal.Draft.Scenario = &scenMismatch
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on version mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(scenDir, validScen.ID+".json"))

	// 7. New scenario with version != 0 -> ErrConflict
	scenNewBadVer := validScen
	scenNewBadVer.Version = 1
	cmd.Proposal.Draft.Scenario = &scenNewBadVer
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for new scenario with version != 0: %v", err)
	}
	cmd.Proposal.Draft.Scenario = &validScen

	// 8. readItems[Scenario] error
	_ = os.WriteFile(filepath.Join(scenDir, "bad..slug.json"), []byte("{}"), 0600)
	if _, err := local.ConfirmCatalog(ctx, cmd); err == nil {
		t.Fatalf("expected readItems[Scenario] error")
	}
	_ = os.Remove(filepath.Join(scenDir, "bad..slug.json"))

	// 9. Max capacity exceeded
	for i := 0; i < maxItems; i++ {
		id := fmt.Sprintf("scen-cap-%03d", i)
		_ = writeItem(scenDir, id, Scenario{ID: id, Title: id, Version: 1})
	}
	if _, err := local.ConfirmCatalog(ctx, cmd); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity on max scenarios: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		_ = os.Remove(filepath.Join(scenDir, fmt.Sprintf("scen-cap-%03d.json", i)))
	}

	// 10. Happy path: create scenario
	receipt, err := local.ConfirmCatalog(ctx, cmd)
	if err != nil || receipt.ResourceID != validScen.ID {
		t.Fatalf("expected success creating scenario: %v, %+v", err, receipt)
	}

	// 11. Happy path: update existing scenario
	existingScen := validScen
	existingScen.Version = 1
	existingScen.Title = "Updated Scenario"
	updateCmd := cmd
	updateCmd.OperationID = "op-scen-update"
	updateCmd.Proposal.Draft.Scenario = &existingScen
	updateCmd.Binding.PayloadDigest = strings.Repeat("b", 64)
	receipt, err = local.ConfirmCatalog(ctx, updateCmd)
	if err != nil || receipt.ResourceID != existingScen.ID {
		t.Fatalf("expected success updating scenario: %v, %+v", err, receipt)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AICatalog_Finish_And_Helpers(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	// Test aiCatalogCollection directly
	if col := aiCatalogCollection(ai.KindCheck); col != "checks" {
		t.Fatalf("expected checks, got %s", col)
	}
	if col := aiCatalogCollection(ai.KindValidator); col != "validators" {
		t.Fatalf("expected validators, got %s", col)
	}
	if col := aiCatalogCollection(ai.KindScenario); col != "scenarios" {
		t.Fatalf("expected scenarios, got %s", col)
	}
	if col := aiCatalogCollection("unknown"); col != "" {
		t.Fatalf("expected empty string for unknown, got %s", col)
	}

	// Test finishAICatalog edge cases:
	// a) Bad collection or bad slug -> ErrInvalid
	badOp := aiCatalogOperation{
		Kind:    "unknown",
		Receipt: ai.Receipt{ResourceID: "valid-id"},
	}
	if err := local.finishAICatalog(ctx, "op-1", &badOp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid: %v", err)
	}
	badOp2 := aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: ai.Receipt{ResourceID: "../bad"},
	}
	if err := local.finishAICatalog(ctx, "op-1", &badOp2); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid: %v", err)
	}

	// b) safeReadFile error other than ErrNotExist (directory)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	_ = os.MkdirAll(filepath.Join(checksDir, "chk-dir.json"), 0700)
	dirOp := aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: ai.Receipt{ResourceID: "chk-dir"},
	}
	if err := local.finishAICatalog(ctx, "op-1", &dirOp); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(checksDir, "chk-dir.json"))

	// c) !jsonSame(current, op.After) && !jsonSame(current, op.Before) -> ErrConflict
	_ = writeItem(checksDir, "chk-conflict", Check{ID: "chk-conflict", Title: "Current Title", Version: 5})
	conflictOp := aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: ai.Receipt{ResourceID: "chk-conflict"},
		Before:  json.RawMessage(`{"id":"chk-conflict","title":"Old Title","version":1}`),
		After:   json.RawMessage(`{"id":"chk-conflict","title":"New Title","version":2}`),
	}
	if err := local.finishAICatalog(ctx, "op-conflict", &conflictOp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict: %v", err)
	}
	_ = os.Remove(filepath.Join(checksDir, "chk-conflict.json"))

	// d) writeItem error on definitionDir (chmod 0500)
	writeErrOp := aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: ai.Receipt{ResourceID: "chk-werr"},
		Before:  nil,
		After:   json.RawMessage(`{"id":"chk-werr","title":"New Title","version":1}`),
	}
	if err := os.Chmod(checksDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := local.finishAICatalog(ctx, "op-werr", &writeErrOp); err == nil {
		t.Fatalf("expected writeItem error on read-only checksDir")
	}
	_ = os.Chmod(checksDir, 0700)

	// e) auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	storeFail := *store
	storeFail.audit = failAuditStore
	localFail := LocalAIStore{Store: &storeFail, Scope: scope}
	auditFailOp := aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: ai.Receipt{ResourceID: "chk-audit-fail"},
		Before:  nil,
		After:   json.RawMessage(`{"id":"chk-audit-fail","title":"Title","version":1}`),
		Audit:   AuditEvent{ID: "op-audit-fail", Action: "ai-confirm-draft-check"},
	}
	if err := localFail.finishAICatalog(ctx, "op-audit-fail", &auditFailOp); err == nil {
		t.Fatalf("expected auditMutation error")
	}
	_ = os.Remove(filepath.Join(checksDir, "chk-audit-fail.json"))

	// f) writeItem on ai-confirm-operations error -> ErrOutcomeUncertain
	opsDir := store.privateItemDir("ai-confirm-operations")
	_ = os.MkdirAll(opsDir, 0700)
	uncOp := aiCatalogOperation{
		Kind:    ai.KindCheck,
		Receipt: ai.Receipt{ResourceID: "chk-unc"},
		Before:  nil,
		After:   json.RawMessage(`{"id":"chk-unc","title":"Title","version":1}`),
		Audit:   AuditEvent{ID: "op-unc", Action: "ai-confirm-draft-check"},
	}
	if err := os.Chmod(opsDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := local.finishAICatalog(ctx, "op-unc", &uncOp); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain: %v", err)
	}
	_ = os.Chmod(opsDir, 0700)
	_ = os.Remove(filepath.Join(checksDir, "chk-unc.json"))
}
