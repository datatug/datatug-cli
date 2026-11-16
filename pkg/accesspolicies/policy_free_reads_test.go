package accesspolicies

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

func TestExplicitPolicyFreeBaselineRetainsMandatoryQueryPolicies(t *testing.T) {
	for _, source := range []dal.RecordsetSource{dal.NewRootCollectionRef("A", ""), dal.NewQualifiedRootCollectionRef("private", "A", "")} {
		query := dal.From(source).NewQuery().SelectIntoRecordset()
		for _, recordset := range []bool{false, true} {
			for _, denied := range []bool{false, true} {
				session := &stubSession{}
				ctx := context.Background()
				if denied {
					ctx = access.WithPolicy(ctx, access.MustPolicy("deny", access.Root(access.Deny(access.Query)), access.OpaqueQueryScope(access.Deny(access.Query))))
				}
				var err error
				if recordset {
					_, err = PrepareRecordset(ctx, query, Options{Unrestricted: true}, func(dal.Query) error { return nil })
				} else {
					_, err = Run(ctx, session, query, Options{Unrestricted: true})
				}
				if denied {
					if !errors.Is(err, access.ErrAccessDenied) || session.query != nil {
						t.Fatalf("mandatory policy skipped: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if _, err := Run(context.Background(), &stubSession{}, nil, Options{Unrestricted: true}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("nil query was not refused: %v", err)
	}
	baseline := policyFreeQueryBaseline{}
	if !baseline.InspectionPure() || baseline.Name() == "" {
		t.Fatal("baseline identity")
	}
	for _, op := range []access.Operations{access.Query, access.Insert, access.Update, access.Delete} {
		r := access.Request{Operation: op}
		if (baseline.Authorize(context.Background(), r) == nil) != (op == access.Query) || (baseline.Decide(context.Background(), r).Allowed) != (op == access.Query) {
			t.Fatalf("query baseline altered write authority %v", op)
		}
	}
}

func TestReservedClientVariablesCannotBecomePolicyAuthority(t *testing.T) {
	query := dal.From(dal.NewRootCollectionRef("A", "")).NewQuery().SelectIntoRecordset()
	for _, name := range []string{"currentUser", "principal.roles", "principal.groups", "principal.id", "path.spaceID"} {
		options := Options{Unrestricted: true, Principal: &access.Principal{ID: "alice"}, Variables: map[string]any{name: "bob"}}
		source := &stubSession{}
		if _, err := Run(context.Background(), source, query, options); !errors.Is(err, ErrInvalidQuery) || source.query != nil {
			t.Fatalf("reserved %q reached data query: %v", name, err)
		}
		if _, err := PrepareRecordset(context.Background(), query, options, func(dal.Query) error { return nil }); !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("reserved %q reached recordset query: %v", name, err)
		}
	}
}
