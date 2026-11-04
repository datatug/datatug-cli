package secureread

import (
	"context"
	"errors"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"testing"
)

func TestRefusedSessionCannotRegainAuthority(t *testing.T) {
	cause := errors.New("owned configuration failure")
	for _, failure := range []error{cause, nil} {
		expected := failure
		if expected == nil {
			expected = ErrInvalidSession
		}
		session := RefusedSession(failure)
		session.Principal = &access.Principal{ID: "operator"}
		session.LocalProjectOwner = true
		session.Unrestricted = true
		session.DatabaseProfile = DatabasePolicyFreeReads
		session.AllowAnonymousLocalIdentity = true
		session.AllowOpaqueSQL = true
		session.Policies = []accesspolicies.Loaded{{Policy: access.MustPolicy("allow", access.Root(access.Allow(access.ReadWrite)))}}
		normalized, err := NormalizeSession(session)
		if !errors.Is(err, expected) || normalized.Principal != nil || normalized.LocalProjectOwner || normalized.Unrestricted || normalized.DatabaseProfile.PolicyFreeReads() || normalized.AllowAnonymousLocalIdentity || normalized.AllowOpaqueSQL {
			t.Fatal("refused session regained authority", normalized, err)
		}
		ctx := access.WithPolicy(context.Background(), access.MustPolicy("dynamic-allow", access.Root(access.Allow(access.Read))))
		if err = NewExecutor(session).Authorize(ctx, identityQuery("A"), nil); !errors.Is(err, expected) {
			t.Fatal("dynamic policy removed refusal", err)
		}
	}
}
