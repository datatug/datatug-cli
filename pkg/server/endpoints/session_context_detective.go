package endpoints

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/julienschmidt/httprouter"
)

func sessionDetectiveScope(q url.Values, p sessioncontext.Principal, n sessioncontext.NativeNamespace) (api.SessionDetectiveOperationScope, error) {
	for key, values := range q {
		switch key {
		case "storage", "project", "spaceID", "databaseId", "projectId", "namespaceId":
		default:
			return api.SessionDetectiveOperationScope{}, sessioncontext.ErrInvalid
		}
		if len(values) != 1 {
			return api.SessionDetectiveOperationScope{}, sessioncontext.ErrInvalid
		}
	}
	if q.Get("spaceID") != "" || q.Get("databaseId") != p.DatabaseID || q.Get("projectId") != p.ProjectID || q.Get("namespaceId") != n.NamespaceID {
		return api.SessionDetectiveOperationScope{}, sessioncontext.ErrForbidden
	}
	storage := q.Get("storage")
	if storage == "" {
		storage = "local"
	}
	return api.SessionDetectiveOperationScope{Storage: storage, Project: q.Get("project"), DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, NamespaceID: n.NamespaceID}, nil
}

var admitSessionDetectiveContent = sessionDetectiveContentAdmission
var sessionDetectiveCurrentOwner = func(service *sessioncontext.Service, lookup func(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error)) (sessionDetectiveOperationOwner, error) {
	return sessionDetectiveNewOwner(service, api.AcceptedSessionActorGate, lookup)
}

var sessionDetectiveContentContext = api.ResolveSessionContext
var sessionDetectiveActorGate = api.AcceptedSessionActorGate
var sessionDetectiveReadRun = (*sessioncontext.Service).ReadRun
var sessionDetectivePinInput = (*sessioncontext.Service).PinDetectiveInput
var sessionDetectivePrepareInput = prepareSessionDetectivePin

var sessionDetectiveEncode = json.Marshal
var sessionDetectiveProjectStore = api.ProjectStoreFor

var sessionDetectiveAcceptedContext = api.ResolveAcceptedSessionContext
var sessionDetectiveProjectDirectory = api.ProjectDir
var sessionDetectivePrivateDirectory = executionstore.ResolvePrivateDir
var sessionDetectiveLocalStore = detective.NewLocalStore
var sessionDetectiveNewOwner = sessioncontext.NewDetectiveOwner
var sessionDetectiveConfigureFence = func(store *detective.LocalStore, check func(context.Context, sessioncontext.DetectiveFence) error) error {
	return store.ConfigureNativeFence(check)
}

func sessionDetectiveNativeStore(ctx context.Context, project, storage string) (*detective.LocalStore, *sessioncontext.Service, sessioncontext.Principal, *sessioncontext.DetectiveOwner, sessioncontext.NativeNamespace, error) {
	var namespace sessioncontext.NativeNamespace
	service, p, repo, err := sessionDetectiveAcceptedContext(ctx, storage, project)
	if err != nil {
		return nil, nil, p, nil, namespace, err
	}
	namespace = repo.NativeNamespace()
	p.Executor = true
	dir, ok := sessionDetectiveProjectDirectory(project)
	if !ok {
		return nil, nil, p, nil, namespace, sessioncontext.ErrForbidden
	}
	base, err := sessionDetectivePrivateDirectory(api.GetCapabilities().EvidencePrivateDir)
	if err != nil {
		return nil, nil, p, nil, namespace, err
	}
	store, err := sessionDetectiveLocalStore(project, dir, base)
	if err != nil {
		return nil, nil, p, nil, namespace, err
	}
	owner, err := sessionDetectiveNewOwner(service, api.AcceptedSessionActorGate, store.NativeObservation)
	if err != nil {
		return nil, nil, p, nil, namespace, err
	}
	if err = sessionDetectiveConfigureFence(store, func(ctx context.Context, fence sessioncontext.DetectiveFence) error {
		currentService, currentPrincipal, err := admitSessionDetectiveContent(ctx, storage, project, p, fence.Identity)
		if err != nil {
			return err
		}
		currentOwner, err := sessionDetectiveCurrentOwner(currentService, store.NativeObservation)
		if err != nil {
			return err
		}
		return currentOwner.CheckFence(ctx, currentPrincipal, fence)
	}); err != nil {
		return nil, nil, p, nil, namespace, err
	}
	return store, service, p, owner, namespace, nil
}

func sessionContextDetectiveHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Existing router origin/client/session-token admission wraps this handler.
	// Require active local owner/write admission even for missing status IDs.
	if !api.GetCapabilities().NativeSessionDetective || !api.GetCapabilities().AllowWrites || !api.SecureSessionUnrestricted() || api.SecurePrincipalID() == "" {
		writeSessionDetectiveError(w, r, sessioncontext.ErrForbidden)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeSessionDetectiveError(w, r, sessioncontext.ErrInvalid)
		return
	}
	store, acceptedService, p, owner, n, err := resolveSessionDetectiveRuntime(r.Context(), q.Get("project"), q.Get("storage"))
	if err != nil {
		writeSessionDetectiveError(w, r, err)
		return
	}
	scope, err := sessionDetectiveScope(q, p, n)
	if err != nil {
		writeSessionDetectiveError(w, r, err)
		return
	}
	opID := ""
	var request api.SessionDetectiveRequest
	reconcile := strings.HasSuffix(r.URL.Path, "/reconcile")
	if r.Method == http.MethodGet {
		opID = httprouter.ParamsFromContext(r.Context()).ByName("clientOpId")
	} else {
		bodyLimit := int64(128 << 10)
		if reconcile {
			bodyLimit = 4 << 10
		}
		body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
		if readErr != nil {
			writeSessionDetectiveError(w, r, readErr)
			return
		}
		if reconcile {
			var command struct {
				OperationID string `json:"operationId"`
			}
			if apicontract.DecodeStrict(body, &command) != nil {
				writeSessionDetectiveError(w, r, sessioncontext.ErrInvalid)
				return
			}
			opID = command.OperationID
		} else {
			if apicontract.DecodeStrict(body, &request) != nil {
				writeSessionDetectiveError(w, r, sessioncontext.ErrInvalid)
				return
			}
			opID = request.OperationID
		}
	}
	accepted, lookupErr := owner.ResolveOperation(r.Context(), p, opID)
	var digest string
	if r.Method == http.MethodPost && !reconcile {
		encoded, encodeErr := sessionDetectiveEncode(struct {
			Scope   api.SessionDetectiveOperationScope
			Actor   string
			Request api.SessionDetectiveRequest
		}{scope, p.Actor, request})
		if encodeErr != nil {
			writeSessionDetectiveError(w, r, sessioncontext.ErrInvalid)
			return
		}
		hash := sha256.Sum256(encoded)
		digest = hex.EncodeToString(hash[:])
		if lookupErr == nil && accepted.ClientRequestDigest != digest {
			writeSessionDetectiveError(w, r, sessioncontext.ErrConflict)
			return
		}
	}
	if lookupErr != nil && (!errors.Is(lookupErr, sessioncontext.ErrNotFound) || r.Method != http.MethodPost || reconcile) {
		writeSessionDetectiveError(w, r, lookupErr)
		return
	}
	if lookupErr != nil {
		// Only a new original dispatch may consult mutable definitions. Exact retry
		// above resolves its old pin without reading current Check/Case/query content.
		service, normalP, _, resolveErr := sessionDetectiveContentContext(r.Context(), q.Get("storage"), q.Get("project"))
		if resolveErr != nil {
			writeSessionDetectiveError(w, r, resolveErr)
			return
		}
		normalP.Executor = true
		command, prepareErr := sessionDetectivePrepareInput(r.Context(), service, normalP, store, n, request, digest)
		if prepareErr != nil {
			writeSessionDetectiveError(w, r, prepareErr)
			return
		}
		if _, err = sessionDetectivePinInput(service, r.Context(), normalP, command); err != nil {
			writeSessionDetectiveError(w, r, err)
			return
		}
		accepted, err = owner.ResolveOperation(r.Context(), p, opID)
		if err != nil {
			writeSessionDetectiveError(w, r, err)
			return
		}
	}
	if r.Method == http.MethodPost && !reconcile {
		token, pin, first, claimErr := owner.ClaimDispatchOnce(r.Context(), p, accepted.Identity)
		if claimErr == nil {
			// Native journal itself checks exact identity, durable phase and current
			// Case/fence under shared admission; false never creates an acceptance.
			_, executeErr := store.ExecuteNativePin(r.Context(), token, pin, first, detectiveQueryRunner(scope.Project, api.SecurityContextID()), nil)
			if executeErr != nil && !errors.Is(executeErr, detective.ErrOutcomeUncertain) && !errors.Is(executeErr, sessioncontext.ErrReadOnly) && !errors.Is(executeErr, sessioncontext.ErrForbidden) {
				writeSessionDetectiveError(w, r, executeErr)
				return
			}
		} else if !errors.Is(claimErr, sessioncontext.ErrReadOnly) && !errors.Is(claimErr, sessioncontext.ErrForbidden) {
			writeSessionDetectiveError(w, r, claimErr)
			return
		}
	}
	if reconcile {
		_, finishErr := store.FinalizeNativeProduced(r.Context(), accepted.Identity)
		if finishErr != nil && !errors.Is(finishErr, sessioncontext.ErrNotFound) && !errors.Is(finishErr, detective.ErrOutcomeUncertain) && !errors.Is(finishErr, sessioncontext.ErrReadOnly) && !errors.Is(finishErr, sessioncontext.ErrForbidden) {
			writeSessionDetectiveError(w, r, finishErr)
			return
		}
	}
	if r.Method == http.MethodPost {
		if _, err = owner.Reconcile(r.Context(), p, accepted.Identity); err != nil && !errors.Is(err, sessioncontext.ErrNotFound) {
			writeSessionDetectiveError(w, r, err)
			return
		}
	}

	beforeHandoff, err := owner.ReadStatus(r.Context(), p, accepted.Identity)
	if err != nil {
		writeSessionDetectiveError(w, r, err)
		return
	}
	if r.Method == http.MethodPost && beforeHandoff.Done && beforeHandoff.FenceReason == "" {
		handoffErr := func() error {
			handoff, version, handoffErr := owner.ReadHandoff(r.Context(), p, accepted.Identity)
			if handoffErr != nil && !errors.Is(handoffErr, sessioncontext.ErrNotFound) {
				return handoffErr
			}
			if handoffErr == nil && handoff.State == "pending" {
				pin, pinErr := acceptedService.ReadDetectiveDispatch(r.Context(), p, accepted.Identity.SessionID, accepted.Identity.RunID)
				if pinErr != nil {
					return pinErr
				}
				if pinErr == nil {
					receiptID, deliverErr := store.DeliverNativeHandoff(r.Context(), accepted.Identity, handoff, pin.Reference, func(ctx context.Context, token sessioncontext.DetectiveFence, expected sessioncontext.EvidenceHandoff) error {
						return authorizeSessionDetectiveHandoff(ctx, scope, p, store, version, pin.Reference, token, expected)
					})
					if deliverErr != nil {
						return deliverErr
					}
					if deliverErr == nil {
						if settleErr := owner.SettleHandoff(r.Context(), p, accepted.Identity, version, true, receiptID); settleErr != nil {
							return settleErr
						}
					}
				}
			}
			return nil
		}()
		if handoffErr != nil && !errors.Is(handoffErr, sessioncontext.ErrReadOnly) && !errors.Is(handoffErr, sessioncontext.ErrForbidden) {
			writeSessionDetectiveError(w, r, handoffErr)
			return
		}
	}

	status, err := owner.ReadStatus(r.Context(), p, accepted.Identity)
	if err != nil {
		writeSessionDetectiveError(w, r, err)
		return
	}
	writeContractResponseStatus(w, r, http.StatusOK, api.SessionDetectiveResponse{OperationID: opID, Status: status, OperationScope: scope})
}

