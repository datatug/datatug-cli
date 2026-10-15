package secureread

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

func authorizeQuery(collection string) dal.Query {
	return dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(collection, ""))).SelectIntoRecord(nil)
}

func TestExecutorAuthorize(t *testing.T) {
	dir := t.TempDir()
	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: authorize-test
default: deny
ruleSets:
  support:
    - path: /Customer
      rules:
        - id: customers-support
          effect: allow
          operations: [query]
bindings:
  roles:
    support: [support]
`
	if err := os.WriteFile(filepath.Join(dir, "p.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	session, err := NewSession(SessionOptions{As: "bob", Roles: []string{"support"}, PoliciesDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutor(session)
	ctx := context.Background()
	if err := e.Authorize(ctx, authorizeQuery("Customer"), nil); err != nil {
		t.Fatalf("Customer: %v", err)
	}
	// A collection that is denied and one that does not exist are denied the same way.
	for _, name := range []string{"Invoice", "NoSuchTable"} {
		err := e.Authorize(ctx, authorizeQuery(name), nil)
		if err == nil || !errors.Is(err, ErrAccessDenied) {
			t.Fatalf("%s: err = %v, want access denied", name, err)
		}
	}
	free, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewExecutor(free).Authorize(ctx, authorizeQuery("Anything"), nil); err != nil {
		t.Fatalf("unrestricted: %v", err)
	}
	none := NewExecutor(Session{Policies: []accesspolicies.Loaded{}})
	if err := none.Authorize(ctx, authorizeQuery("X"), nil); err == nil {
		t.Fatal("a session with no policy and not unrestricted must refuse")
	}
	// The data-less session exposes no data.
	s := authorizeSession{}
	if s.Get(ctx, nil) == nil || s.GetMulti(ctx, nil) == nil {
		t.Fatal("Get/GetMulti must not be supported")
	}
	if _, err := s.Exists(ctx, nil); err == nil {
		t.Fatal("Exists must not be supported")
	}
	if _, err := s.ExecuteQueryToRecordsetReader(ctx, authorizeQuery("X")); !errors.Is(err, errAuthorized) {
		t.Fatalf("recordset reader: %v", err)
	}
}
