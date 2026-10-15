package detective

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AI_VerifyAIBasisLocked(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}

	// 1. CatalogRevision is empty -> returns nil
	propNoBasis := ai.Proposal{Scope: scope}
	if err := local.verifyAIBasisLocked(ctx, propNoBasis); err != nil {
		t.Fatalf("expected nil for empty CatalogRevision: %v", err)
	}

	// 2. BasisVerifier is nil -> ai.ErrUnauthorized
	propWithBasis := ai.Proposal{Scope: scope, Basis: ai.Snapshot{CatalogRevision: "rev-1"}}
	if err := local.verifyAIBasisLocked(ctx, propWithBasis); !errors.Is(err, ai.ErrUnauthorized) {
		t.Fatalf("expected ai.ErrUnauthorized when BasisVerifier is nil: %v", err)
	}

	// 3. statePageLocked error (e.g. bad slug in checks directory)
	checksDir := store.definitionDir("checks")
	_ = os.MkdirAll(checksDir, 0700)
	badSlugPath := filepath.Join(checksDir, "bad..slug.json")
	_ = os.WriteFile(badSlugPath, []byte("{}"), 0600)
	localWithVerifier := local
	localWithVerifier.BasisVerifier = func(ctx context.Context, basis ai.Snapshot, state State) error {
		return nil
	}
	if err := localWithVerifier.verifyAIBasisLocked(ctx, propWithBasis); err == nil {
		t.Fatalf("expected error from statePageLocked")
	}
	_ = os.Remove(badSlugPath)

	// 4. BasisVerifier returns error
	localWithVerifier.BasisVerifier = func(ctx context.Context, basis ai.Snapshot, state State) error {
		return errors.New("basis mismatch")
	}
	if err := localWithVerifier.verifyAIBasisLocked(ctx, propWithBasis); err == nil {
		t.Fatalf("expected error from BasisVerifier")
	}

	// 5. BasisVerifier returns nil
	localWithVerifier.BasisVerifier = func(ctx context.Context, basis ai.Snapshot, state State) error {
		return nil
	}
	if err := localWithVerifier.verifyAIBasisLocked(ctx, propWithBasis); err != nil {
		t.Fatalf("unexpected error when BasisVerifier succeeds: %v", err)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AI_AIGeneration(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	genDir := store.privateItemDir("ai-generations")
	_ = os.MkdirAll(genDir, 0700)

	binding := core.OperationBinding{
		Project:       scope.Key,
		Actor:         scope.Actor,
		Action:        "ai-generate",
		ResourceID:    "res-1",
		PayloadDigest: strings.Repeat("a", 64),
	}

	// 1. BeginAIGeneration validation
	invalidStores := []LocalAIStore{
		{Store: nil, Scope: scope},
	}
	for i, badStore := range invalidStores {
		if _, _, err := badStore.BeginAIGeneration(ctx, "gen-1", binding); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidStores[%d]: expected ErrInvalid: %v", i, err)
		}
	}
	invalidBindings := []core.OperationBinding{
		func() core.OperationBinding { b := binding; b.Project = "wrong"; return b }(),
		func() core.OperationBinding { b := binding; b.Actor = "wrong"; return b }(),
		func() core.OperationBinding { b := binding; b.Action = "wrong"; return b }(),
	}
	for i, badBinding := range invalidBindings {
		if _, _, err := local.BeginAIGeneration(ctx, "gen-1", badBinding); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalidBindings[%d]: expected ErrInvalid: %v", i, err)
		}
	}
	if _, _, err := local.BeginAIGeneration(ctx, "../bad", binding); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad id slug")
	}

	// 2. BeginAIGeneration Replay:
	// a) safeReadFile error (directory in place of op file)
	_ = os.MkdirAll(filepath.Join(genDir, "gen-dir.json"), 0700)
	if _, _, err := local.BeginAIGeneration(ctx, "gen-dir", binding); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(genDir, "gen-dir.json"))

	// b) Corrupt JSON or CheckOperationReplay error -> ErrConflict
	_ = os.WriteFile(filepath.Join(genDir, "gen-corrupt.json"), []byte("{"), 0600)
	if _, _, err := local.BeginAIGeneration(ctx, "gen-corrupt", binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on corrupt JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(genDir, "gen-corrupt.json"))

	receipt, _ := core.NewOperationReceipt("gen-replay", binding, 0, time.Now().UTC())
	badReceipt := receipt
	badReceipt.PayloadDigest = strings.Repeat("b", 64)
	_ = writeItem(genDir, "gen-bad-receipt", aiGenerationOperation{Receipt: badReceipt})
	if _, _, err := local.BeginAIGeneration(ctx, "gen-bad-receipt", binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on receipt mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(genDir, "gen-bad-receipt.json"))

	// c) !operation.Produced -> ErrOutcomeUncertain
	_ = writeItem(genDir, "gen-unproduced", aiGenerationOperation{Receipt: receipt, Produced: false})
	if _, _, err := local.BeginAIGeneration(ctx, "gen-unproduced", binding); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain on unproduced op: %v", err)
	}
	_ = os.Remove(filepath.Join(genDir, "gen-unproduced.json"))

	// d) !operation.Done -> finishAIGeneration called (audit error)
	auditPath := filepath.Join(store.privateDir, "audit.jsonl")
	_ = os.WriteFile(auditPath, []byte("{\n"), 0600)
	_ = writeItem(genDir, "gen-not-done", aiGenerationOperation{Receipt: receipt, Produced: true, Done: false})
	if _, _, err := local.BeginAIGeneration(ctx, "gen-not-done", binding); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected error from finishAIGeneration on corrupt audit: %v", err)
	}
	_ = os.Remove(auditPath)
	_ = os.Remove(filepath.Join(genDir, "gen-not-done.json"))

	// e) operation.Done -> returns response, replay=true, nil
	expectedResp := ai.Response{Proposals: []ai.Proposal{{ID: "prop-gen-done"}}}
	_ = writeItem(genDir, "gen-done", aiGenerationOperation{Receipt: receipt, Response: expectedResp, Produced: true, Done: true})
	gotResp, replay, err := local.BeginAIGeneration(ctx, "gen-done", binding)
	if err != nil || !replay || len(gotResp.Proposals) != 1 || gotResp.Proposals[0].ID != "prop-gen-done" {
		t.Fatalf("unexpected result from done replay: %v, %v, %+v", err, replay, gotResp)
	}
	_ = os.Remove(filepath.Join(genDir, "gen-done.json"))

	// 3. BeginAIGeneration: New operation
	// Bad payload digest -> NewOperationReceipt error -> ErrInvalid
	badDigestBinding := binding
	badDigestBinding.PayloadDigest = "bad"
	if _, _, err := local.BeginAIGeneration(ctx, "gen-bad-digest", badDigestBinding); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on invalid digest in BeginAIGeneration: %v", err)
	}

	// writeItem error (chmod 0500)
	if err := os.Chmod(genDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.BeginAIGeneration(ctx, "gen-werr", binding); err == nil {
		t.Fatalf("expected writeItem error in BeginAIGeneration")
	}
	_ = os.Chmod(genDir, 0700)

	// Happy path for BeginAIGeneration
	_, replay, err = local.BeginAIGeneration(ctx, "gen-happy", binding)
	if err != nil || replay {
		t.Fatalf("expected success for new BeginAIGeneration: %v, %v", err, replay)
	}

	// 4. CompleteAIGeneration
	// a) safeReadFile error (missing op)
	if err := local.CompleteAIGeneration(ctx, "gen-missing", binding, expectedResp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist: %v", err)
	}

	// b) Corrupt JSON or CheckOperationReplay error -> ErrConflict
	_ = os.WriteFile(filepath.Join(genDir, "gen-comp-corrupt.json"), []byte("{"), 0600)
	if err := local.CompleteAIGeneration(ctx, "gen-comp-corrupt", binding, expectedResp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on corrupt JSON in CompleteAIGeneration: %v", err)
	}
	_ = os.Remove(filepath.Join(genDir, "gen-comp-corrupt.json"))

	// c) operation.Produced == true -> ErrConflict
	_ = writeItem(genDir, "gen-already-produced", aiGenerationOperation{Receipt: receipt, Produced: true})
	if err := local.CompleteAIGeneration(ctx, "gen-already-produced", binding, expectedResp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on already produced op: %v", err)
	}
	_ = os.Remove(filepath.Join(genDir, "gen-already-produced.json"))

	// d) writeItem error (chmod 0500)
	if err := os.Chmod(genDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := local.CompleteAIGeneration(ctx, "gen-happy", binding, expectedResp); err == nil {
		t.Fatalf("expected writeItem error in CompleteAIGeneration")
	}
	_ = os.Chmod(genDir, 0700)

	// e) finishAIGeneration error via auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	storeFail := *store
	storeFail.audit = failAuditStore
	localFail := LocalAIStore{Store: &storeFail, Scope: scope}
	_, _, _ = local.BeginAIGeneration(ctx, "gen-audit-fail", binding)
	if err := localFail.CompleteAIGeneration(ctx, "gen-audit-fail", binding, expectedResp); err == nil {
		t.Fatalf("expected auditMutation error in CompleteAIGeneration")
	}

	// f) finishAIGeneration: writeItem error -> ErrOutcomeUncertain
	// Replace genDir with read-only after audit
	// We can test finishAIGeneration directly
	validOp := aiGenerationOperation{Receipt: receipt, Response: expectedResp, Produced: true}
	if err := os.Chmod(genDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := local.finishAIGeneration(ctx, "gen-fin-unc", &validOp); !errors.Is(err, ErrOutcomeUncertain) {
		t.Fatalf("expected ErrOutcomeUncertain: %v", err)
	}
	_ = os.Chmod(genDir, 0700)

	// g) Happy path for CompleteAIGeneration
	if err := local.CompleteAIGeneration(ctx, "gen-happy", binding, expectedResp); err != nil {
		t.Fatalf("unexpected error in CompleteAIGeneration: %v", err)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AI_Proposal_CRUD(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	propDir := store.privateItemDir("ai-proposals")
	_ = os.MkdirAll(propDir, 0700)

	proposal := ai.Proposal{
		ID:    "prop-1",
		Scope: scope,
		Draft: ai.Draft{Kind: ai.KindCase, Case: &ai.CaseEdit{Title: "Case Draft"}},
	}

	// 1. Save validation
	if err := (LocalAIStore{Store: nil, Scope: scope}).Save(ctx, proposal); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil store: %v", err)
	}
	badScopeProp := proposal
	badScopeProp.Scope.Key = "other"
	if err := local.Save(ctx, badScopeProp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for mismatched scope: %v", err)
	}
	badSlugProp := proposal
	badSlugProp.ID = "../bad"
	if err := local.Save(ctx, badSlugProp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad ID slug: %v", err)
	}

	// 2. Save replay/update:
	// a) safeReadFile error (directory)
	_ = os.MkdirAll(filepath.Join(propDir, "prop-dir.json"), 0700)
	dirProp := proposal
	dirProp.ID = "prop-dir"
	if err := local.Save(ctx, dirProp); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(propDir, "prop-dir.json"))

	// b) Corrupt JSON in existing proposal -> ErrInvalid
	_ = os.WriteFile(filepath.Join(propDir, "prop-corrupt.json"), []byte("{"), 0600)
	corruptProp := proposal
	corruptProp.ID = "prop-corrupt"
	if err := local.Save(ctx, corruptProp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt proposal JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(propDir, "prop-corrupt.json"))

	// c) old.Scope mismatch -> ErrInvalid
	otherScopeProp := proposal
	otherScopeProp.ID = "prop-other-scope"
	otherScopeProp.Scope.Key = "local:other"
	_ = writeItem(propDir, "prop-other-scope", otherScopeProp)
	testProp := proposal
	testProp.ID = "prop-other-scope"
	if err := local.Save(ctx, testProp); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on scope mismatch with existing: %v", err)
	}
	_ = os.Remove(filepath.Join(propDir, "prop-other-scope.json"))

	// d) Existing identical proposal -> nil (idempotent)
	_ = writeItem(propDir, proposal.ID, proposal)
	if err := local.Save(ctx, proposal); err != nil {
		t.Fatalf("expected nil on idempotent Save: %v", err)
	}

	// e) Existing different proposal -> ErrConflict
	diffProp := proposal
	diffProp.Draft.Case = &ai.CaseEdit{Title: "Different"}
	if err := local.Save(ctx, diffProp); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on different proposal: %v", err)
	}
	_ = os.Remove(filepath.Join(propDir, proposal.ID+".json"))

	// 3. Save new proposal:
	// a) readItems error (> maxItems)
	for i := 0; i < maxItems+1; i++ {
		_ = os.WriteFile(filepath.Join(propDir, fmt.Sprintf("dummy-%03d.json", i)), []byte("{}"), 0600)
	}
	if err := local.Save(ctx, proposal); err == nil {
		t.Fatalf("expected readItems error exceeding maxItems")
	}
	for i := 0; i < maxItems+1; i++ {
		_ = os.Remove(filepath.Join(propDir, fmt.Sprintf("dummy-%03d.json", i)))
	}

	// b) len(items) >= maxItems -> ErrCapacity
	for i := 0; i < maxItems; i++ {
		id := fmt.Sprintf("cap-%03d", i)
		_ = writeItem(propDir, id, ai.Proposal{ID: id, Scope: scope})
	}
	if err := local.Save(ctx, ai.Proposal{ID: "prop-overflow", Scope: scope}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		_ = os.Remove(filepath.Join(propDir, fmt.Sprintf("cap-%03d.json", i)))
	}

	// c) writeItem error (chmod 0500)
	if err := os.Chmod(propDir, 0500); err != nil {
		t.Fatal(err)
	}
	if err := local.Save(ctx, proposal); err == nil {
		t.Fatalf("expected writeItem error on read-only propDir")
	}
	_ = os.Chmod(propDir, 0700)

	// d) Happy path for Save
	if err := local.Save(ctx, proposal); err != nil {
		t.Fatalf("unexpected error saving proposal: %v", err)
	}

	// 4. Get
	// a) Store nil, bad slug
	if _, err := (LocalAIStore{Store: nil, Scope: scope}).Get(ctx, "prop-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil store: %v", err)
	}
	if _, err := local.Get(ctx, "../bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad slug: %v", err)
	}

	// b) Not exist -> ErrNotFound
	if _, err := local.Get(ctx, "missing-prop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound: %v", err)
	}

	// c) safeReadFile error (directory)
	_ = os.MkdirAll(filepath.Join(propDir, "get-dir.json"), 0700)
	if _, err := local.Get(ctx, "get-dir"); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(propDir, "get-dir.json"))

	// d) Corrupt JSON -> ErrNotFound
	_ = os.WriteFile(filepath.Join(propDir, "get-corrupt.json"), []byte("{"), 0600)
	if _, err := local.Get(ctx, "get-corrupt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on corrupt JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(propDir, "get-corrupt.json"))

	// e) proposal.ID mismatch or Scope mismatch -> ErrNotFound
	_ = writeItem(propDir, "get-mismatch", ai.Proposal{ID: "other-id", Scope: scope})
	if _, err := local.Get(ctx, "get-mismatch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on ID mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(propDir, "get-mismatch.json"))

	_ = writeItem(propDir, "get-scope-mismatch", ai.Proposal{ID: "get-scope-mismatch", Scope: ai.Scope{Key: "other"}})
	if _, err := local.Get(ctx, "get-scope-mismatch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on scope mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(propDir, "get-scope-mismatch.json"))

	// f) Happy path for Get
	got, err := local.Get(ctx, proposal.ID)
	if err != nil || got.ID != proposal.ID {
		t.Fatalf("unexpected error getting proposal: %v, got: %+v", err, got)
	}
	_ = os.Remove(filepath.Join(propDir, proposal.ID+".json"))

	// 5. Page
	// a) Store nil, bad after slug
	if _, err := (LocalAIStore{Store: nil, Scope: scope}).Page(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil store: %v", err)
	}
	if _, err := local.Page(ctx, "../bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad after slug: %v", err)
	}

	// b) readItems error (> maxItems)
	for i := 0; i < maxItems+1; i++ {
		_ = os.WriteFile(filepath.Join(propDir, fmt.Sprintf("dummy-%03d.json", i)), []byte("{}"), 0600)
	}
	if _, err := local.Page(ctx, ""); err == nil {
		t.Fatalf("expected readItems error exceeding maxItems")
	}
	for i := 0; i < maxItems+1; i++ {
		_ = os.Remove(filepath.Join(propDir, fmt.Sprintf("dummy-%03d.json", i)))
	}

	// c) Pagination with 55 proposals
	for i := 0; i < 55; i++ {
		id := fmt.Sprintf("page-prop-%03d", i)
		_ = writeItem(propDir, id, ai.Proposal{ID: id, Scope: scope})
	}
	// Add an item with different scope to test skipping
	_ = writeItem(propDir, "page-prop-diff-scope", ai.Proposal{ID: "page-prop-diff-scope", Scope: ai.Scope{Key: "other"}})

	page1, err := local.Page(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error in Page: %v", err)
	}
	if len(page1.Proposals) != 50 || !page1.HasMore || page1.NextCursor == "" {
		t.Fatalf("expected 50 proposals with HasMore=true: len=%d, HasMore=%v, cursor=%s", len(page1.Proposals), page1.HasMore, page1.NextCursor)
	}

	page2, err := local.Page(ctx, page1.NextCursor)
	if err != nil {
		t.Fatalf("unexpected error in Page 2: %v", err)
	}
	if len(page2.Proposals) != 5 || page2.HasMore || page2.NextCursor != "" {
		t.Fatalf("expected remaining 5 proposals: len=%d, HasMore=%v", len(page2.Proposals), page2.HasMore)
	}

	for i := 0; i < 55; i++ {
		_ = os.Remove(filepath.Join(propDir, fmt.Sprintf("page-prop-%03d.json", i)))
	}
	_ = os.Remove(filepath.Join(propDir, "page-prop-diff-scope.json"))
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_AI_Paste_And_Sources(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	scope := ai.Scope{Key: "local:p", Project: "p", Actor: "alice"}
	local := LocalAIStore{Store: store, Scope: scope}
	sourcesDir := store.privateItemDir("ai-sources")
	_ = os.MkdirAll(sourcesDir, 0700)

	// 1. SavePaste validation
	if _, err := (LocalAIStore{Store: nil, Scope: scope}).SavePaste(ctx, "text"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for nil store: %v", err)
	}
	if _, err := local.SavePaste(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for empty text: %v", err)
	}
	if _, err := local.SavePaste(ctx, strings.Repeat("x", 16*1024+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for oversized text: %v", err)
	}

	// 2. SavePaste happy path
	src, err := local.SavePaste(ctx, "hello paste text")
	if err != nil {
		t.Fatalf("unexpected error saving paste: %v", err)
	}
	if src.ID == "" || src.Kind != "paste" {
		t.Fatalf("unexpected source: %+v", src)
	}

	// 3. SavePaste replay:
	// a) Exact same text returns existing source
	replaySrc, err := local.SavePaste(ctx, "hello paste text")
	if err != nil || replaySrc.ID != src.ID {
		t.Fatalf("expected idempotent paste replay: %v, %+v", err, replaySrc)
	}

	// b) Corrupt JSON in existing paste -> ErrConflict
	_ = os.WriteFile(filepath.Join(sourcesDir, src.ID+".json"), []byte("{"), 0600)
	if _, err := local.SavePaste(ctx, "hello paste text"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on corrupt paste: %v", err)
	}

	// c) old.Scope mismatch -> ErrConflict
	_ = writeItem(sourcesDir, src.ID, localAISource{Scope: ai.Scope{Key: "other"}})
	if _, err := local.SavePaste(ctx, "hello paste text"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on scope mismatch: %v", err)
	}

	// d) old.Evidence mismatch -> ErrConflict
	_ = writeItem(sourcesDir, src.ID, localAISource{Scope: scope, Evidence: ai.SourceEvidence{Length: 9999}})
	if _, err := local.SavePaste(ctx, "hello paste text"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on evidence mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(sourcesDir, src.ID+".json"))

	// 4. SavePaste new paste errors:
	// a) safeReadFile error (directory)
	paste2Text := "paste 2 text"
	src2, _ := local.SavePaste(ctx, paste2Text)
	_ = os.Remove(filepath.Join(sourcesDir, src2.ID+".json"))
	_ = os.MkdirAll(filepath.Join(sourcesDir, src2.ID+".json"), 0700)
	if _, err := local.SavePaste(ctx, paste2Text); err == nil {
		t.Fatalf("expected safeReadFile error on directory")
	}
	_ = os.RemoveAll(filepath.Join(sourcesDir, src2.ID+".json"))

	// b) readItems error (> maxItems)
	for i := 0; i < maxItems+1; i++ {
		_ = os.WriteFile(filepath.Join(sourcesDir, fmt.Sprintf("dummy-%03d.json", i)), []byte("{}"), 0600)
	}
	if _, err := local.SavePaste(ctx, paste2Text); err == nil {
		t.Fatalf("expected readItems error exceeding maxItems")
	}
	for i := 0; i < maxItems+1; i++ {
		_ = os.Remove(filepath.Join(sourcesDir, fmt.Sprintf("dummy-%03d.json", i)))
	}

	// len(items) >= maxItems -> ErrCapacity
	for i := 0; i < maxItems; i++ {
		id := fmt.Sprintf("cap-%03d", i)
		_ = writeItem(sourcesDir, id, localAISource{Scope: scope})
	}
	if _, err := local.SavePaste(ctx, paste2Text); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity in SavePaste: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		_ = os.Remove(filepath.Join(sourcesDir, fmt.Sprintf("cap-%03d.json", i)))
	}

	// c) writeItem error (chmod 0500)
	if err := os.Chmod(sourcesDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := local.SavePaste(ctx, paste2Text); err == nil {
		t.Fatalf("expected writeItem error on read-only sourcesDir")
	}
	_ = os.Chmod(sourcesDir, 0700)

	// Save valid paste for AuthorizeSources tests
	validSource, err := local.SavePaste(ctx, "valid paste content")
	if err != nil {
		t.Fatal(err)
	}
	validEvidence := ai.SourceEvidence{
		ID:       validSource.ID,
		Kind:     validSource.Kind,
		Revision: validSource.Revision,
		Digest:   validSource.Revision,
		Length:   len("valid paste content"),
	}

	// 5. AuthorizeSources
	// a) Scope mismatch
	if err := local.AuthorizeSources(ctx, ai.Scope{Key: "other"}, []ai.SourceEvidence{validEvidence}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on scope mismatch: %v", err)
	}

	// b) len(sources) > 32
	tooMany := make([]ai.SourceEvidence, 33)
	if err := local.AuthorizeSources(ctx, scope, tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on len > 32: %v", err)
	}

	// c) Invalid evidence.ID slug
	badSlugEv := validEvidence
	badSlugEv.ID = "../bad"
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{badSlugEv}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on bad slug: %v", err)
	}

	// d) evidence.Kind != "paste"
	badKindEv := validEvidence
	badKindEv.Kind = "other"
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{badKindEv}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on bad kind: %v", err)
	}

	// e) safeReadFile error (missing source) -> ErrNotFound
	missingEv := validEvidence
	missingEv.ID = "paste-missing"
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{missingEv}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing source: %v", err)
	}

	// f) Corrupt JSON in source -> ErrInvalid
	_ = os.WriteFile(filepath.Join(sourcesDir, "paste-corrupt.json"), []byte("{"), 0600)
	corruptEv := validEvidence
	corruptEv.ID = "paste-corrupt"
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{corruptEv}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on corrupt source JSON: %v", err)
	}
	_ = os.Remove(filepath.Join(sourcesDir, "paste-corrupt.json"))

	// g) saved.Scope mismatch -> ErrInvalid
	_ = writeItem(sourcesDir, "paste-diff-scope", localAISource{Scope: ai.Scope{Key: "other"}})
	diffScopeEv := validEvidence
	diffScopeEv.ID = "paste-diff-scope"
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{diffScopeEv}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on scope mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(sourcesDir, "paste-diff-scope.json"))

	// h) saved.Evidence mismatch -> ErrInvalid
	_ = writeItem(sourcesDir, "paste-diff-ev", localAISource{Scope: scope, Evidence: ai.SourceEvidence{Length: 1234}})
	diffEv := validEvidence
	diffEv.ID = "paste-diff-ev"
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{diffEv}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on evidence mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(sourcesDir, "paste-diff-ev.json"))

	// i) Happy path: AuthorizeSources succeeds
	if err := local.AuthorizeSources(ctx, scope, []ai.SourceEvidence{validEvidence}); err != nil {
		t.Fatalf("unexpected error in AuthorizeSources: %v", err)
	}
}
