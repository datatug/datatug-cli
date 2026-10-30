package secureread

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

func TestSessionSplitInvalidSessionCannotRegainPublicAuthority(t *testing.T) {
	closed, err := NormalizeSession(Session{DatabaseProfile: DatabaseProfile(255)})
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatal(err)
	}
	closed.Principal = &access.Principal{ID: "alice"}
	closed.Policies = []accesspolicies.Loaded{{Policy: access.MustPolicy("allow", access.Root(access.Allow(access.ReadWrite)))}}
	closed.DatabaseProfile = DatabasePolicyFreeReads
	closed.LocalProjectOwner = true
	closed.AllowAnonymousLocalIdentity = true
	closed.AllowOpaqueSQL = true
	closed.Unrestricted = true
	again, err := NormalizeSession(closed)
	if !errors.Is(err, ErrInvalidSession) || again.Principal != nil || len(again.Policies) != 0 || again.DatabaseProfile != DatabaseProfileUnspecified || again.LocalProjectOwner || again.AllowAnonymousLocalIdentity || again.AllowOpaqueSQL || again.Unrestricted {
		t.Fatalf("invalid session regained public authority: %+v %v", again, err)
	}
	// Also defend the JOIN preflight against an already-bound session changed
	// inside the package; every other read entrypoint checks this sticky error.
	executor := &Executor{session: closed}
	if err := executor.CanReadWholeCollection(context.Background(), "customers"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("invalid JOIN authority: %v", err)
	}
	if err := NewExecutor(closed).Authorize(context.Background(), customersQuery(), nil); !errors.Is(err, ErrInvalidSession) {
		t.Fatal(err)
	}
}

func TestSessionSplitPolicyFreeProfilePreservesWholeCollectionPolicies(t *testing.T) {
	for _, tc := range []struct {
		name, policy, collection string
		allowed                  bool
	}{
		{"allowed", permissivePolicy, "customers", true},
		{"denied", productsOnlyPolicy, "customers", false},
		{"row", ownerScopedPolicy, "customers", false},
		{"field", fieldRestrictedPolicy, "customers", false},
		{"unrestricted other collection", ownerScopedPolicy, "products", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := aliceSession(t, tc.policy)
			session.DatabaseProfile = DatabasePolicyFreeReads
			err := NewExecutor(session).CanReadWholeCollection(context.Background(), tc.collection)
			if (err == nil) != tc.allowed {
				t.Fatalf("policy-free JOIN preflight: %v, allowed=%v", err, tc.allowed)
			}
		})
	}
}

func TestSessionSplitPolicyFreeProfileKeepsOpaquePolicyAndLimitation(t *testing.T) {
	source := newSQLiteFixture(t)
	for _, tc := range []struct {
		name, policy string
		allowed      bool
	}{
		{"opaque grant", opaqueSQLAllowedPolicy, true},
		{"no opaque grant", ownerScopedPolicy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := aliceSession(t, tc.policy)
			session.DatabaseProfile = DatabasePolicyFreeReads
			// The policy-free profile retains its historical operator-level raw
			// capability. Loaded policies must still grant OpaqueQueryScope.
			result, err := NewExecutor(session).RunNativeSQL(context.Background(), source, "SELECT name FROM products")
			if !tc.allowed {
				if !errors.Is(err, ErrAccessDenied) {
					t.Fatalf("opaque policy bypassed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Rows) != 3 || findLimitation(result.Limitations, LimitationNativeSQL) == nil {
				t.Fatalf("governed opaque result lost its mandatory limitation: %+v", result)
			}
		})
	}
}

func TestSessionSplitPolicyFreeProfileDoesNotBypassDuckDBExternalBoundary(t *testing.T) {
	session := aliceSession(t, opaqueSQLAllowedPolicy)
	session.DatabaseProfile = DatabasePolicyFreeReads
	_, err := NewExecutor(session).RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "external files and URLs") {
		t.Fatalf("loaded policies reached DuckDB native execution: %v", err)
	}
}
