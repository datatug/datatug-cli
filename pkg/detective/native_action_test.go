package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func nativeCaptureFixture(t *testing.T, withCase bool) (*LocalStore, NativeCapture, sessioncontext.DetectiveFence) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.SaveCheck(ctx, Check{ID: "check", Title: "Check", Steps: []Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice"); err != nil {
		t.Fatal(err)
	}
	caseID := ""
	if withCase {
		caseID = "case"
		if _, err := s.SaveCase(ctx, Case{ID: caseID, Title: "Case"}, "alice", "create-case"); err != nil {
			t.Fatal(err)
		}
	}
	req := NativeCaptureRequest{CheckID: "check", CaseID: caseID, NativeOperationID: "native", Actor: "alice", Reference: apicontract.SessionInputReference{SchemaVersion: 1, StorageID: "local", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns", SessionID: "session", InputSnapshotID: "input", ContextSetID: "context", EnvironmentID: "qa", IdentityDomain: "tenant"}}
	capture, err := s.CaptureNativeInput(ctx, req, func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: "q", Revision: "old", DefinitionJSON: json.RawMessage(`{"id":"q","title":"Query","type":"SQL","text":"select old"}`)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	id := sessioncontext.DetectiveIdentity{Namespace: sessioncontext.NativeNamespace{StorageID: "local", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}, Actor: "alice", SessionID: "session", RunID: "session-run", NativeStoreID: "local", NativeProjectID: "p", NativeOperationID: "native", Digest: capture.Binding.PayloadDigest}
	fence := sessioncontext.DetectiveFence{Identity: id, Generation: 1}
	return s, capture, fence
}

func TestNativeCaptureCaseChangesRefuseSourceAcceptance(t *testing.T) {
	for _, status := range []string{"active", "archived"} {
		t.Run(status, func(t *testing.T) {
			s, capture, fence := nativeCaptureFixture(t, true)
			if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
				t.Fatal(err)
			}
			item := *capture.Case
			item.Title = "Changed"
			if status == "archived" {
				item.Status, item.ClosureReason = "closed", "Reviewed"
				var err error
				item, err = s.SaveCase(context.Background(), item, "alice", "update-case")
				if err != nil {
					t.Fatal(err)
				}
			}
			item.Status = status
			if _, err := s.SaveCase(context.Background(), item, "alice", "update-case"); err != nil {
				t.Fatal(err)
			}
			calls := 0
			runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
				calls++
				return apicontract.Result{}, nil
			}
			if _, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, runner, nil); !errors.Is(err, ErrConflict) {
				t.Fatal("changed Case accepted", err)
			}
			if calls != 0 {
				t.Fatal("source ran after Case changed")
			}
			if _, err := s.NativeObservation(context.Background(), fence.Identity); !errors.Is(err, sessioncontext.ErrNotFound) {
				t.Fatal("rejected Case created native acceptance", err)
			}
		})
	}
}

