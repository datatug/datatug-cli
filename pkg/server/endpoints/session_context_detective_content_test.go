package endpoints

import (
	"context"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"testing"
)

func TestNativeContentAdmissionRequiresCurrentActorScopeAndRunReference(t *testing.T) {
	oldContext, oldActor, oldRun := sessionDetectiveContentContext, sessionDetectiveActorGate, sessionDetectiveReadRun
	t.Cleanup(func() {
		sessionDetectiveContentContext, sessionDetectiveActorGate, sessionDetectiveReadRun = oldContext, oldActor, oldRun
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	p := sessioncontext.Principal{Actor: "alice", DatabaseID: "db", ProjectID: "project"}
	for _, kind := range []string{"context", "actor", "database", "project", "writes", "untrusted", "binding", "run", "missing-reference", "scope", "valid"} {
		t.Run(kind, func(t *testing.T) {
			api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: kind != "untrusted"}, nil, api.Capabilities{AllowWrites: kind != "writes"})
			sentinel := errors.New("source admission unavailable")
			current := p
			switch kind {
			case "actor":
				current.Actor = "mallory"
			case "database":
				current.DatabaseID = "foreign"
			case "project":
				current.ProjectID = "foreign"
			}
			sessionDetectiveContentContext = func(context.Context, string, string) (*sessioncontext.Service, sessioncontext.Principal, api.SessionContextCapabilities, error) {
				if kind == "context" {
					return nil, current, api.SessionContextCapabilities{}, sentinel
				}
				return nil, current, api.SessionContextCapabilities{Environments: []api.SessionContextEnvironment{{EnvironmentID: "qa", IdentityDomain: "tenant"}}}, nil
			}
			sessionDetectiveActorGate = func(context.Context, sessioncontext.Principal, sessioncontext.DetectiveIdentity) error {
				if kind == "binding" {
					return sentinel
				}
				return nil
			}
			sessionDetectiveReadRun = func(*sessioncontext.Service, context.Context, sessioncontext.Principal, string) (sessioncontext.Run, error) {
				if kind == "run" {
					return sessioncontext.Run{}, sentinel
				}
				if kind == "missing-reference" {
					return sessioncontext.Run{}, nil
				}
				reference := &apicontract.SessionInputReference{EnvironmentID: "qa", IdentityDomain: "tenant"}
				if kind == "scope" {
					reference.IdentityDomain = "foreign"
				}
				return sessioncontext.Run{InputReference: reference}, nil
			}
			_, _, e := sessionDetectiveContentAdmission(context.Background(), "local", "project", p, sessioncontext.DetectiveIdentity{})
			if (kind == "valid") != (e == nil) {
				t.Fatal(kind, e)
			}
		})
	}
}
