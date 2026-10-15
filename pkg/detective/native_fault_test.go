package detective

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func nativeNoSource(t *testing.T) QueryRunner {
	t.Helper()
	return func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		t.Error("invalid or uncertain native authority executed source")
		return apicontract.Result{}, nil
	}
}

func TestNativeAdmissionRejectsMalformedAndUncertainAuthority(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"no-checker", "no-runner", "invalid-definition", "invalid-reference", "wrong-digest", "absent-not-first", "corrupt-journal", "legacy-journal", "querying-journal", "accepted-not-first", "unavailable-catalog", "invalid-receipt"} {
		t.Run(kind, func(t *testing.T) {
			s, capture, fence := nativeCaptureFixture(t, false)
			if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
				t.Fatal(err)
			}
			first := true
			runner := nativeNoSource(t)
			oldLoad, oldReceipt := nativeLoadCatalog, actionNewOperationReceipt
			defer func() { nativeLoadCatalog = oldLoad; actionNewOperationReceipt = oldReceipt }()
			switch kind {
			case "no-checker":
				s.nativeFence = nil
			case "no-runner":
				runner = nil
			case "invalid-definition":
				capture.Pin.Definition.SchemaVersion = 99
			case "invalid-reference":
				capture.Pin.Reference.EnvironmentID = "other"
			case "wrong-digest":
				fence.Identity.Digest = "wrong"
			case "absent-not-first":
				first = false
			case "unavailable-catalog":
				nativeLoadCatalog = func(context.Context, string, string, core.CatalogReader) (core.Catalog, error) {
					return core.Catalog{}, ErrNotFound
				}
			case "invalid-receipt":
				actionNewOperationReceipt = func(string, core.OperationBinding, int64, time.Time) (core.OperationReceipt, error) {
					return core.OperationReceipt{}, ErrInvalid
				}
			default:
				receipt, err := core.NewOperationReceipt("native", capture.Binding, 0, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				op := actionOperation{Receipt: receipt, Kind: "run", ID: "run", Native: &nativeActionAuthority{Fence: fence, Phase: "querying"}}
				switch kind {
				case "legacy-journal":
					op.Native = nil
				case "accepted-not-first":
					op.Native.Phase = "accepted"
					first = false
				}
				if err = writeItem(s.privateItemDir("action-operations"), "native", op); err != nil {
					t.Fatal(err)
				}
				if kind == "corrupt-journal" {
					if err = os.WriteFile(filepath.Join(s.privateItemDir("action-operations"), "native.json"), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.ExecuteNativePin(ctx, fence, capture.Pin, first, runner, nil); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	s, _, _ := nativeCaptureFixture(t, false)
	if err := s.ConfigureNativeFence(nil); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal("nil gate accepted", err)
	}
	old := nativePlatformAdmission
	defer func() { nativePlatformAdmission = old }()
	nativePlatformAdmission = func() error { return sessioncontext.ErrUnavailable }
	if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal("unsupported durability platform activated", err)
	}
}

func TestNativeJournalDurabilityFailuresNeverReplayQuery(t *testing.T) {
	for _, failAt := range []int{1, 3, 5} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			s, capture, fence := nativeCaptureFixture(t, false)
			if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
				t.Fatal(err)
			}
			failed := errors.New("directory durability fault")
			old := writeItemSyncDir
			defer func() { writeItemSyncDir = old }()
			syncs, calls := 0, 0
			writeItemSyncDir = func(path string) error {
				syncs++
				if syncs == failAt {
					return failed
				}
				return old(path)
			}
			runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
				calls++
				return apicontract.Result{}, nil
			}
			if _, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, runner, nil); !errors.Is(err, failed) {
				t.Fatal("durability fault escaped", err)
			}
			expected := 0
			if failAt == 5 {
				expected = 1
			}
			if calls != expected {
				t.Fatal("source before durable querying phase", calls)
			}
			writeItemSyncDir = old
			_, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, false, runner, nil)
			if failAt != 5 && !errors.Is(err, ErrOutcomeUncertain) {
				t.Fatal("uncertain acceptance became dispatchable", err)
			}
			if calls != expected {
				t.Fatal("uncertain query replayed")
			}
		})
	}
}