type sessionDetectivePinReader interface {
	GetSnapshot(context.Context, sessioncontext.Principal, string) (sessioncontext.Snapshot, error)
	ReadEntryRevision(context.Context, sessioncontext.Principal, string, string, string) (sessioncontext.EntryRevision, error)
}
type sessionDetectiveCaptureOwner interface {
	CaptureNativeInput(context.Context, detective.NativeCaptureRequest, detective.NativeQuerySnapshot) (detective.NativeCapture, error)
}

func prepareSessionDetectivePin(ctx context.Context, service sessionDetectivePinReader, p sessioncontext.Principal, store sessionDetectiveCaptureOwner, n sessioncontext.NativeNamespace, req api.SessionDetectiveRequest, digest string) (sessioncontext.PinDetectiveInputCommand, error) {
	var empty sessioncontext.PinDetectiveInputCommand
	if len(req.Bindings) > apicontract.MaxSessionInputBindings || len(req.Inputs) > apicontract.MaxSessionInputBindings || len(req.Attachments) > apicontract.MaxSessionInputEntryRefs {
		return empty, sessioncontext.ErrInvalid
	}
	snapshot, err := service.GetSnapshot(ctx, p, req.SessionID)
	if err != nil {
		return empty, err
	}
	if snapshot.Session.Version != req.ExpectedVersion || snapshot.ContextSet.ID != req.ExpectedContextSetID {
		return empty, sessioncontext.ErrConflict
	}
	inputs := make(map[string]core.InputValue, len(req.Inputs)+len(req.Bindings))
	for name, value := range req.Inputs {
		inputs[name] = value
	}
	bindings := make(map[string]apicontract.SessionInputBindingRef)
	for _, selection := range req.Bindings {
		if _, exists := bindings[selection.InputName]; exists {
			return empty, sessioncontext.ErrInvalid
		}
		if _, exists := inputs[selection.InputName]; exists {
			return empty, sessioncontext.ErrInvalid
		}
		revision, err := service.ReadEntryRevision(ctx, p, req.SessionID, selection.EntryID, selection.RevisionID)
		if err != nil {
			return empty, err
		}
		var bag *sessioncontext.Bag
		for i := range snapshot.Bags {
			if snapshot.Bags[i].ID == revision.BagID {
				bag = &snapshot.Bags[i]
				break
			}
		}
		if bag == nil || bag.Scope != req.Scope {
			return empty, sessioncontext.ErrForbidden
		}
		value := core.InputValue{}
		switch revision.Value.Kind {
		case "scalar":
			value.Scalar = revision.Value.Scalar
		case "set":
			if len(revision.Value.Keys) != 0 || len(revision.Value.Values) == 0 {
				return empty, sessioncontext.ErrEmptyCheckSet
			}
			set, setErr := apicontract.NewTypedValueSet(revision.Value.Values...)
			if setErr != nil {
				return empty, setErr
			}
			value.Set = &set
		default:
			return empty, sessioncontext.ErrInvalid
		}
		if value.Validate() != nil {
			return empty, sessioncontext.ErrInvalid
		}
		bagVersion := bag.Version
		for _, slot := range snapshot.ContextSet.Slots {
			if slot.BagID == bag.ID && slot.Scope == req.Scope {
				for _, entry := range slot.Entries {
					if entry.EntryID == selection.EntryID && entry.RevisionID == selection.RevisionID {
						bagVersion = slot.BagVersion
					}
				}
			}
		}
		inputs[selection.InputName] = value
		bindings[selection.InputName] = apicontract.SessionInputBindingRef{InputName: selection.InputName, Origin: "bag", Entries: []apicontract.SessionInputEntryRef{{BagID: bag.ID, BagVersion: bagVersion, EntryID: selection.EntryID, RevisionID: selection.RevisionID}}}
	}
	ids, err := sessionDetectiveEncode(struct {
		Actor       string
		Namespace   sessioncontext.NativeNamespace
		OperationID string
	}{p.Actor, n, req.OperationID})
	if err != nil {
		return empty, err
	}
	hash := sha256.Sum256(ids)
	suffix := hex.EncodeToString(hash[:])
	runID, inputID, nativeID := "run-"+suffix, "input-"+suffix, "native-"+suffix
	reference := apicontract.SessionInputReference{SchemaVersion: 1, StorageID: n.StorageID, SpaceID: n.SpaceID, DatabaseID: n.DatabaseID, ProjectID: n.ProjectID, NamespaceID: n.NamespaceID, SessionID: req.SessionID, InputSnapshotID: inputID, ContextSetID: req.ExpectedContextSetID, EnvironmentID: req.Scope.EnvironmentID, IdentityDomain: req.Scope.IdentityDomain}
	// Capture derives Case defaults and the exact transitive executable graph.
	for _, binding := range bindings {
		reference.Bindings = append(reference.Bindings, binding)
	}
	capture, err := store.CaptureNativeInput(ctx, detective.NativeCaptureRequest{CheckID: req.CheckID, CaseID: req.CaseID, NativeOperationID: nativeID, Actor: p.Actor, Inputs: inputs, Reference: reference}, sessionDetectiveQuerySnapshot(p.ProjectID))
	if err != nil {
		return empty, err
	}
	return sessioncontext.PinDetectiveInputCommand{ClientRequestDigest: digest, OpID: req.OperationID, SessionID: req.SessionID, RunID: runID, InputSnapshotID: inputID, ExpectedVersion: req.ExpectedVersion, ExpectedContextSetID: req.ExpectedContextSetID, Scope: req.Scope, Attachments: req.Attachments, Pin: capture.Pin, Case: capture.Case, NativeBinding: capture.Binding}, nil
}

