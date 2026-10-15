package endpoints

import (
	"context"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/sessionstore"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"testing"
)

func TestNativeRuntimeConstructionFailsClosedAtEveryOwnerBoundary(t *testing.T) {
	oldContent, oldCurrent := admitSessionDetectiveContent, sessionDetectiveCurrentOwner
	oldAccepted, oldProject, oldPrivate, oldStore, oldOwner, oldFence := sessionDetectiveAcceptedContext, sessionDetectiveProjectDirectory, sessionDetectivePrivateDirectory, sessionDetectiveLocalStore, sessionDetectiveNewOwner, sessionDetectiveConfigureFence
	t.Cleanup(func() {
		sessionDetectiveAcceptedContext, sessionDetectiveProjectDirectory, sessionDetectivePrivateDirectory, sessionDetectiveLocalStore, sessionDetectiveNewOwner, sessionDetectiveConfigureFence = oldAccepted, oldProject, oldPrivate, oldStore, oldOwner, oldFence
		admitSessionDetectiveContent, sessionDetectiveCurrentOwner = oldContent, oldCurrent
		_ = api.CloseSessionContext()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, map[string]string{"project": t.TempDir()}, api.Capabilities{NativeSessionDetective: true, AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	for _, kind := range []string{"accepted", "project", "private", "store", "owner", "fence", "valid"} {
		t.Run(kind, func(t *testing.T) {
			sessionDetectiveAcceptedContext, sessionDetectiveProjectDirectory, sessionDetectivePrivateDirectory, sessionDetectiveLocalStore, sessionDetectiveNewOwner, sessionDetectiveConfigureFence = oldAccepted, oldProject, oldPrivate, oldStore, oldOwner, oldFence
			sentinel := errors.New("owner admission unavailable")
			switch kind {
			case "accepted":
				sessionDetectiveAcceptedContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, *sessionstore.NativeRepository, error) {
					return nil, sessioncontext.Principal{}, nil, sentinel
				}
			case "project":
				sessionDetectiveProjectDirectory = func(string) (string, bool) { return "", false }
			case "private":
				sessionDetectivePrivateDirectory = func(string) (string, error) { return "", sentinel }
			case "store":
				sessionDetectiveLocalStore = func(string, string, string) (*detective.LocalStore, error) { return nil, sentinel }
			case "owner":
				sessionDetectiveNewOwner = func(*sessioncontext.Service, func(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) error, func(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error)) (*sessioncontext.DetectiveOwner, error) {
					return nil, sentinel
				}
			case "fence":
				sessionDetectiveConfigureFence = func(*detective.LocalStore, func(context.Context, sessioncontext.DetectiveFence) error) error {
					return sentinel
				}
			}
			store, service, _, owner, _, e := sessionDetectiveNativeStore(context.Background(), "project", "local")
			if kind == "valid" {
				if e != nil || store == nil || service == nil || owner == nil {
					t.Fatal(e)
				}
			} else if e == nil || store != nil || service != nil || owner != nil {
				t.Fatal("partial runtime admitted", e)
			}
		})
	}
	sessionDetectiveNewOwner = oldOwner
	admitSessionDetectiveContent = func(context.Context, string, string, sessioncontext.Principal, sessioncontext.DetectiveIdentity) (*sessioncontext.Service, sessioncontext.Principal, error) {
		return nil, sessioncontext.Principal{}, nil
	}
	sentinel := errors.New("current native fence owner unavailable")
	sessionDetectiveCurrentOwner = func(*sessioncontext.Service, func(context.Context, sessioncontext.DetectiveIdentity) (sessioncontext.DetectiveObservation, error)) (sessionDetectiveOperationOwner, error) {
		return nil, sentinel
	}
	sessionDetectiveConfigureFence = func(_ *detective.LocalStore, check func(context.Context, sessioncontext.DetectiveFence) error) error {
		return check(context.Background(), sessioncontext.DetectiveFence{})
	}
	if _, _, _, _, _, e := sessionDetectiveNativeStore(context.Background(), "project", "local"); !errors.Is(e, sentinel) {
		t.Fatal("missing current fence owner admitted", e)
	}

}
