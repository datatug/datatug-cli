package endpoints

import (
	"context"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

// Internal ports make owner/storage failure boundaries testable. They do not
// expose trusted owner methods through the generic command transport.
type sessionDetectiveAcceptedReader interface {
	ReadDetectiveDispatch(context.Context, sessioncontext.Principal, string, string) (sessioncontext.DetectiveInputPin, error)
}
type sessionDetectiveOperationOwner interface {
	CheckFence(context.Context, sessioncontext.Principal, sessioncontext.DetectiveFence) error
	ResolveOperation(context.Context, sessioncontext.Principal, string) (sessioncontext.AcceptedDetectiveOperation, error)
	ClaimDispatchOnce(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveFence, sessioncontext.DetectiveInputPin, bool, error)
	Reconcile(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.AcceptedDetectiveStatus, error)
	ReadStatus(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.AcceptedDetectiveStatus, error)
	ReadHandoff(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.EvidenceHandoff, int64, error)
	SettleHandoff(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity, int64, bool, string) error
}
type sessionDetectiveNativeOwner interface {
	sessionDetectiveCaptureOwner
	NativeObservation(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error)
	ExecuteNativePin(context.Context, sessioncontext.DetectiveFence, sessioncontext.DetectiveInputPin, bool, detective.QueryRunner, detective.ExecutionMetadata) (detective.ActionResponse, error)
	FinalizeNativeProduced(context.Context, sessioncontext.DetectiveIdentity) (detective.ActionResponse, error)
	DeliverNativeHandoff(context.Context, sessioncontext.DetectiveIdentity, sessioncontext.EvidenceHandoff, apicontract.SessionInputReference, func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error) (string, error)
}

var resolveSessionDetectiveRuntime = func(ctx context.Context, project, storage string) (sessionDetectiveNativeOwner, sessionDetectiveAcceptedReader, sessioncontext.Principal, sessionDetectiveOperationOwner, sessioncontext.NativeNamespace, error) {
	store, service, p, owner, n, err := sessionDetectiveNativeStore(ctx, project, storage)
	return store, service, p, owner, n, err
}
