package endpoints

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"net/http"
	"net/http/httptest"
	"testing"
)

type nativeTransportOwner struct {
	sessionDetectiveOperationOwner
	kind     string
	failure  error
	accepted sessioncontext.AcceptedDetectiveOperation
	statuses int
	lookups  int
}

func (o *nativeTransportOwner) ResolveOperation(context.Context, sessioncontext.Principal, string) (sessioncontext.AcceptedDetectiveOperation, error) {
	o.lookups++
	if o.kind == "new-context" || o.kind == "new-pin" || o.kind == "after-pin" {
		if o.lookups == 1 {
			return sessioncontext.AcceptedDetectiveOperation{}, sessioncontext.ErrNotFound
		}
		if o.kind == "after-pin" {
			return sessioncontext.AcceptedDetectiveOperation{}, o.failure
		}
	}
	return o.accepted, nil
}
func (o *nativeTransportOwner) ClaimDispatchOnce(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveFence, sessioncontext.DetectiveInputPin, bool, error) {
	if o.kind == "claim" {
		return sessioncontext.DetectiveFence{}, sessioncontext.DetectiveInputPin{}, false, o.failure
	}
	return sessioncontext.DetectiveFence{}, sessioncontext.DetectiveInputPin{}, false, nil
}
func (o *nativeTransportOwner) Reconcile(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.AcceptedDetectiveStatus, error) {
	if o.kind == "reconcile" {
		return sessioncontext.AcceptedDetectiveStatus{}, o.failure
	}
	return sessioncontext.AcceptedDetectiveStatus{}, nil
}
func (o *nativeTransportOwner) ReadStatus(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.AcceptedDetectiveStatus, error) {
	o.statuses++
	if o.kind == "prestatus" || o.kind == "finalstatus" && o.statuses == 2 {
		return sessioncontext.AcceptedDetectiveStatus{}, o.failure
	}
	return sessioncontext.AcceptedDetectiveStatus{Done: true}, nil
}
func (o *nativeTransportOwner) ReadHandoff(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (sessioncontext.EvidenceHandoff, int64, error) {
	if o.kind == "handoff" {
		return sessioncontext.EvidenceHandoff{}, 0, o.failure
	}
	return sessioncontext.EvidenceHandoff{State: "pending"}, 1, nil
}
func (o *nativeTransportOwner) SettleHandoff(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity, int64, bool, string) error {
	if o.kind == "settle" {
		return o.failure
	}
	return nil
}

type nativeTransportReader struct {
	kind    string
	failure error
}

func (r nativeTransportReader) ReadDetectiveDispatch(context.Context, sessioncontext.Principal, string, string) (sessioncontext.DetectiveInputPin, error) {
	if r.kind == "pin" {
		return sessioncontext.DetectiveInputPin{}, r.failure
	}
	return sessioncontext.DetectiveInputPin{}, nil
}

type nativeTransportStore struct {
	sessionDetectiveNativeOwner
	kind    string
	failure error
}

func (s nativeTransportStore) ExecuteNativePin(context.Context, sessioncontext.DetectiveFence, sessioncontext.DetectiveInputPin, bool, detective.QueryRunner, detective.ExecutionMetadata) (detective.ActionResponse, error) {
	if s.kind == "execute" {
		return detective.ActionResponse{}, s.failure
	}
	return detective.ActionResponse{}, nil
}
func (s nativeTransportStore) FinalizeNativeProduced(context.Context, sessioncontext.DetectiveIdentity) (detective.ActionResponse, error) {
	if s.kind == "finalize" {
		return detective.ActionResponse{}, s.failure
	}
	return detective.ActionResponse{}, nil
}
func (s nativeTransportStore) DeliverNativeHandoff(context.Context, sessioncontext.DetectiveIdentity, sessioncontext.EvidenceHandoff, apicontract.SessionInputReference, func(context.Context, sessioncontext.DetectiveFence, sessioncontext.EvidenceHandoff) error) (string, error) {
	if s.kind == "deliver" {
		return "", s.failure
	}
	return "native-receipt", nil
}
func TestNativeTransportOwnerFailuresAndOptionalContentDenials(t *testing.T) {
	old := resolveSessionDetectiveRuntime
	oldEncode := sessionDetectiveEncode
	oldContext, oldPrepare, oldPin := sessionDetectiveContentContext, sessionDetectivePrepareInput, sessionDetectivePinInput
	t.Cleanup(func() {
		resolveSessionDetectiveRuntime = old
		sessionDetectiveEncode = oldEncode
		sessionDetectiveContentContext, sessionDetectivePrepareInput, sessionDetectivePinInput = oldContext, oldPrepare, oldPin
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, nil, api.Capabilities{NativeSessionDetective: true, AllowWrites: true})
	p := sessioncontext.Principal{Actor: "alice", DatabaseID: "db", ProjectID: "project"}
	n := sessioncontext.NativeNamespace{NamespaceID: "namespace"}
	scope := api.SessionDetectiveOperationScope{Storage: "local", Project: "source", DatabaseID: "db", ProjectID: "project", NamespaceID: "namespace"}
	req := api.SessionDetectiveRequest{OperationID: "operation"}
	encoded, e := json.Marshal(struct {
		Scope   api.SessionDetectiveOperationScope
		Actor   string
		Request api.SessionDetectiveRequest
	}{scope, p.Actor, req})
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	for _, kind := range []string{"new-context", "new-pin", "after-pin", "encode", "runtime", "claim", "execute", "finalize", "reconcile", "prestatus", "finalstatus", "handoff", "pin", "deliver", "settle"} {
		for _, denied := range []bool{false, true} {
			t.Run(kind+map[bool]string{true: "-content-denied", false: "-storage-failure"}[denied], func(t *testing.T) {
				sessionDetectiveEncode = oldEncode
				failure := errors.New("private storage diagnostic")
				if denied {
					failure = sessioncontext.ErrForbidden
				}
				if kind == "encode" {
					sessionDetectiveEncode = func(any) ([]byte, error) { return nil, failure }
				}
				sessionDetectiveContentContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, api.SessionContextCapabilities, error) {
					if kind == "new-context" {
						return nil, p, api.SessionContextCapabilities{}, failure
					}
					return nil, p, api.SessionContextCapabilities{}, nil
				}
				sessionDetectivePrepareInput = func(context.Context, sessionDetectivePinReader, sessioncontext.Principal, sessionDetectiveCaptureOwner, sessioncontext.NativeNamespace, api.SessionDetectiveRequest, string) (sessioncontext.PinDetectiveInputCommand, error) {
					return sessioncontext.PinDetectiveInputCommand{}, nil
				}
				sessionDetectivePinInput = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, sessioncontext.PinDetectiveInputCommand) (sessioncontext.PinnedDetectiveInput, error) {
					if kind == "new-pin" {
						return sessioncontext.PinnedDetectiveInput{}, failure
					}
					return sessioncontext.PinnedDetectiveInput{}, nil
				}
				owner := &nativeTransportOwner{kind: kind, failure: failure, accepted: sessioncontext.AcceptedDetectiveOperation{ClientRequestDigest: digest}}
				resolveSessionDetectiveRuntime = func(context.Context, string, string) (sessionDetectiveNativeOwner, sessionDetectiveAcceptedReader, sessioncontext.Principal, sessionDetectiveOperationOwner, sessioncontext.NativeNamespace, error) {
					if kind == "runtime" {
						return nil, nil, p, nil, n, failure
					}
					return nativeTransportStore{kind: kind, failure: failure}, nativeTransportReader{kind: kind, failure: failure}, p, owner, n, nil
				}
				path := "/session-context/detective"
				body, _ := json.Marshal(req)
				if kind == "finalize" {
					path += "/reconcile"
					body = []byte(`{"operationId":"operation"}`)
				}
				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodPost, "http://localhost"+path+"?storage=local&project=source&databaseId=db&projectId=project&namespaceId=namespace", bytes.NewReader(body))
				sessionContextDetectiveHandler(w, r)
				want := 503
				if kind == "encode" {
					want = 400
				}
				if denied {
					want = 403
					if kind == "encode" {
						want = 400
					}
					switch kind {
					case "claim", "execute", "finalize", "handoff", "pin", "deliver", "settle":
						want = 200
					}
				}
				if w.Code != want {
					t.Fatal(w.Code, w.Body.String())
				}
			})
		}
	}
	malformed := httptest.NewRequest(http.MethodPost, "http://localhost/session-context/detective", nil)
	malformed.URL.RawQuery = "project=%zz"
	response := httptest.NewRecorder()
	sessionContextDetectiveHandler(response, malformed)
	if response.Code != http.StatusBadRequest {
		t.Fatal(response.Code, response.Body.String())
	}

}

func (s nativeTransportStore) NativeObservation(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error) {
	return sessioncontext.DetectiveObservation{}, sessioncontext.ErrNotFound
}