func sessionDetectiveQuerySnapshot(project string) detective.NativeQuerySnapshot {
	return func(ctx context.Context, id string) (sessioncontext.DetectiveAcceptedQueryPin, error) {
		store, err := sessionDetectiveProjectStore(project)
		if err != nil {
			return sessioncontext.DetectiveAcceptedQueryPin{}, err
		}
		revisioned, ok := store.(datatug.RevisionedQueriesStore)
		if !ok {
			return sessioncontext.DetectiveAcceptedQueryPin{}, sessioncontext.ErrUnavailable
		}
		saved, err := revisioned.LoadQueryRevision(ctx, id)
		if err != nil {
			return sessioncontext.DetectiveAcceptedQueryPin{}, err
		}
		encoded, err := sessionDetectiveEncode(saved.Query)
		if err != nil {
			return sessioncontext.DetectiveAcceptedQueryPin{}, err
		}
		return sessioncontext.DetectiveAcceptedQueryPin{QueryID: id, Revision: string(saved.Revision), DefinitionJSON: encoded}, nil
	}
}

func sessionDetectiveContentAdmission(ctx context.Context, storage, project string, p sessioncontext.Principal, id sessioncontext.DetectiveIdentity) (*sessioncontext.Service, sessioncontext.Principal, error) {
	service, current, caps, err := sessionDetectiveContentContext(ctx, storage, project)
	if err != nil {
		return nil, current, err
	}
	current.Executor = true
	if current.Actor != p.Actor || current.DatabaseID != p.DatabaseID || current.ProjectID != p.ProjectID || !api.GetCapabilities().AllowWrites || !api.SecureSessionUnrestricted() {
		return nil, current, sessioncontext.ErrForbidden
	}
	if err = sessionDetectiveActorGate(ctx, current, id); err != nil {
		return nil, current, err
	}
	run, err := sessionDetectiveReadRun(service, ctx, current, id.RunID)
	if err != nil {
		return nil, current, err
	}
	if run.InputReference == nil {
		return nil, current, sessioncontext.ErrConflict
	}
	for _, environment := range caps.Environments {
		if environment.EnvironmentID == run.InputReference.EnvironmentID && environment.IdentityDomain == run.InputReference.IdentityDomain {
			return service, current, nil
		}
	}
	return nil, current, sessioncontext.ErrForbidden
}

