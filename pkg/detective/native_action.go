package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"os"
	"path/filepath"
	"reflect"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

var nativeLoadCatalog = core.LoadCatalog
var nativeBindOperation = core.BindOperation
var nativeEvaluateCheck = core.EvaluateCheck
var nativePlatformAdmission = nativeBridgePlatformAdmission

type nativeSessionEvidenceReceipt struct {
	Identity       sessioncontext.DetectiveIdentity  `json:"identity"`
	CaseID         string                            `json:"caseId"`
	RunID          string                            `json:"runId"`
	InputReference apicontract.SessionInputReference `json:"inputReference"`
	Retention      string                            `json:"retention"`
	Availability   string                            `json:"availability"`
}

type nativeActionAuthority struct {
	Fence sessioncontext.DetectiveFence `json:"fence"`
	Phase string                        `json:"phase"`
}

// ConfigureNativeFence is a trusted host boundary. The checker reads the
// session owner's durable fence while native admission is held; its repository
// View must not acquire admission again. Never expose this as client transport.
func (s *LocalStore) ConfigureNativeFence(check func(context.Context, sessioncontext.DetectiveFence) error) error {
	if check == nil {
		return sessioncontext.ErrUnavailable
	}
	if err := nativePlatformAdmission(); err != nil {
		return err
	}
	s.nativeFence = check
	return nil
}

func (s *LocalStore) checkNativeDispatchCase(pin sessioncontext.DetectiveInputPin) error {
	d := pin.Dispatch
	if d.CaseID == "" {
		return nil
	}
	current, err := s.getCase(d.CaseID)
	if err != nil {
		return err
	}
	digest, err := caseContextDigest(current.Context)
	if err != nil {
		return err
	}
	if current.Status != "active" || current.Version != d.CaseVersion || digest != d.CaseContextDigest {
		return ErrConflict
	}
	return nil
}

func (s *LocalStore) checkNativeAction(ctx context.Context, id string, op *actionOperation) error {
	authority := op.Native
	if authority == nil || s.nativeFence == nil {
		return sessioncontext.ErrUnavailable
	}
	identity := authority.Fence.Identity
	if identity.Actor != op.Receipt.Actor || identity.NativeProjectID != s.projectID || identity.NativeStoreID != "local" || identity.NativeOperationID != id || identity.Digest != op.Receipt.PayloadDigest || authority.Fence.Generation < 1 {
		return ErrConflict
	}
	return s.nativeFence(ctx, authority.Fence)
}

// NativeObservation is authoritative journal lookup only. It never runs a
// source, claims work, or infers that absence makes dispatch safe.
func (s *LocalStore) NativeObservation(ctx context.Context, id sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error) {
	var out sessioncontext.DetectiveObservation
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.privateItemDir("action-operations"), id.NativeOperationID)
		if errors.Is(err, os.ErrNotExist) {
			return sessioncontext.ErrNotFound
		}
		if err != nil {
			return err
		}
		var op actionOperation
		if json.Unmarshal(data, &op) != nil || op.Native == nil || op.Native.Fence.Identity != id || op.Receipt.Actor != id.Actor || op.Receipt.Project != s.projectID || op.Receipt.PayloadDigest != id.Digest || op.Receipt.ID != id.NativeOperationID || op.Receipt.Validate() != nil || op.Receipt.Action != "run-check" || op.Kind != "run" || op.ID != actionRunID(s.projectID, id.Actor, id.NativeOperationID) || op.Done && !op.Produced {
			return ErrConflict
		}
		out = sessioncontext.DetectiveObservation{Identity: id, FenceGeneration: op.Native.Fence.Generation, SessionGeneration: op.Native.Fence.SessionGeneration, Produced: op.Produced, Done: op.Done, CaseLinkStatus: op.CaseLinkStatus}
		if op.Produced {
			if op.Response.Run == nil || op.Response.Run.ID != op.ID || op.Response.Run.CheckID != op.Receipt.ResourceID || op.Response.Run.CaseID != op.CaseID || op.Response.Run.Actor != id.Actor || op.Response.Run.InputReference == nil {
				return ErrConflict
			}
			r := op.Response.Run.InputReference
			if (sessioncontext.NativeNamespace{StorageID: r.StorageID, SpaceID: r.SpaceID, DatabaseID: r.DatabaseID, ProjectID: r.ProjectID, NamespaceID: r.NamespaceID}) != id.Namespace || r.SessionID != id.SessionID {
				return ErrConflict
			}
			out.NativeRunID = op.ID
		}
		if op.Done {
			out.NativeReceiptID = id.NativeOperationID
			out.State = "completed"
		}
		return nil
	})
	return out, err
}

