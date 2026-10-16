// Package sessionnative provides trusted local native/session worker ports.
// It defines no transport, background scheduler, evaluator or capability grant.
package sessionnative

import (
	"context"
	"encoding/json"

	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

type PinRedactionContinuation struct {
	Phase      string `json:"phase"` // pins | references | done
	Cursor     string `json:"cursor,omitempty"`
	Generation int64  `json:"generation"`
}

var newPinService = sessioncontext.NewNativePinService
var newNativeOwner = sessioncontext.NewDetectiveOwner

type PinRedactionWorker struct {
	repo    sessioncontext.Repository
	native  *detective.LocalStore
	service *sessioncontext.Service
	owner   *sessioncontext.DetectiveOwner
	active  func(context.Context, sessioncontext.Principal) error
}

// NewPinRedactionWorker binds one actual guarded SQLite repository to its
// native owner. All gates are mandatory. Active authenticates the current
// runtime/caller before metadata lookup; accepted binds its stored full identity.
func NewPinRedactionWorker(repo *sessionstore.NativeRepository, native *detective.LocalStore, authorize func(context.Context, sessioncontext.Access) error, active func(context.Context, sessioncontext.Principal) error, accepted func(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) error) (*PinRedactionWorker, error) {
	if repo == nil || repo.DALRepository == nil || native == nil || authorize == nil || active == nil || accepted == nil {
		return nil, sessioncontext.ErrUnavailable
	}
	service, err := newPinService(repo, authorize)
	if err != nil {
		return nil, err
	}
	owner, err := newNativeOwner(service, accepted, native.NativeObservation)
	if err != nil {
		return nil, err
	}
	return &PinRedactionWorker{repo: repo, native: native, service: service, owner: owner, active: active}, nil
}

// RedactPage installs/reuses a permanent Core marker, scrubs at most one bounded
// input page, then marks at most one bounded accepted-work page's native session
// references unavailable. Replayed pages are safe; no Case evidence is erased.
// The trusted job owner durably retains the returned continuation before retry.
func (w *PinRedactionWorker) RedactPage(ctx context.Context, p sessioncontext.Principal, clientOpID, deleteOpID string, page PinRedactionContinuation, limit int) (PinRedactionContinuation, error) {
	var empty PinRedactionContinuation
	if err := w.active(ctx, p); err != nil {
		return empty, err
	}
	if limit < 1 || limit > 100 || page.Generation < 0 {
		return empty, sessioncontext.ErrInvalid
	}
	if page.Phase == "" {
		if page.Cursor != "" || page.Generation != 0 {
			return empty, sessioncontext.ErrInvalid
		}
		page.Phase = "pins"
	}
	switch page.Phase {
	case "pins", "references", "done":
	default:
		return empty, sessioncontext.ErrInvalid
	}
	accepted, err := w.owner.ResolveOperation(ctx, p, clientOpID)
	if err != nil {
		return empty, err
	}
	id := accepted.Identity
	p.Executor = true
	marker := sessioncontext.SessionRedaction{}
	loadMarker := func() error {
		return w.repo.View(ctx, func(reader sessioncontext.Reader) error {
			if _, err := reader.Load(ctx, sessioncontext.Redactions, id.SessionID, &marker); err != nil {
				return err
			}
			if marker.Owner != p.Actor || marker.Namespace != id.Namespace || marker.DeleteOpID != deleteOpID || marker.Version < 1 {
				return sessioncontext.ErrConflict
			}
			if page.Generation != 0 && marker.Version != page.Generation {
				return sessioncontext.ErrConflict
			}
			return nil
		})
	}
	if page.Generation != 0 {
		if err = loadMarker(); err != nil {
			return empty, err
		}
	}
	if page.Phase == "pins" {
		next, err := w.owner.RedactPins(ctx, p, id, deleteOpID, page.Cursor, limit)
		if err != nil {
			return empty, err
		}
		if err = loadMarker(); err != nil {
			return empty, err
		}
		if next != "" {
			return PinRedactionContinuation{Phase: "pins", Cursor: next, Generation: marker.Version}, nil
		}
		return PinRedactionContinuation{Phase: "references", Generation: marker.Version}, nil
	}
	if page.Generation == 0 {
		return empty, sessioncontext.ErrInvalid
	}
	if page.Phase == "done" {
		return page, nil
	}
	var rows []json.RawMessage
	var next string
	if err = w.repo.View(ctx, func(reader sessioncontext.Reader) error {
		var err error
		rows, next, err = reader.List(ctx, sessioncontext.DetectiveWork, p.Actor, page.Cursor, limit)
		return err
	}); err != nil {
		return empty, err
	}
	for _, raw := range rows {
		var work sessioncontext.AcceptedDetectiveWork
		if json.Unmarshal(raw, &work) != nil {
			return empty, sessioncontext.ErrUnavailable
		}
		if work.SessionID != id.SessionID {
			continue
		}
		if work.Owner != p.Actor || work.Namespace != id.Namespace {
			return empty, sessioncontext.ErrConflict
		}
		bound, err := w.owner.ResolveOperation(ctx, p, work.ClientOperationID)
		if err != nil {
			return empty, err
		}
		if bound.Identity.RunID != work.RunID || bound.Identity.SessionID != id.SessionID {
			return empty, sessioncontext.ErrConflict
		}
		if _, err = w.native.MarkSessionReferenceUnavailable(ctx, bound.Identity); err != nil {
			return empty, err
		}
	}
	phase := "references"
	if next == "" {
		phase = "done"
	}
	return PinRedactionContinuation{Phase: phase, Cursor: next, Generation: marker.Version}, nil
}
