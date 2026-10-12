package secureread

import (
	"context"
	"testing"
)

func TestCanReadWholeCollectionPreflight(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, policy, collection string
		allowed                  bool
	}{
		{"unrestricted", "", "customers", true},
		{"full access", permissivePolicy, "customers", true},
		{"denied", productsOnlyPolicy, "customers", false},
		{"row restricted", ownerScopedPolicy, "customers", false},
		{"field restricted", fieldRestrictedPolicy, "customers", false},
		{"other full target", ownerScopedPolicy, "products", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var session Session
			if tt.policy == "" {
				session = Session{Unrestricted: true}
			} else {
				session = aliceSession(t, tt.policy)
			}
			err := NewExecutor(session).CanReadWholeCollection(ctx, tt.collection)
			if (err == nil) != tt.allowed {
				t.Fatalf("CanReadWholeCollection(%q) = %v, allowed=%t", tt.collection, err, tt.allowed)
			}
		})
	}
}