// ExecuteNativePin is called only with the sealed Core pin and first-claim
// discriminator. Recovery with first=false cannot manufacture native acceptance.
// The source runs outside admission; every finalization path rechecks the fence.
func (s *LocalStore) ExecuteNativePin(ctx context.Context, fence sessioncontext.DetectiveFence, pin sessioncontext.DetectiveInputPin, first bool, runner QueryRunner, metadata ExecutionMetadata) (ActionResponse, error) {
	var empty ActionResponse
	if runner == nil || s.nativeFence == nil {
		return empty, sessioncontext.ErrUnavailable
	}
	definition, digest, err := sessioncontext.CanonicalDetectiveDefinition(ctx, pin.Definition)
	if err != nil || digest != pin.Dispatch.DefinitionDigest {
		return empty, ErrInvalid
	}
	d := pin.Dispatch
	req := core.ActionRequest{Type: d.Action, Project: d.NativeProjectID, CheckID: d.CheckID, CaseID: d.CaseID, Environment: pin.Reference.EnvironmentID, Inputs: pin.EffectiveInputs, InputReference: &pin.Reference, ExpectedDefinitionDigest: d.DefinitionDigest, ExpectedCaseContextDigest: d.CaseContextDigest}
	payload, err := core.ActionOperationPayload(req)
	if err != nil {
		return empty, err
	}
	binding, err := nativeBindOperation(s.projectID, fence.Identity.Actor, d.Action, d.CheckID, payload)
	if err != nil || binding.PayloadDigest != fence.Identity.Digest || d.NativeOperationID != fence.Identity.NativeOperationID {
		return empty, ErrConflict
	}
	catalog, err := nativeLoadCatalog(ctx, definition.Check.ID, "", pinnedNativeCatalog{definition})
	if err != nil {
		return empty, err
	}
	var op actionOperation
	runID := actionRunID(s.projectID, fence.Identity.Actor, d.NativeOperationID)
	dispatch := false
	err = s.withLock(ctx, func() error {
		data, readErr := safeReadFile(s.privateItemDir("action-operations"), d.NativeOperationID)
		if readErr == nil {
			if json.Unmarshal(data, &op) != nil || op.Native == nil || op.Native.Fence != fence || core.CheckOperationReplay(op.Receipt, binding) != nil {
				return ErrConflict
			}
			if op.Produced {
				return s.finishAction(ctx, d.NativeOperationID, &op)
			}
			// No replay after a querying/uncertain phase. Only a durable not-started
			// native acceptance can continue after a crash, under the same fence.
			if !first || op.Native.Phase != "accepted" {
				return ErrOutcomeUncertain
			}
		} else {
			if !errors.Is(readErr, os.ErrNotExist) {
				return readErr
			}
			if !first {
				return ErrOutcomeUncertain
			}
			receipt, err := actionNewOperationReceipt(d.NativeOperationID, binding, 0, time.Now().UTC())
			if err != nil {
				return err
			}
			op = actionOperation{Receipt: receipt, Kind: "run", ID: runID, CaseID: d.CaseID, CaseContextDigest: d.CaseContextDigest, Native: &nativeActionAuthority{Fence: fence, Phase: "accepted"}}
			if err = s.checkNativeDispatchCase(pin); err != nil {
				return err
			}
			if err = s.checkNativeAction(ctx, d.NativeOperationID, &op); err != nil {
				return err
			}
			if err = writeDurableItem(s.privateItemDir("action-operations"), d.NativeOperationID, op); err != nil {
				return err
			}
		}
		if err := s.checkNativeAction(ctx, d.NativeOperationID, &op); err != nil {
			return err
		}
		if err := s.checkNativeDispatchCase(pin); err != nil {
			return err
		}
		op.Native.Phase = "querying"
		if err := writeDurableItem(s.privateItemDir("action-operations"), d.NativeOperationID, op); err != nil {
			return err
		}
		dispatch = true
		return nil
	})
	if err != nil {
		return empty, err
	}
	if !dispatch {
		return op.Response, nil
	}
	queries := make(map[string]sessioncontext.DetectiveAcceptedQueryPin, len(definition.Queries))
	for _, query := range definition.Queries {
		queries[query.QueryID] = query
	}
	queryCtx := context.WithValue(ctx, nativeQueriesKey{}, queries)
	// Admit every dependency query separately. A durable Stop between steps
	// prevents the next source call; the query itself still runs outside admission.
	guardedRunner := func(queryCtx context.Context, queryID, environment string, parameters map[string]apicontract.TypedValueOrSet) (apicontract.Result, error) {
		err := s.withLock(queryCtx, func() error {
			if err := s.checkNativeAction(queryCtx, d.NativeOperationID, &op); err != nil {
				return err
			}
			return s.checkNativeDispatchCase(pin)
		})
		if err != nil {
			return apicontract.Result{}, err
		}
		return runner(queryCtx, queryID, environment, parameters)
	}
	run, err := nativeEvaluateCheck(queryCtx, core.RunRequest{ID: runID, CheckID: d.CheckID, CaseID: d.CaseID, Environment: pin.Reference.EnvironmentID, Actor: fence.Identity.Actor, Inputs: pin.EffectiveInputs, InputReference: &pin.Reference, ExpectedDefinitionDigest: d.DefinitionDigest, ExpectedCaseContextDigest: d.CaseContextDigest}, catalog, guardedRunner, metadata)
	if err != nil {
		return empty, err
	}
	err = s.withLock(ctx, func() error {
		data, err := safeReadFile(s.privateItemDir("action-operations"), d.NativeOperationID)
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &op) != nil || op.Native == nil || op.Native.Fence != fence || op.Produced || core.CheckOperationReplay(op.Receipt, binding) != nil {
			return ErrConflict
		}
		op.Response.Run = &run
		audit := core.AuditForRun(run)
		audit.Project = s.projectID
		op.RunAudit = &audit
		op.Produced = true
		op.Native.Phase = "produced"
		// Preserve the known result durably even when Stop has fenced Case writes.
		if err = writeDurableItem(s.privateItemDir("action-operations"), d.NativeOperationID, op); err != nil {
			return err
		}
		return s.finishAction(ctx, d.NativeOperationID, &op)
	})
	if err != nil {
		return empty, err
	}
	return op.Response, nil
}