func TestNativeSourceOutsideAdmissionAndFencedProducedRecovery(t *testing.T) {
	s, capture, fence := nativeCaptureFixture(t, true)
	var revoked atomic.Bool
	if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error {
		if revoked.Load() {
			return sessioncontext.ErrReadOnly
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	calls := 0
	runner := func(ctx context.Context, id, _ string, _ map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		query, ok := NativeAcceptedQuery(ctx, id)
		if !ok || !strings.Contains(string(query.DefinitionJSON), "select old") {
			return apicontract.Result{}, ErrInvalid
		}
		close(started)
		<-release
		return apicontract.Result{}, nil
	}
	go func() {
		_, err := s.ExecuteNativePin(context.Background(), fence, capture.Pin, true, runner, nil)
		finished <- err
	}()
	<-started
	lockctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.withLock(lockctx, func() error { revoked.Store(true); return nil }); err != nil {
		t.Fatal("source held admission and blocked Stop", err)
	}
	close(release)
	if err := <-finished; !errors.Is(err, sessioncontext.ErrReadOnly) {
		t.Fatal("fenced result finalized", err)
	}
	observed, err := s.NativeObservation(context.Background(), fence.Identity)
	if err != nil || !observed.Produced || observed.Done {
		t.Fatal("known result lost", observed, err)
	}
	if _, err = s.ExecuteNativePin(context.Background(), fence, capture.Pin, false, runner, nil); !errors.Is(err, sessioncontext.ErrReadOnly) {
		t.Fatal("fenced recovery wrote Case", err)
	}
	if calls != 1 {
		t.Fatal("known result reran source")
	}
	item, err := s.GetCase(context.Background(), capture.Case.ID)
	if err != nil || len(item.RunRefs) != 0 {
		t.Fatal("fenced Case changed", item, err)
	}
}

func TestNativeCompletedLinkAndHandoffRecoverWithoutSource(t *testing.T) {
	s, capture, fence := nativeCaptureFixture(t, true)
	ctx := context.Background()
	if err := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		return apicontract.Result{}, nil
	}
	response, err := s.ExecuteNativePin(ctx, fence, capture.Pin, true, runner, nil)
	if err != nil || response.Run == nil {
		t.Fatal("native completion", err)
	}
	observed, err := s.NativeObservation(ctx, fence.Identity)
	if err != nil || !observed.Produced || !observed.Done || observed.CaseLinkStatus != "linked" {
		t.Fatal("native attestation", observed, err)
	}
	linkData, err := safeReadFile(s.privateItemDir("case-run-links"), nativeCaseRunLinkID(capture.Case.ID, response.Run.ID))
	if err != nil {
		t.Fatal("authoritative direct CaseRunLink absent", err)
	}
	var link core.CaseRunLink
	if json.Unmarshal(linkData, &link) != nil || link.CaseID != capture.Case.ID || link.RunID != response.Run.ID {
		t.Fatal("wrong direct link", string(linkData))
	}
	handoff := sessioncontext.EvidenceHandoff{ID: fence.Identity.RunID, SessionID: fence.Identity.SessionID, RunID: fence.Identity.RunID, InputSnapshotID: capture.Pin.Reference.InputSnapshotID, CaseID: capture.Case.ID, DetectiveRunID: response.Run.ID, State: "pending"}
	allow := func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error { return nil }
	receipt, err := s.DeliverNativeHandoff(ctx, fence.Identity, handoff, capture.Pin.Reference, allow)
	if err != nil || receipt == "" {
		t.Fatal("handoff", err)
	}
	if replay, err := s.DeliverNativeHandoff(ctx, fence.Identity, handoff, capture.Pin.Reference, allow); err != nil || replay != receipt {
		t.Fatal("handoff replay", replay, err)
	}
	if _, err = s.DeliverNativeHandoff(ctx, fence.Identity, handoff, capture.Pin.Reference, func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error {
		return sessioncontext.ErrForbidden
	}); !errors.Is(err, sessioncontext.ErrForbidden) {
		t.Fatal("handoff skipped current authorization", err)
	}
	if _, err = s.ExecuteNativePin(ctx, fence, capture.Pin, false, runner, nil); err != nil {
		t.Fatal("known result replay", err)
	}
	if calls != 1 {
		t.Fatal("recovery repeated source")
	}
}

func TestNativeProducedReconciliationNeverDispatches(t *testing.T) {
	s, capture, fence := nativeCaptureFixture(t, true)
	ctx := context.Background()
	allow := true
	if e := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error {
		if !allow {
			return sessioncontext.ErrReadOnly
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.FinalizeNativeProduced(ctx, fence.Identity); !errors.Is(e, sessioncontext.ErrNotFound) {
		t.Fatal(e)
	}
	calls := 0
	_, e := s.ExecuteNativePin(ctx, fence, capture.Pin, true, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		allow = false
		return apicontract.Result{}, nil
	}, nil)
	if !errors.Is(e, sessioncontext.ErrReadOnly) {
		t.Fatal(e)
	}
	if _, e = s.FinalizeNativeProduced(ctx, fence.Identity); !errors.Is(e, sessioncontext.ErrReadOnly) {
		t.Fatal("fenced reconcile finalized", e)
	}
	allow = true // transient native-owner gate failure, not a session generation reset.
	response, e := s.FinalizeNativeProduced(ctx, fence.Identity)
	if e != nil || response.Run == nil {
		t.Fatal(response, e)
	}
	if _, e = s.FinalizeNativeProduced(ctx, fence.Identity); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatal("reconcile evaluated source", calls)
	}
	var op actionOperation
	data, e := safeReadFile(s.privateItemDir("action-operations"), fence.Identity.NativeOperationID)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(data, &op); e != nil {
		t.Fatal(e)
	}
	op.Produced = false
	if e = writeDurableItem(s.privateItemDir("action-operations"), fence.Identity.NativeOperationID, op); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FinalizeNativeProduced(ctx, fence.Identity); !errors.Is(e, ErrOutcomeUncertain) {
		t.Fatal(e)
	}
	op.Native = nil
	if e = writeDurableItem(s.privateItemDir("action-operations"), fence.Identity.NativeOperationID, op); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FinalizeNativeProduced(ctx, fence.Identity); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}

func TestNativeDependencyQueryRechecksDurableFence(t *testing.T) {
	s, _, _ := nativeCaptureFixture(t, false)
	ctx := context.Background()
	if _, e := s.SaveCheck(ctx, Check{ID: "check", Version: 1, Title: "Two sources", Steps: []Step{{ID: "first", Kind: "query", QueryID: "q"}, {ID: "second", Kind: "query", QueryID: "q"}}}, "alice"); e != nil {
		t.Fatal(e)
	}
	req := NativeCaptureRequest{CheckID: "check", NativeOperationID: "native", Actor: "alice", Reference: apicontract.SessionInputReference{SchemaVersion: 1, StorageID: "local", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns", SessionID: "session", InputSnapshotID: "input", ContextSetID: "context", EnvironmentID: "qa", IdentityDomain: "tenant"}}
	capture, e := s.CaptureNativeInput(ctx, req, func(context.Context, string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: "q", Revision: "old", DefinitionJSON: json.RawMessage(`{"id":"q","title":"Query","type":"SQL","text":"select 1"}`)}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	fence := sessioncontext.DetectiveFence{Identity: sessioncontext.DetectiveIdentity{Namespace: sessioncontext.NativeNamespace{StorageID: "local", DatabaseID: "db", ProjectID: "p", NamespaceID: "ns"}, Actor: "alice", SessionID: "session", RunID: "session-run", NativeStoreID: "local", NativeProjectID: "p", NativeOperationID: "native", Digest: capture.Binding.PayloadDigest}, Generation: 1}
	stopped := false
	if e = s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error {
		if stopped {
			return sessioncontext.ErrReadOnly
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	_, e = s.ExecuteNativePin(ctx, fence, capture.Pin, true, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		calls++
		stopped = true
		return apicontract.Result{}, nil
	}, nil)
	if !errors.Is(e, sessioncontext.ErrReadOnly) || calls != 1 {
		t.Fatal("dependency ran after fence", calls, e)
	}
}

func TestNativeScrubMetadataReceiptFaults(t *testing.T) {
	for _, kind := range []string{"missing-operation", "operation-read", "invalid-operation", "missing-reference", "reference-read", "invalid-reference", "unknown-availability", "sync-failure", "success"} {
		t.Run(kind, func(t *testing.T) {
			s, capture, fence := nativeCaptureFixture(t, true)
			ctx := context.Background()
			if e := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); e != nil {
				t.Fatal(e)
			}
			if _, e := s.ExecuteNativePin(ctx, fence, capture.Pin, true, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
				return apicontract.Result{}, nil
			}, nil); e != nil {
				t.Fatal(e)
			}
			var op actionOperation
			operationDir := s.privateItemDir("action-operations")
			data, e := safeReadFile(operationDir, fence.Identity.NativeOperationID)
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(data, &op); e != nil {
				t.Fatal(e)
			}
			receiptID := "handoff-" + nativeCaseRunLinkID(op.CaseID, op.ID)
			dir := s.privateItemDir("session-evidence-handoffs")
			receipt := nativeSessionEvidenceReceipt{Identity: fence.Identity, CaseID: op.CaseID, RunID: op.ID, InputReference: capture.Pin.Reference, Retention: "session-owned-reference", Availability: "available"}
			if e = writeDurableItem(dir, receiptID, receipt); e != nil {
				t.Fatal(e)
			}
			oldSync := writeItemSyncDir
			t.Cleanup(func() { writeItemSyncDir = oldSync })
			switch kind {
			case "missing-operation":
				e = os.Remove(filepath.Join(operationDir, fence.Identity.NativeOperationID+".json"))
			case "operation-read":
				e = os.Remove(filepath.Join(operationDir, fence.Identity.NativeOperationID+".json"))
				if e == nil {
					e = os.Symlink("missing", filepath.Join(operationDir, fence.Identity.NativeOperationID+".json"))
				}
			case "invalid-operation":
				op.Native = nil
				e = writeDurableItem(operationDir, fence.Identity.NativeOperationID, op)
			case "missing-reference":
				e = os.Remove(filepath.Join(dir, receiptID+".json"))
			case "reference-read":
				e = os.Remove(filepath.Join(dir, receiptID+".json"))
				if e == nil {
					e = os.Symlink("missing", filepath.Join(dir, receiptID+".json"))
				}
			case "invalid-reference":
				receipt.RunID = "foreign"
				e = writeDurableItem(dir, receiptID, receipt)
			case "unknown-availability":
				receipt.Availability = "unknown"
				e = writeDurableItem(dir, receiptID, receipt)
			case "sync-failure":
				writeItemSyncDir = func(string) error { return errors.New("sync failure") }
			}
			if e != nil {
				t.Fatal(e)
			}
			changed, e := s.MarkSessionReferenceUnavailable(ctx, fence.Identity)
			switch kind {
			case "missing-operation", "missing-reference":
				if e != nil || changed {
					t.Fatal(changed, e)
				}
			case "success":
				if e != nil || !changed {
					t.Fatal(changed, e)
				}
				if changed, e = s.MarkSessionReferenceUnavailable(ctx, fence.Identity); e != nil || changed {
					t.Fatal(changed, e)
				}
			default:
				if e == nil || changed {
					t.Fatal("invalid scrub acknowledged", changed, e)
				}
			}
		})
	}
}

func TestNativeFinalizerUnreadableJournalAndArchivedHandoff(t *testing.T) {
	s, capture, fence := nativeCaptureFixture(t, true)
	ctx := context.Background()
	if e := s.ConfigureNativeFence(func(context.Context, sessioncontext.DetectiveFence) error { return nil }); e != nil {
		t.Fatal(e)
	}
	response, e := s.ExecuteNativePin(ctx, fence, capture.Pin, true, func(context.Context, string, string, map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		return apicontract.Result{}, nil
	}, nil)
	if e != nil {
		t.Fatal(e)
	}
	item, e := s.GetCase(ctx, "case")
	if e != nil {
		t.Fatal(e)
	}
	item.Status = "closed"
	item.ClosureReason = "Reviewed"
	item, e = s.SaveCase(ctx, item, "alice", "update-case")
	if e != nil {
		t.Fatal(e)
	}
	item.Status = "archived"
	if _, e = s.SaveCase(ctx, item, "alice", "update-case"); e != nil {
		t.Fatal(e)
	}
	handoff := sessioncontext.EvidenceHandoff{ID: fence.Identity.RunID, SessionID: fence.Identity.SessionID, RunID: fence.Identity.RunID, InputSnapshotID: capture.Pin.Reference.InputSnapshotID, CaseID: "case", DetectiveRunID: response.Run.ID, State: "pending"}
	if _, e = s.DeliverNativeHandoff(ctx, fence.Identity, handoff, capture.Pin.Reference, func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error { return nil }); !errors.Is(e, sessioncontext.ErrReadOnly) {
		t.Fatal("archived Case got handoff", e)
	}
	path := filepath.Join(s.privateItemDir("action-operations"), fence.Identity.NativeOperationID+".json")
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink("missing", path); e != nil {
		t.Fatal(e)
	}
	if _, e = s.FinalizeNativeProduced(ctx, fence.Identity); e == nil || errors.Is(e, sessioncontext.ErrNotFound) {
		t.Fatal("unsafe journal treated as absent", e)
	}
}