func validateSessionDetectiveHandoffMetadata(ctx context.Context, reader sessioncontext.Reader, currentPrincipal sessioncontext.Principal, token sessioncontext.DetectiveFence, reference apicontract.SessionInputReference) error {
	var session sessioncontext.Session
	if _, err := reader.Load(ctx, sessioncontext.Sessions, token.Identity.SessionID, &session); err != nil {
		return err
	}
	var work sessioncontext.AcceptedDetectiveWork
	if _, err := reader.Load(ctx, sessioncontext.DetectiveWork, token.Identity.RunID, &work); err != nil {
		return err
	}
	if session.State != "active" || session.NativeFenceGeneration != token.SessionGeneration || work.SessionGeneration != token.SessionGeneration {
		return sessioncontext.ErrReadOnly
	}
	if work.Owner != currentPrincipal.Actor || work.Namespace != token.Identity.Namespace || !reflect.DeepEqual(work.Reference, reference) {
		return sessioncontext.ErrConflict
	}
	return nil
}

func authorizeSessionDetectiveHandoff(ctx context.Context, scope api.SessionDetectiveOperationScope, p sessioncontext.Principal, store sessionDetectiveNativeOwner, version int64, reference apicontract.SessionInputReference, token sessioncontext.DetectiveFence, expected sessioncontext.EvidenceHandoff) error {
	currentService, currentPrincipal, err := admitSessionDetectiveContent(ctx, scope.Storage, scope.Project, p, token.Identity)
	if err != nil {
		return err
	}
	_, _, currentRepo, err := sessionDetectiveAcceptedContext(ctx, scope.Storage, scope.Project)
	if err != nil {
		return err
	}
	currentOwner, err := sessionDetectiveCurrentOwner(currentService, store.NativeObservation)
	if err != nil {
		return err
	}
	actual, actualVersion, err := currentOwner.ReadHandoff(ctx, currentPrincipal, token.Identity)
	if err != nil {
		return err
	}
	if actualVersion != version || actual.State != "pending" || !reflect.DeepEqual(actual, expected) {
		return sessioncontext.ErrConflict
	}
	return currentRepo.View(ctx, func(reader sessioncontext.Reader) error {
		return validateSessionDetectiveHandoffMetadata(ctx, reader, currentPrincipal, token, reference)
	})
}