// finishNativeActionFile adds directory durability to the existing idempotent
// native materializer; legacy unrelated writes keep their original behavior.
func (s *LocalStore) finishNativeActionFile(op *actionOperation, dir, id string, before []byte, after any) error {
	if err := s.finishActionFile(dir, id, before, after); err != nil {
		return err
	}
	if op.Native == nil {
		return nil
	}
	if err := writeItemSyncDir(dir); err != nil {
		return err
	}
	return writeItemSyncDir(filepath.Dir(dir))
}

func (s *LocalStore) finishNativeEvidence(ctx context.Context, id string, op *actionOperation) error {
	if err := s.checkNativeAction(ctx, id, op); err != nil {
		return err
	}
	// The audit file is fsynced by its writer; sync its containing directory
	// before acknowledging completion, including first creation after a crash.
	if err := writeItemSyncDir(s.privateDir); err != nil {
		return err
	}
	if op.CaseID == "" || op.CaseLinkStatus != "linked" {
		return nil
	}
	proofDir := s.privateItemDir("run-cohorts")
	if err := writeItemSyncDir(proofDir); err != nil {
		return err
	}
	link := core.CaseRunLink{CaseID: op.CaseID, RunID: op.ID, Actor: op.Receipt.Actor, At: op.Receipt.At}
	dir := s.privateItemDir("case-run-links")
	key := nativeCaseRunLinkID(op.CaseID, op.ID)
	data, err := safeReadFile(dir, key)
	if err == nil {
		var prior core.CaseRunLink
		if json.Unmarshal(data, &prior) != nil || prior != link {
			return ErrConflict
		}
		if err = writeItemSyncDir(dir); err != nil {
			return err
		}
		return writeItemSyncDir(filepath.Dir(dir))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeDurableItem(dir, key, link)
}
func nativeCaseRunLinkID(caseID, runID string) string {
	hash := sha256.Sum256([]byte(caseID + "\x00" + runID))
	return "link-" + hex.EncodeToString(hash[:])
}

// DeliverNativeHandoff records a metadata reference to separately owned Case
// evidence. It never reads source rows or mutates CaseRunLink. The host must
// reauthorize both resources; session references resolve unavailable after scrub.
func (s *LocalStore) DeliverNativeHandoff(ctx context.Context, id sessioncontext.DetectiveIdentity, handoff sessioncontext.EvidenceHandoff, reference apicontract.SessionInputReference, authorize func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error) (string, error) {
	receiptID := "handoff-" + nativeCaseRunLinkID(handoff.CaseID, handoff.DetectiveRunID)
	if authorize == nil {
		return "", sessioncontext.ErrUnavailable
	}
	err := s.withLock(ctx, func() error {
		if handoff.SessionID != id.SessionID || handoff.RunID != id.RunID || reference.SessionID != id.SessionID || reference.InputSnapshotID != handoff.InputSnapshotID {
			return ErrConflict
		}
		data, err := safeReadFile(s.privateItemDir("action-operations"), id.NativeOperationID)
		if err != nil {
			return err
		}
		var op actionOperation
		if json.Unmarshal(data, &op) != nil || op.Native == nil || op.Native.Fence.Identity != id || !op.Done || op.CaseLinkStatus != "linked" || op.CaseID != handoff.CaseID || op.ID != handoff.DetectiveRunID || op.Response.Run == nil || !reflect.DeepEqual(op.Response.Run.InputReference, &reference) {
			return ErrConflict
		}
		if err := authorize(ctx, op.Native.Fence, handoff); err != nil {
			return err
		}
		linkData, err := safeReadFile(s.privateItemDir("case-run-links"), nativeCaseRunLinkID(op.CaseID, op.ID))
		if err != nil {
			return err
		}
		var link core.CaseRunLink
		if json.Unmarshal(linkData, &link) != nil || link.CaseID != op.CaseID || link.RunID != op.ID || link.Actor != id.Actor {
			return ErrConflict
		}
		// A Case's independent lifecycle/permission is checked again at delivery.
		currentCase, err := s.getCase(op.CaseID)
		if err != nil {
			return err
		}
		if currentCase.Status != "active" {
			return sessioncontext.ErrReadOnly
		}
		record := nativeSessionEvidenceReceipt{Identity: id, CaseID: op.CaseID, RunID: op.ID, InputReference: reference, Retention: "session-owned-reference", Availability: "available"}
		return s.finishNativeActionFile(&op, s.privateItemDir("session-evidence-handoffs"), receiptID, nil, record)
	})
	if err != nil {
		return "", err
	}
	return receiptID, nil
}

// FinalizeNativeProduced is reconciliation only: it can finish a durable known
// result under the existing fence, but never claims work or invokes an evaluator.
func (s *LocalStore) FinalizeNativeProduced(ctx context.Context, id sessioncontext.DetectiveIdentity) (ActionResponse, error) {
	var op actionOperation
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.privateItemDir("action-operations"), id.NativeOperationID)
		if errors.Is(err, os.ErrNotExist) {
			return sessioncontext.ErrNotFound
		}
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &op) != nil || op.Native == nil || op.Native.Fence.Identity != id || op.Receipt.ID != id.NativeOperationID || op.Receipt.PayloadDigest != id.Digest || op.Receipt.Actor != id.Actor || op.Receipt.Project != s.projectID || op.Receipt.Validate() != nil {
			return ErrConflict
		}
		if !op.Produced {
			return ErrOutcomeUncertain
		}
		if op.Done {
			return nil
		}
		return s.finishAction(ctx, id.NativeOperationID, &op)
	})
	if err != nil {
		return ActionResponse{}, err
	}
	return op.Response, nil
}

