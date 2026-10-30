package secureread

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

func TestSessionSplitNormalizesLegacyOnlyAtConstruction(t *testing.T) {
	legacy, err := NormalizeSession(Session{Unrestricted: true})
	if err != nil || !legacy.LocalProjectOwner || !legacy.DatabaseProfile.PolicyFreeReads() || !legacy.AllowAnonymousLocalIdentity {
		t.Fatalf("legacy profile: %+v %v", legacy, err)
	}
	legacy.LocalProjectOwner = false
	legacy.AllowAnonymousLocalIdentity = false
	again, err := NormalizeSession(legacy)
	if err != nil || !reflect.DeepEqual(again, legacy) {
		t.Fatalf("normalization restored withdrawn powers: %+v %v", again, err)
	}
	governed, err := NormalizeSession(Session{})
	if err != nil || governed.DatabaseProfile != DatabasePolicyGoverned || governed.LocalProjectOwner || governed.AllowAnonymousLocalIdentity {
		t.Fatalf("default authority widened: %+v %v", governed, err)
	}
	explicit, err := NormalizeSession(Session{DatabaseProfile: DatabasePolicyFreeReads})
	if err != nil || explicit.LocalProjectOwner || explicit.AllowAnonymousLocalIdentity || !explicit.Unrestricted {
		t.Fatalf("explicit read profile acquired other powers: %+v %v", explicit, err)
	}
}

func TestSessionSplitProjectOwnerDoesNotAuthorizeDatabaseReads(t *testing.T) {
	for _, owner := range []bool{false, true} {
		session := aliceSession(t, productsOnlyPolicy)
		session.LocalProjectOwner = owner
		executor := NewExecutor(session)
		if err := executor.Authorize(context.Background(), customersQuery(), nil); !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("owner=%v bypassed denied source: %v", owner, err)
		}
		if err := executor.Authorize(context.Background(), productsQuery(), nil); err != nil {
			t.Fatalf("owner=%v lost permitted read: %v", owner, err)
		}
		if err := executor.CanReadWholeCollection(context.Background(), "customers"); err == nil {
			t.Fatalf("owner=%v bypassed JOIN policy", owner)
		}
		if _, err := executor.RunNativeSQL(context.Background(), "unsupported://must-not-open", "SELECT 1"); !errors.Is(err, ErrOpaqueSQLNotGranted) {
			t.Fatalf("owner=%v acquired raw access: %v", owner, err)
		}
	}
	// The legacy policy-free profile permits absence of policies, not removal
	// of a policy supplied by the operator.
	loaded := aliceSession(t, productsOnlyPolicy)
	loaded.DatabaseProfile = DatabasePolicyFreeReads
	if err := NewExecutor(loaded).Authorize(context.Background(), customersQuery(), nil); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("policy-free profile bypassed an existing policy", err)
	}
	reader := NewExecutor(Session{DatabaseProfile: DatabasePolicyFreeReads})
	if err := reader.Authorize(context.Background(), customersQuery(), nil); err != nil {
		t.Fatal("non-owner lost legacy policy-free read", err)
	}
	if err := reader.CanReadWholeCollection(context.Background(), "customers"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSplitInvalidConfigurationFailsBeforeAnySourceOpen(t *testing.T) {
	old := openSourceFn
	t.Cleanup(func() { openSourceFn = old })
	opens := 0
	openSourceFn = func(context.Context, string, bool, int) (dal.DB, func(), error) {
		opens++
		return nil, func() {}, errors.New("unexpected open")
	}
	for _, input := range []Session{
		{DatabaseProfile: DatabaseProfile(255), LocalProjectOwner: true, AllowAnonymousLocalIdentity: true, Policies: []accesspolicies.Loaded{{Policy: access.MustPolicy("allow", access.Root(access.Allow(access.ReadWrite)))}}},
		{DatabaseProfile: DatabasePolicyGoverned, Unrestricted: true, LocalProjectOwner: true},
	} {
		closed, err := NormalizeSession(input)
		if !errors.Is(err, ErrInvalidSession) || closed.LocalProjectOwner || closed.AllowAnonymousLocalIdentity || len(closed.Policies) > 0 || closed.Principal != nil {
			t.Fatalf("invalid retained authority: %+v %v", closed, err)
		}
		again, err := NormalizeSession(closed)
		if !errors.Is(err, ErrInvalidSession) || !reflect.DeepEqual(again, closed) {
			t.Fatalf("closed profile changed: %+v %v", again, err)
		}
		e := NewExecutor(closed)
		ctx := context.Background()
		if err = e.Authorize(ctx, customersQuery(), nil); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if _, err = e.RunStructured(ctx, "unused", customersQuery(), nil); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if _, err = e.RunNativeSQL(ctx, "unused", "SELECT 1"); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if _, err = e.StreamFederatedTugQL(ctx, nil, nil, nil); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if _, err = e.RunExactAggregateTugQL(ctx, "unused", nil, nil, nil, nil); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if _, err = e.RunRecursiveTugQL(ctx, "unused", nil, nil); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if _, err = e.RunSnapshot(ctx, "customers", apicontract.Recordset{}); !errors.Is(err, ErrInvalidSession) {
			t.Fatal(err)
		}
		if err = e.CanReadWholeCollection(ctx, "customers"); err == nil {
			t.Fatal("invalid JOIN profile allowed")
		}
	}
	if opens != 0 {
		t.Fatalf("invalid configuration opened %d sources", opens)
	}
}
