package secureread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/policyscope"
)

func TestExecutorForTargetProjectsSelectorRules(t *testing.T) {
	dir := t.TempDir()
	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: instances}
default: deny
scopes:
  - path: /Customer
    rules:
      - {id: only-prod-crm, effect: allow, operations: [query], instance: prod/crm}
`
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	session, err := NewSession(SessionOptions{As: "bob", PoliciesDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(session)
	ctx := context.Background()
	if err := e.Authorize(ctx, authorizeQuery("Customer"), nil); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("an executor that does not know its instance denies: %v", err)
	}
	if err := e.ForTarget(policyscope.Target{Environment: "prod", Catalog: "crm"}).Authorize(ctx, authorizeQuery("Customer"), nil); err != nil {
		t.Fatalf("prod/crm: %v", err)
	}
	if err := e.ForTarget(policyscope.Target{Environment: "prod", Catalog: "erp"}).Authorize(ctx, authorizeQuery("Customer"), nil); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("prod/erp: %v", err)
	}
	if err := e.Authorize(ctx, authorizeQuery("Customer"), nil); !errors.Is(err, ErrAccessDenied) {
		t.Fatal("ForTarget leaves the executor it was called on unchanged")
	}
}
