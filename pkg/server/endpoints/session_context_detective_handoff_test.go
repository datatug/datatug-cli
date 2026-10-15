package endpoints

import (
	"context"
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"testing"
)

type nativeHandoffReader struct {
	sessioncontext.Reader
	load func(string, any) (int64, error)
}

func (r nativeHandoffReader) Load(_ context.Context, c, _ string, v any) (int64, error) {
	return r.load(c, v)
}
func TestNativeHandoffValidatesCurrentSessionAndExactAcceptedMetadata(t *testing.T) {
	for _, kind := range []string{"active", "session-read", "work-read", "stopped", "session-generation", "work-generation", "foreign-owner", "foreign-namespace", "different-reference"} {
		t.Run(kind, func(t *testing.T) {
			reference := apicontract.SessionInputReference{SessionID: "session", InputSnapshotID: "input"}
			principal := sessioncontext.Principal{Actor: "alice"}
			token := sessioncontext.DetectiveFence{SessionGeneration: 4, Identity: sessioncontext.DetectiveIdentity{Actor: "alice", SessionID: "session", RunID: "run", Namespace: sessioncontext.NativeNamespace{NamespaceID: "namespace"}}}
			session := sessioncontext.Session{State: "active", NativeFenceGeneration: 4}
			work := sessioncontext.AcceptedDetectiveWork{Owner: "alice", SessionGeneration: 4, Namespace: token.Identity.Namespace, Reference: reference}
			sentinel := errors.New("metadata storage unavailable")
			switch kind {
			case "stopped":
				session.State = "stopped"
			case "session-generation":
				session.NativeFenceGeneration++
			case "work-generation":
				work.SessionGeneration++
			case "foreign-owner":
				work.Owner = "mallory"
			case "foreign-namespace":
				work.Namespace.NamespaceID = "foreign"
			case "different-reference":
				work.Reference.InputSnapshotID = "foreign"
			}
			reader := nativeHandoffReader{load: func(c string, v any) (int64, error) {
				if c == sessioncontext.Sessions {
					if kind == "session-read" {
						return 0, sentinel
					}
					*v.(*sessioncontext.Session) = session
				} else {
					if kind == "work-read" {
						return 0, sentinel
					}
					*v.(*sessioncontext.AcceptedDetectiveWork) = work
				}
				return 1, nil
			}}
			e := validateSessionDetectiveHandoffMetadata(context.Background(), reader, principal, token, reference)
			if (kind == "active") != (e == nil) {
				t.Fatal(kind, e)
			}
		})
	}
}

func TestNativeHandoffRechecksContentOwnerAndExactOutbox(t *testing.T) {
	oldContent, oldAccepted, oldOwner := admitSessionDetectiveContent, sessionDetectiveAcceptedContext, sessionDetectiveCurrentOwner
	t.Cleanup(func() {
		admitSessionDetectiveContent, sessionDetectiveAcceptedContext, sessionDetectiveCurrentOwner = oldContent, oldAccepted, oldOwner
	})
	for _, kind := range []string{"content", "repository", "owner", "outbox", "version", "binding"} {
		t.Run(kind, func(t *testing.T) {
			sentinel := errors.New("current owner unavailable")
			admitSessionDetectiveContent = func(context.Context, string, string, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (*sessioncontext.Service, sessioncontext.Principal, error) {
				if kind == "content" {
					return nil, sessioncontext.Principal{}, sentinel
				}
				return nil, sessioncontext.Principal{}, nil
			}
			sessionDetectiveAcceptedContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
				if kind == "repository" {
					return nil, sessioncontext.Principal{}, nil, sentinel
				}
				return nil, sessioncontext.Principal{}, nil, nil
			}
			sessionDetectiveCurrentOwner = func(*sessioncontext.Service, func(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error)) (sessionDetectiveOperationOwner, error) {
				if kind == "owner" {
					return nil, sentinel
				}
				owner := &nativeTransportOwner{}
				if kind == "outbox" {
					owner.kind = "handoff"
					owner.failure = sentinel
				}
				return owner, nil
			}
			version := int64(1)
			expected := sessioncontext.EvidenceHandoff{State: "pending"}
			if kind == "version" {
				version++
			}
			if kind == "binding" {
				expected.CaseID = "different"
			}
			e := authorizeSessionDetectiveHandoff(context.Background(), api.SessionDetectiveOperationScope{}, sessioncontext.Principal{}, nativeTransportStore{}, version, apicontract.SessionInputReference{}, sessioncontext.DetectiveFence{}, expected)
			if e == nil {
				t.Fatal("stale/denied handoff admitted")
			}
		})
	}
}
