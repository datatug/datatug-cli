package detective

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/investigation"
)

// Not parallel: tests filesystem modifications on store dirs and failing audit.
func TestCoverage100_Mutations_SaveCase(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	casesDir := s.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)

	// 1. reconcilePendingActions error (bad action file)
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	_ = os.WriteFile(filepath.Join(actDir, "bad.json"), []byte("{"), 0600)
	if _, err := s.SaveCase(ctx, Case{ID: "c1", Title: "T"}, "alice", "create-case"); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad.json"))

	// 2. Case does not exist, but action != "create-case" -> ErrConflict
	if _, err := s.SaveCase(ctx, Case{ID: "c1", Title: "T"}, "alice", "update-case"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got: %v", err)
	}

	// 3. Case does not exist, item.IncidentRefs != 0 -> ErrInvalid
	if _, err := s.SaveCase(ctx, Case{ID: "c1", Title: "T", IncidentRefs: []string{"inc-1"}}, "alice", "create-case"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for non-empty IncidentRefs, got: %v", err)
	}

	// 4. readItems error (> maxItems items in cases directory)
	for i := 0; i < maxItems+1; i++ {
		_ = os.WriteFile(filepath.Join(casesDir, fmt.Sprintf("dummy-%03d.json", i)), []byte("{}"), 0600)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "c1", Title: "T"}, "alice", "create-case"); err == nil {
		t.Fatalf("expected error from readItems exceeding maxItems")
	}
	// Clean up dummy files
	for i := 0; i < maxItems+1; i++ {
		_ = os.Remove(filepath.Join(casesDir, fmt.Sprintf("dummy-%03d.json", i)))
	}

	// 5. len(items) >= maxItems -> ErrCapacity
	for i := 0; i < maxItems; i++ {
		id := fmt.Sprintf("cap-%03d", i)
		_ = writeItem(casesDir, id, Case{ID: id, Title: "T"})
	}
	if _, err := s.SaveCase(ctx, Case{ID: "c-cap-overflow", Title: "T"}, "alice", "create-case"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity, got: %v", err)
	}
	for i := 0; i < maxItems; i++ {
		_ = os.Remove(filepath.Join(casesDir, fmt.Sprintf("cap-%03d.json", i)))
	}

	// 6. Case exists, action != "update-case" -> ErrConflict
	c1, err := s.SaveCase(ctx, Case{ID: "c1", Title: "T"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, c1, "alice", "create-case"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict on existing case with create-case, got: %v", err)
	}

	// 7. Case exists, old.IncidentRefs != item.IncidentRefs -> ErrInvalid
	modified := c1
	modified.IncidentRefs = []string{"inc-different"}
	if _, err := s.SaveCase(ctx, modified, "alice", "update-case"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on changed IncidentRefs, got: %v", err)
	}

	// 8. core.UpdateCase error (e.g. invalid status or stale version)
	stale := c1
	stale.Status = "nonexistent-status"
	if _, err := s.SaveCase(ctx, stale, "alice", "update-case"); err == nil {
		t.Fatalf("expected error from core.UpdateCase with invalid status")
	}

	// 9. compactCaseTimeline error (>400 events, timeline dir read-only)
	timelineDir := s.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	hugeEvents := make([]CaseEvent, 405)
	for i := range hugeEvents {
		hugeEvents[i] = CaseEvent{ID: fmt.Sprintf("ev-%03d", i), Type: "note"}
	}
	hugeCase := c1
	hugeCase.Timeline = hugeEvents
	_ = writeItem(casesDir, hugeCase.ID, hugeCase)
	if err := os.Chmod(timelineDir, 0500); err != nil {
		t.Fatal(err)
	}
	hugeUpdate := hugeCase
	hugeUpdate.Title = "Updated Huge"
	if _, err := s.SaveCase(ctx, hugeUpdate, "alice", "update-case"); err == nil {
		t.Fatalf("expected error from compactCaseTimeline")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = writeItem(casesDir, c1.ID, c1)

	// 10. writeItem error (casesDir chmod 0500)
	if err := os.Chmod(casesDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCase(ctx, Case{ID: "c-werr", Title: "T"}, "alice", "create-case"); err == nil {
		t.Fatalf("expected error from writeItem in read-only casesDir")
	}
	_ = os.Chmod(casesDir, 0700)

	// 11. auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	sFail := *s
	sFail.audit = failAuditStore
	if _, err := sFail.SaveCase(ctx, Case{ID: "c-audit-fail", Title: "T"}, "alice", "create-case"); err == nil {
		t.Fatalf("expected error from auditMutation in SaveCase")
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Mutations_ApplyScenario(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	scenariosDir := s.definitionDir("scenarios")
	casesDir := s.privateItemDir("cases")
	_ = os.MkdirAll(scenariosDir, 0700)
	_ = os.MkdirAll(casesDir, 0700)

	// 1. Invalid slugs
	if _, err := s.ApplyScenario(ctx, "../bad", "scen", "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad caseID: %v", err)
	}
	if _, err := s.ApplyScenario(ctx, "c1", "../bad", "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad scenarioID: %v", err)
	}

	// 2. reconcilePendingActions error
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	_ = os.WriteFile(filepath.Join(actDir, "bad.json"), []byte("{"), 0600)
	if _, err := s.ApplyScenario(ctx, "c1", "scen", "alice"); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad.json"))

	// 3. getCase error (case not found)
	if _, err := s.ApplyScenario(ctx, "c-notfound", "scen", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	// Create valid case
	c, err := s.SaveCase(ctx, Case{ID: "c-scen", Title: "Scenario Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}

	// 4. safeReadFile scenario not exist -> ErrNotFound
	if _, err := s.ApplyScenario(ctx, c.ID, "missing-scen", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing scenario: %v", err)
	}

	// 5. safeReadFile error (directory in place of scenario file)
	_ = os.MkdirAll(filepath.Join(scenariosDir, "scen-dir.json"), 0700)
	if _, err := s.ApplyScenario(ctx, c.ID, "scen-dir", "alice"); err == nil {
		t.Fatalf("expected error from safeReadFile on directory")
	}
	_ = os.RemoveAll(filepath.Join(scenariosDir, "scen-dir.json"))

	// 6. json.Unmarshal error (corrupt JSON)
	_ = os.WriteFile(filepath.Join(scenariosDir, "scen-corrupt.json"), []byte("{"), 0600)
	if _, err := s.ApplyScenario(ctx, c.ID, "scen-corrupt", "alice"); err == nil {
		t.Fatalf("expected error from json.Unmarshal on corrupt scenario")
	}
	_ = os.Remove(filepath.Join(scenariosDir, "scen-corrupt.json"))

	// 7. scenario.ID != scenarioID -> ErrInvalid
	_ = writeItem(scenariosDir, "scen-id-mismatch", Scenario{ID: "other-id", Title: "Mismatch"})
	if _, err := s.ApplyScenario(ctx, c.ID, "scen-id-mismatch", "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for scenario ID mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(scenariosDir, "scen-id-mismatch.json"))

	// 8. core.ApplyScenario error (e.g. archived case)
	scen, err := s.SaveScenario(ctx, Scenario{ID: "scen-valid", Title: "Valid Scen", Steps: []ScenarioStep{{ID: "s1", Kind: "query", Ref: "q"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	archivedCase := Case{ID: "c-arch", Title: "Archived", Status: "archived"}
	_ = writeItem(casesDir, archivedCase.ID, archivedCase)
	if _, err := s.ApplyScenario(ctx, archivedCase.ID, scen.ID, "alice"); err == nil {
		t.Fatalf("expected error from core.ApplyScenario on archived case")
	}

	// 9. compactCaseTimeline error
	timelineDir := s.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	hugeEvents := make([]CaseEvent, 405)
	for i := range hugeEvents {
		hugeEvents[i] = CaseEvent{ID: fmt.Sprintf("ev-%03d", i), Type: "note"}
	}
	hugeCase := c
	hugeCase.ID = "c-huge-scen"
	hugeCase.Timeline = hugeEvents
	_ = writeItem(casesDir, hugeCase.ID, hugeCase)
	if err := os.Chmod(timelineDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyScenario(ctx, hugeCase.ID, scen.ID, "alice"); err == nil {
		t.Fatalf("expected error from compactCaseTimeline in ApplyScenario")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = os.Remove(filepath.Join(casesDir, hugeCase.ID+".json"))

	// 10. writeItem error (casesDir chmod 0500)
	if err := os.Chmod(casesDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyScenario(ctx, c.ID, scen.ID, "alice"); err == nil {
		t.Fatalf("expected error from writeItem in ApplyScenario")
	}
	_ = os.Chmod(casesDir, 0700)

	// 11. auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	sFail := *s
	sFail.audit = failAuditStore
	cBeforeFail := c
	if _, err := sFail.ApplyScenario(ctx, c.ID, scen.ID, "alice"); err == nil {
		t.Fatalf("expected error from auditMutation in ApplyScenario")
	}
	_ = writeItem(casesDir, c.ID, cBeforeFail)

	// 12. Happy path
	applied, err := s.ApplyScenario(ctx, c.ID, scen.ID, "alice")
	if err != nil {
		t.Fatalf("unexpected error applying scenario: %v", err)
	}
	if len(applied.ScenarioApplications) != 1 {
		t.Fatalf("expected 1 scenario application, got %d", len(applied.ScenarioApplications))
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Mutations_RecordScenarioStep(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	casesDir := s.privateItemDir("cases")
	runsDir := s.privateItemDir("runs")
	_ = os.MkdirAll(casesDir, 0700)
	_ = os.MkdirAll(runsDir, 0700)

	// 1. Invalid caseID slug
	if _, err := s.RecordScenarioStep(ctx, "../bad", "app1", "step1", "completed", "run1", "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad caseID slug: %v", err)
	}

	// 2. reconcilePendingActions error
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	_ = os.WriteFile(filepath.Join(actDir, "bad.json"), []byte("{"), 0600)
	if _, err := s.RecordScenarioStep(ctx, "c1", "app1", "step1", "completed", "run1", "alice"); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad.json"))

	// 3. getCase error
	if _, err := s.RecordScenarioStep(ctx, "c-notfound", "app1", "step1", "completed", "run1", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	// Setup valid scenario and case
	if _, err := s.SaveCheck(ctx, Check{ID: "chk-1", Title: "Check 1", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	scen, err := s.SaveScenario(ctx, Scenario{
		ID:    "rec-scen",
		Title: "Record Scen",
		Steps: []ScenarioStep{
			{ID: "step-1", Kind: "check", Ref: "chk-1"},
			{ID: "step-2", Kind: "check", Ref: "chk-1"},
		},
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "c-rec", Title: "Rec Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, scen.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	appID := c.ScenarioApplications[0].ID

	// 4. status == "completed": getRun error (missing run)
	if _, err := s.RecordScenarioStep(ctx, c.ID, appID, "step-1", "completed", "run-missing", "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing run: %v", err)
	}

	// 5. status == "completed": authorizeCaseRunLink error (mismatched cohort context)
	seven := investigation.NewIntegerValue("7")
	cohortCase, err := s.SaveCase(ctx, Case{ID: "c-cohort-rec", Title: "Cohort Rec", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	cohortCase, err = s.ApplyScenario(ctx, cohortCase.ID, scen.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// Run without cohort proof
	_ = writeItem(runsDir, "run-no-proof", CheckRun{ID: "run-no-proof", CheckID: "chk-1", CaseID: cohortCase.ID, Actor: "alice", Environment: "qa", StartedAt: time.Now().UTC()})
	if _, err := s.RecordScenarioStep(ctx, cohortCase.ID, cohortCase.ScenarioApplications[0].ID, "step-1", "completed", "run-no-proof", "alice"); err == nil {
		t.Fatalf("expected authorizeCaseRunLink error")
	}

	// 6. core.RecordScenarioStep error (non-existent applicationID)
	validRun := CheckRun{
		ID:              "run-valid-step",
		CheckID:         "chk-1",
		Kind:            "check",
		CaseID:          c.ID,
		ExecutionStatus: "completed",
		Outcome:         "pass",
		Actor:           "alice",
		Environment:     "qa",
		StartedAt:       time.Now().UTC(),
	}
	_ = writeItem(runsDir, validRun.ID, validRun)
	cohortDir := s.privateItemDir("run-cohorts")
	_ = os.MkdirAll(cohortDir, 0700)
	cDigest, _ := caseContextDigest(c.Context)
	_ = writeItem(cohortDir, validRun.ID, runCohortProof{RunID: validRun.ID, CaseID: c.ID, ContextDigest: cDigest})
	c.RunRefs = []string{validRun.ID}
	_ = writeItem(casesDir, c.ID, c)

	if _, err := s.RecordScenarioStep(ctx, c.ID, "app-wrong", "step-1", "completed", validRun.ID, "alice"); err == nil {
		t.Fatalf("expected error from core.RecordScenarioStep with wrong applicationID")
	}

	// 7. compactCaseTimeline error
	timelineDir := s.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	hugeEvents := make([]CaseEvent, 405)
	for i := range hugeEvents {
		hugeEvents[i] = CaseEvent{ID: fmt.Sprintf("ev-%03d", i), Type: "note"}
	}
	hugeCase := c
	hugeCase.ID = "c-huge-rec"
	hugeCase.Timeline = hugeEvents
	_ = writeItem(casesDir, hugeCase.ID, hugeCase)
	if err := os.Chmod(timelineDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordScenarioStep(ctx, hugeCase.ID, appID, "step-1", "skipped", "", "alice"); err == nil {
		t.Fatalf("expected error from compactCaseTimeline in RecordScenarioStep")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = os.Remove(filepath.Join(casesDir, hugeCase.ID+".json"))

	// 8. writeItem error (casesDir chmod 0500)
	if err := os.Chmod(casesDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordScenarioStep(ctx, c.ID, appID, "step-1", "completed", validRun.ID, "alice"); err == nil {
		t.Fatalf("expected writeItem error in RecordScenarioStep")
	}
	_ = os.Chmod(casesDir, 0700)

	// 9. auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	sFail := *s
	sFail.audit = failAuditStore
	cBeforeFail := c
	if _, err := sFail.RecordScenarioStep(ctx, c.ID, appID, "step-1", "completed", validRun.ID, "alice"); err == nil {
		t.Fatalf("expected auditMutation error in RecordScenarioStep")
	}
	_ = writeItem(casesDir, c.ID, cBeforeFail)

	// 10. Happy path: complete step-1, then complete step-2 with the same run to hit linked=true
	cLinked, err := s.RecordScenarioStep(ctx, c.ID, appID, "step-1", "completed", validRun.ID, "alice")
	if err != nil {
		t.Fatalf("unexpected error recording completed step-1: %v", err)
	}
	if _, err := s.RecordScenarioStep(ctx, cLinked.ID, appID, "step-2", "completed", validRun.ID, "alice"); err != nil {
		t.Fatalf("unexpected error recording completed step-2 with already linked run: %v", err)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Mutations_RecordScenarioQueryStep(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	casesDir := s.privateItemDir("cases")
	_ = os.MkdirAll(casesDir, 0700)

	proof := core.ScenarioQueryProof{
		Execution: apicontract.ExecutionRef{StoreID: "p", ProjectID: s.projectID, ExecutionID: "exec-1"},
		QueryID:   "q", Environment: "qa", ExecutionStatus: "completed",
	}

	// 1. Invalid caseID slug or mismatched ProjectID
	if _, err := s.RecordScenarioQueryStep(ctx, "../bad", "app1", "s1", proof, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad caseID slug: %v", err)
	}
	badProjProof := proof
	badProjProof.Execution.ProjectID = "wrong-project"
	if _, err := s.RecordScenarioQueryStep(ctx, "c1", "app1", "s1", badProjProof, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for mismatched ProjectID: %v", err)
	}

	// 2. reconcilePendingActions error
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	_ = os.WriteFile(filepath.Join(actDir, "bad.json"), []byte("{"), 0600)
	if _, err := s.RecordScenarioQueryStep(ctx, "c1", "app1", "s1", proof, "alice"); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad.json"))

	// 3. getCase error
	if _, err := s.RecordScenarioQueryStep(ctx, "c-notfound", "app1", "s1", proof, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	// Setup case and scenario
	scen, err := s.SaveScenario(ctx, Scenario{
		ID:    "query-scen",
		Title: "Query Scen",
		Steps: []ScenarioStep{{ID: "step-q", Kind: "query", Ref: "q"}},
	}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCase(ctx, Case{ID: "c-q-step", Title: "Query Step Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.ApplyScenario(ctx, c.ID, scen.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	appID := c.ScenarioApplications[0].ID

	// 4. core.RecordScenarioQueryStep error (e.g. wrong stepID)
	if _, err := s.RecordScenarioQueryStep(ctx, c.ID, appID, "wrong-step", proof, "alice"); err == nil {
		t.Fatalf("expected error from core.RecordScenarioQueryStep on wrong stepID")
	}

	// 5. compactCaseTimeline error
	timelineDir := s.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	hugeEvents := make([]CaseEvent, 405)
	for i := range hugeEvents {
		hugeEvents[i] = CaseEvent{ID: fmt.Sprintf("ev-%03d", i), Type: "note"}
	}
	hugeCase := c
	hugeCase.ID = "c-huge-q"
	hugeCase.Timeline = hugeEvents
	_ = writeItem(casesDir, hugeCase.ID, hugeCase)
	if err := os.Chmod(timelineDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordScenarioQueryStep(ctx, hugeCase.ID, appID, "step-q", proof, "alice"); err == nil {
		t.Fatalf("expected error from compactCaseTimeline in RecordScenarioQueryStep")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = os.Remove(filepath.Join(casesDir, hugeCase.ID+".json"))

	// 6. writeItem error (casesDir chmod 0500)
	if err := os.Chmod(casesDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordScenarioQueryStep(ctx, c.ID, appID, "step-q", proof, "alice"); err == nil {
		t.Fatalf("expected writeItem error in RecordScenarioQueryStep")
	}
	_ = os.Chmod(casesDir, 0700)

	// 7. auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	sFail := *s
	sFail.audit = failAuditStore
	cBeforeFail := c
	if _, err := sFail.RecordScenarioQueryStep(ctx, c.ID, appID, "step-q", proof, "alice"); err == nil {
		t.Fatalf("expected auditMutation error in RecordScenarioQueryStep")
	}
	_ = writeItem(casesDir, c.ID, cBeforeFail)

	// 8. Happy path
	updated, err := s.RecordScenarioQueryStep(ctx, c.ID, appID, "step-q", proof, "alice")
	if err != nil {
		t.Fatalf("unexpected error in RecordScenarioQueryStep: %v", err)
	}
	if updated.ScenarioApplications[0].Steps[0].Status != "completed" {
		t.Fatalf("expected step completed, got: %s", updated.ScenarioApplications[0].Steps[0].Status)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Mutations_AppendCaseEvent(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	casesDir := s.privateItemDir("cases")
	runsDir := s.privateItemDir("runs")
	_ = os.MkdirAll(casesDir, 0700)
	_ = os.MkdirAll(runsDir, 0700)

	// 1. Invalid case ID slug
	if _, err := s.AppendCaseEvent(ctx, "../bad", CaseEvent{Type: "note"}, "alice"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad case ID slug: %v", err)
	}

	// 2. reconcilePendingActions error
	actDir := s.privateItemDir("action-operations")
	_ = os.MkdirAll(actDir, 0700)
	_ = os.WriteFile(filepath.Join(actDir, "bad.json"), []byte("{"), 0600)
	if _, err := s.AppendCaseEvent(ctx, "c1", CaseEvent{Type: "note"}, "alice"); err == nil {
		t.Fatalf("expected error from reconcilePendingActions")
	}
	_ = os.Remove(filepath.Join(actDir, "bad.json"))

	// 3. getCase error
	if _, err := s.AppendCaseEvent(ctx, "c-notfound", CaseEvent{Type: "note"}, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing case: %v", err)
	}

	c, err := s.SaveCase(ctx, Case{ID: "c-ev", Title: "Event Case"}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}

	// 4. event.Type == "link-evidence": getRun error (missing run)
	if _, err := s.AppendCaseEvent(ctx, c.ID, CaseEvent{Type: "link-evidence", Ref: "run-missing"}, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing run in link-evidence: %v", err)
	}

	// 5. event.Type == "link-evidence": authorizeCaseRunLink error
	seven := investigation.NewIntegerValue("7")
	cohortCase, err := s.SaveCase(ctx, Case{ID: "c-cohort-ev", Title: "Cohort EV", Context: map[string]InputValue{"invoice_id": {Scalar: &seven}}}, "alice", "create-case")
	if err != nil {
		t.Fatal(err)
	}
	_ = writeItem(runsDir, "run-no-proof-ev", CheckRun{ID: "run-no-proof-ev", CheckID: "chk-1", Actor: "alice", Environment: "qa", StartedAt: time.Now().UTC()})
	if _, err := s.AppendCaseEvent(ctx, cohortCase.ID, CaseEvent{Type: "link-evidence", Ref: "run-no-proof-ev"}, "alice"); err == nil {
		t.Fatalf("expected authorizeCaseRunLink error in link-evidence")
	}

	// 6. appendServerCaseRun:
	// a) server == true, event.Type != "check-run" or run == nil
	// Calling appendCaseEvent directly with server=true and invalid event (run loaded but wrong type)
	validRun := CheckRun{ID: "run-server-1", CheckID: "chk-1", CaseID: c.ID, Actor: "alice", Environment: "qa", StartedAt: time.Now().UTC()}
	_ = writeItem(runsDir, validRun.ID, validRun)
	cohortDir := s.privateItemDir("run-cohorts")
	_ = os.MkdirAll(cohortDir, 0700)
	cDigest, _ := caseContextDigest(c.Context)
	_ = writeItem(cohortDir, validRun.ID, runCohortProof{RunID: validRun.ID, CaseID: c.ID, ContextDigest: cDigest})

	if _, err := s.appendCaseEvent(ctx, c.ID, CaseEvent{Type: "link-evidence", Ref: validRun.ID}, "alice", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid when server=true and event.Type != check-run: %v", err)
	}

	// b) valid appendServerCaseRun
	cWithRun, err := s.appendServerCaseRun(ctx, c.ID, validRun.ID, "alice")
	if err != nil {
		t.Fatalf("unexpected error in appendServerCaseRun: %v", err)
	}
	if len(cWithRun.RunRefs) != 1 || cWithRun.RunRefs[0] != validRun.ID {
		t.Fatalf("expected run attached to case RunRefs: %+v", cWithRun.RunRefs)
	}

	// 7. core.AttachRun or AppendCaseEvent error (e.g. attaching duplicate run returns conflict)
	if _, err := s.appendServerCaseRun(ctx, cWithRun.ID, validRun.ID, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict attaching duplicate run: %v", err)
	}

	// 8. compactCaseTimeline error
	timelineDir := s.privateItemDir("case-timeline")
	_ = os.MkdirAll(timelineDir, 0700)
	hugeEvents := make([]CaseEvent, 405)
	for i := range hugeEvents {
		hugeEvents[i] = CaseEvent{ID: fmt.Sprintf("ev-%03d", i), Type: "note", Message: "note"}
	}
	hugeCase := c
	hugeCase.ID = "c-huge-ev"
	hugeCase.Timeline = hugeEvents
	_ = writeItem(casesDir, hugeCase.ID, hugeCase)
	if err := os.Chmod(timelineDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCaseEvent(ctx, hugeCase.ID, CaseEvent{Type: "note", Message: "note"}, "alice"); err == nil {
		t.Fatalf("expected error from compactCaseTimeline in AppendCaseEvent")
	}
	_ = os.Chmod(timelineDir, 0700)
	_ = os.Remove(filepath.Join(casesDir, hugeCase.ID+".json"))

	// 9. writeItem error (casesDir chmod 0500)
	if err := os.Chmod(casesDir, 0500); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendCaseEvent(ctx, c.ID, CaseEvent{Type: "note", Message: "note"}, "alice"); err == nil {
		t.Fatalf("expected writeItem error in AppendCaseEvent")
	}
	_ = os.Chmod(casesDir, 0700)

	// 10. auditMutation error
	failAuditStore := &failingAuditStore{failCase: true}
	sFail := *s
	sFail.audit = failAuditStore
	cBeforeFail := c
	if _, err := sFail.AppendCaseEvent(ctx, c.ID, CaseEvent{Type: "note"}, "alice"); err == nil {
		t.Fatalf("expected auditMutation error in AppendCaseEvent")
	}
	_ = writeItem(casesDir, c.ID, cBeforeFail)

	// 11. Happy path: ordinary AppendCaseEvent and link-evidence
	if _, err := s.AppendCaseEvent(ctx, c.ID, CaseEvent{Type: "note", Message: "hello"}, "alice"); err != nil {
		t.Fatalf("unexpected error appending note event: %v", err)
	}
	validRun2 := CheckRun{ID: "run-server-2", CheckID: "chk-2", CaseID: c.ID, Actor: "alice", Environment: "qa", StartedAt: time.Now().UTC()}
	_ = writeItem(runsDir, validRun2.ID, validRun2)
	_ = writeItem(cohortDir, validRun2.ID, runCohortProof{RunID: validRun2.ID, CaseID: c.ID, ContextDigest: cDigest})
	if _, err := s.AppendCaseEvent(ctx, c.ID, CaseEvent{Type: "link-evidence", Ref: validRun2.ID}, "alice"); err != nil {
		t.Fatalf("unexpected error appending link-evidence event: %v", err)
	}
}

// Not parallel: tests filesystem modifications on store dirs.
func TestCoverage100_Mutations_GetRun(t *testing.T) {
	s := testStore(t)
	runsDir := s.privateItemDir("runs")
	_ = os.MkdirAll(runsDir, 0700)

	// 1. Invalid slug
	if _, err := s.getRun("../bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad run slug: %v", err)
	}

	// 2. Not exist -> ErrNotFound
	if _, err := s.getRun("run-notexist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing run: %v", err)
	}

	// 3. safeReadFile error (directory in place of run file)
	_ = os.MkdirAll(filepath.Join(runsDir, "run-dir.json"), 0700)
	if _, err := s.getRun("run-dir"); err == nil {
		t.Fatalf("expected error reading directory run file")
	}
	_ = os.RemoveAll(filepath.Join(runsDir, "run-dir.json"))

	// 4. json.Unmarshal error (corrupt JSON)
	_ = os.WriteFile(filepath.Join(runsDir, "run-corrupt.json"), []byte("{"), 0600)
	if _, err := s.getRun("run-corrupt"); err == nil {
		t.Fatalf("expected error from json.Unmarshal on corrupt run")
	}
	_ = os.Remove(filepath.Join(runsDir, "run-corrupt.json"))

	// 5. run.ID != id -> ErrInvalid
	_ = writeItem(runsDir, "run-mismatch", CheckRun{ID: "other-id"})
	if _, err := s.getRun("run-mismatch"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid on run ID mismatch: %v", err)
	}
	_ = os.Remove(filepath.Join(runsDir, "run-mismatch.json"))

	// 6. Happy path
	valid := CheckRun{ID: "run-ok", CheckID: "c1"}
	_ = writeItem(runsDir, "run-ok", valid)
	got, err := s.getRun("run-ok")
	if err != nil {
		t.Fatalf("unexpected error getting run: %v", err)
	}
	if got.ID != "run-ok" {
		t.Fatalf("expected run-ok, got: %s", got.ID)
	}
}
