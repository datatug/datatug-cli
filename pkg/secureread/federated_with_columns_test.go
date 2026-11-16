package secureread

import (
	"context"
	"errors"
	"testing"
)

func TestWithFederatedColumnsReplacesOnlyTheGuard(t *testing.T) {
	refused := errors.New("refused")
	original := NewExecutor(Session{Unrestricted: true})
	guarded := original.WithFederatedColumns(func(context.Context, FederatedLeaf) error { return refused })
	if original.session.FederatedColumns != nil {
		t.Fatal("the original executor changed")
	}
	if !guarded.session.Unrestricted {
		t.Fatal("the session was not kept")
	}
	if err := guarded.session.FederatedColumns(context.Background(), FederatedLeaf{}); !errors.Is(err, refused) {
		t.Fatalf("guard = %v", err)
	}
}