func TestNativeObservationRejectsCorruptProofs(t *testing.T) {
	s, capture, fence := nativeCaptureFixture(t, false)
	ctx := context.Background()
	if _, err := s.NativeObservation(ctx, fence.Identity); !errors.Is(err, sessioncontext.ErrNotFound) {
		t.Fatal(err)
	}
	receipt, err := core.NewOperationReceipt("native", capture.Binding, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"wrong-identity", "missing-result"} {
		op := actionOperation{Receipt: receipt, ID: "run", Native: &nativeActionAuthority{Fence: fence}, Produced: true}
		if kind == "wrong-identity" {
			op.Native.Fence.Identity.Actor = "bob"
		}
		if err = writeItem(s.privateItemDir("action-operations"), "native", op); err != nil {
			t.Fatal(err)
		}
		if _, err = s.NativeObservation(ctx, fence.Identity); !errors.Is(err, ErrConflict) {
			t.Fatal(kind, err)
		}
	}
	if err = os.Remove(filepath.Join(s.privateItemDir("action-operations"), "native.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(s.privateItemDir("action-operations"), "native.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.NativeObservation(ctx, fence.Identity); err == nil {
		t.Fatal("directory accepted as journal")
	}
	if _, err = s.ExecuteNativePin(ctx, fence, capture.Pin, true, nativeNoSource(t), nil); err == nil {
		t.Fatal("missing gate accepted")
	}
	if err = s.checkNativeAction(ctx, "native", &actionOperation{}); !errors.Is(err, sessioncontext.ErrUnavailable) {
		t.Fatal(err)
	}
	s.nativeFence = func(context.Context, sessioncontext.DetectiveFence) error { return nil }
	if err = s.checkNativeAction(ctx, "foreign", &actionOperation{Native: &nativeActionAuthority{Fence: fence}}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestNativeSourceBoundaryAndFinalizationFaultsRetainUncertainty(t *testing.T) {
	for _, kind := range []string{"binding-fault", "evaluation-fault", "journal-disappears", "journal-corrupted", "final-gate", "first-gate", "second-gate", "case-digest-fault"} {
		t.Run(kind, func(t *testing.T) {
			s, capture, fence := nativeCaptureFixture(t, true)
			failed := errors.New("native owner boundary fault")
			oldBind, oldEvaluate, oldDigest := nativeBindOperation, nativeEvaluateCheck, caseContextDigest
			defer func() {
				nativeBindOperation = oldBind
				nativeEvaluateCheck = oldEvaluate
				caseContextDigest = oldDigest
			}()
			checks := 0
			if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error {
				checks++
				if kind == "first-gate" && checks == 1 || kind == "second-gate" && checks == 2 || kind == "final-gate" && checks == 4 {
					return failed
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "binding-fault":
				nativeBindOperation = func(string, string, string, string, any) (core.OperationBinding, error) {
					return core.OperationBinding{}, failed
				}
			case "evaluation-fault":
				nativeEvaluateCheck = func(context.Context, core.RunRequest, core.Catalog, core.QueryRunner, core.ExecutionMetadata) (core.CheckRun, error) {
					return core.CheckRun{}, failed
				}
			case "case-digest-fault":
				caseContextDigest = func(map[string]InputValue) (string, error) { return "", failed }
			}
			calls := 0
			runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
				calls++
				path := filepath.Join(s.privateItemDir("action-operations"), "native.json")
				switch kind {
				case "journal-disappears":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "journal-corrupted":
					if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return apicontract.Result{}, nil
			}
			if _, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, runner, nil); err == nil {
				t.Fatal("boundary failure acknowledged")
			}
			expected := 0
			switch kind {
			case "journal-disappears", "journal-corrupted", "final-gate":
				expected = 1
			}
			if calls != expected {
				t.Fatal("unexpected source calls", calls, expected)
			}
		})
	}
}

func completedNativeFixture(t *testing.T) (*LocalStore, NativeCapture, sessioncontext.DetectiveFence, actionOperation) {
	t.Helper()
	s, capture, fence := nativeCaptureFixture(t, true)
	if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}
	if _, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, runner, nil); err != nil {
		t.Fatal(err)
	}
	data, err := safeReadFile(s.privateItemDir("action-operations"), "native")
	if err != nil {
		t.Fatal(err)
	}
	var op actionOperation
	if json.Unmarshal(data, &op) != nil {
		t.Fatal("invalid fixture journal")
	}
	return s, capture, fence, op
}

func TestNativeHandoffRejectsUnavailableAndMismatchedEvidence(t *testing.T) {
	for _, kind := range []string{"nil-gate", "wrong-reference", "missing-journal", "invalid-journal", "missing-link", "wrong-link", "missing-case"} {
		t.Run(kind, func(t *testing.T) {
			s, capture, fence, op := completedNativeFixture(t)
			handoff := sessioncontext.EvidenceHandoff{SessionID: fence.Identity.SessionID, RunID: fence.Identity.RunID, InputSnapshotID: capture.Pin.Reference.InputSnapshotID, CaseID: op.CaseID, DetectiveRunID: op.ID}
			gate := func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error { return nil }
			switch kind {
			case "nil-gate":
				gate = nil
			case "wrong-reference":
				handoff.InputSnapshotID = "foreign"
			case "missing-journal":
				if err := os.Remove(filepath.Join(s.privateItemDir("action-operations"), "native.json")); err != nil {
					t.Fatal(err)
				}
			case "invalid-journal":
				op.Done = false
				if err := writeItem(s.privateItemDir("action-operations"), "native", op); err != nil {
					t.Fatal(err)
				}
			case "missing-link":
				if err := os.Remove(filepath.Join(s.privateItemDir("case-run-links"), nativeCaseRunLinkID(op.CaseID, op.ID)+".json")); err != nil {
					t.Fatal(err)
				}
			case "wrong-link":
				if err := writeItem(s.privateItemDir("case-run-links"), nativeCaseRunLinkID(op.CaseID, op.ID), core.CaseRunLink{CaseID: "foreign"}); err != nil {
					t.Fatal(err)
				}
			case "missing-case":
				if err := os.Remove(filepath.Join(s.privateItemDir("cases"), op.CaseID+".json")); err != nil {
					t.Fatal(err)
				}
			}
			if receipt, err := s.DeliverNativeHandoff(context.Background(), fence.Identity, handoff, capture.Pin.Reference, gate); err == nil || receipt != "" {
				t.Fatal("bad evidence handoff acknowledged", receipt, err)
			}
		})
	}
}

func TestNativeEvidenceDurabilityAndCorruptionFaultsFailClosed(t *testing.T) {
	for _, kind := range []string{"gate", "private-directory", "cohort-directory", "link-directory", "corrupt-link", "unreadable-link"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, op := completedNativeFixture(t)
			oldSync := writeItemSyncDir
			defer func() { writeItemSyncDir = oldSync }()
			failed := errors.New("evidence durability fault")
			target := ""
			switch kind {
			case "gate":
				s.nativeFence = func(context.Context, sessioncontext.DetectiveFence) error { return failed }
			case "private-directory":
				target = s.privateDir
			case "cohort-directory":
				target = s.privateItemDir("run-cohorts")
			case "link-directory":
				target = s.privateItemDir("case-run-links")
			case "corrupt-link":
				if err := writeItem(s.privateItemDir("case-run-links"), nativeCaseRunLinkID(op.CaseID, op.ID), core.CaseRunLink{CaseID: "foreign"}); err != nil {
					t.Fatal(err)
				}
			case "unreadable-link":
				path := filepath.Join(s.privateItemDir("case-run-links"), nativeCaseRunLinkID(op.CaseID, op.ID)+".json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if target != "" {
				writeItemSyncDir = func(path string) error {
					if path == target {
						return failed
					}
					return oldSync(path)
				}
			}
			if err := s.finishNativeEvidence(context.Background(), "native", &op); err == nil {
				t.Fatal("evidence failure acknowledged")
			}
			if kind == "corrupt-link" {
				if err := s.finishAction(context.Background(), "native", &op); err == nil {
					t.Fatal("native finalizer acknowledged corrupt evidence")
				}
			}
		})
	}
	s, _, fence, op := completedNativeFixture(t)
	dir := t.TempDir()
	if err := s.finishNativeActionFile(&actionOperation{}, dir, "legacy", nil, map[string]string{"value": "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.finishNativeActionFile(&op, dir, "legacy", nil, map[string]string{"value": "different"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	oldSync := writeItemSyncDir
	defer func() { writeItemSyncDir = oldSync }()
	failed := errors.New("materialized directory sync fault")
	writeItemSyncDir = func(string) error { return failed }
	if err := s.finishNativeActionFile(&op, dir, "new", nil, map[string]string{"value": "new"}); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if err := syncPrivateDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory sync acknowledged")
	}
	writeItemSyncDir = oldSync
	pin := sessioncontext.DetectiveInputPin{Dispatch: sessioncontext.DetectiveDispatchBinding{CaseID: "missing"}}
	if err := s.checkNativeDispatchCase(pin); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	op.Response.Run = nil
	if err := writeItem(s.privateItemDir("action-operations"), "native", op); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NativeObservation(context.Background(), fence.Identity); !errors.Is(err, ErrConflict) {
		t.Fatal("missing produced result attested", err)
	}
}

func TestNativeAcceptedContinuationRechecksCaseAndNeverManufacturesProof(t *testing.T) {
	s, capture, fence := nativeCaptureFixture(t, true)
	if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	oldSync := writeItemSyncDir
	failed := errors.New("acceptance directory sync lost")
	writeItemSyncDir = func(string) error { return failed }
	_, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, nativeNoSource(t), nil)
	writeItemSyncDir = oldSync
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	item := *capture.Case
	item.Title = "Changed after native acceptance"
	if _, err = s.SaveCase(context.Background(), item, "alice", "update-case"); err != nil {
		t.Fatal(err)
	}
	// Only the original still-owned first-claim token can enter this durable
	// accepted phase. Even that token cannot execute after the Case changed.
	if _, err = s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, nativeNoSource(t), nil); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted continuation ignored Case version", err)
	}
	broken := capture.Pin
	broken.Reference.DefinitionDigest = "wrong"
	if _, err = s.ExecuteNativePin(context.Background(), fence, broken, true, nativeNoSource(t), nil); err == nil {
		t.Fatal("unbound input reference accepted")
	}
	if err = os.Remove(filepath.Join(s.privateItemDir("action-operations"), "native.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(s.privateItemDir("action-operations"), "native.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, nativeNoSource(t), nil); err == nil {
		t.Fatal("unreadable acceptance treated as absent")
	}
}

func TestNativeObservationBindsProducedReferenceNamespace(t *testing.T) {
	s, _, fence, op := completedNativeFixture(t)
	op.Response.Run.InputReference.NamespaceID = "foreign"
	if err := writeItem(s.privateItemDir("action-operations"), "native", op); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NativeObservation(context.Background(), fence.Identity); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign private pin reference attested", err)
	}
}