// MarkSessionReferenceUnavailable is a trusted redaction continuation. The
// caller must prove the permanent Core session marker before invoking it. It
// alters only a session-owned metadata reference, preserving Case/run evidence.
func (s *LocalStore) MarkSessionReferenceUnavailable(ctx context.Context, id sessioncontext.DetectiveIdentity) (bool, error) {
	changed := false
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.privateItemDir("action-operations"), id.NativeOperationID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var op actionOperation
		if json.Unmarshal(data, &op) != nil || op.Native == nil || op.Native.Fence.Identity != id || op.Receipt.Actor != id.Actor || op.Receipt.Project != s.projectID || op.Receipt.PayloadDigest != id.Digest {
			return ErrConflict
		}
		receiptID := "handoff-" + nativeCaseRunLinkID(op.CaseID, op.ID)
		dir := s.privateItemDir("session-evidence-handoffs")
		data, err = safeReadFile(dir, receiptID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var receipt nativeSessionEvidenceReceipt
		if json.Unmarshal(data, &receipt) != nil || receipt.Identity != id || receipt.InputReference.SessionID != id.SessionID || receipt.CaseID != op.CaseID || receipt.RunID != op.ID {
			return ErrConflict
		}
		if receipt.Availability == "unavailable" {
			return nil
		}
		if receipt.Availability != "available" {
			return ErrConflict
		}
		receipt.Availability = "unavailable"
		if err = writeDurableItem(dir, receiptID, receipt); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
