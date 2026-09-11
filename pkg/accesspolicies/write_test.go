package accesspolicies

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"
)

// projectWritePolicy binds one rule set per role: admin (read/write on
// everything, as datatug-demo-projects/demo-project-1 grants it), support
// (data reads only), reader (read on everything, no write), curator (write
// on project queries only) and conditional (a row-conditioned write grant,
// which project-file writes cannot enforce).
const projectWritePolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: project-write-test
default: deny
ruleSets:
  admin:
    - path: /**
      rules:
        - id: admin-full-access
          effect: allow
          operations: [readwrite]
  support:
    - path: /Customer
      rules:
        - id: customers-support
          effect: allow
          operations: [query]
  reader:
    - path: /**
      rules:
        - id: read-everything
          effect: allow
          operations: [read]
  curator:
    - path: /datatug_projects/*/queries/*
      rules:
        - id: curate-queries
          effect: allow
          operations: [write]
  conditional:
    - path: /datatug_projects/*/queries/*
      rules:
        - id: own-queries-only
          effect: allow
          operations: [write]
          where:
            op: "=="
            left: { field: owner }
            right: { param: currentUser }
bindings:
  roles:
    admin: [admin]
    support: [support]
    reader: [reader]
    curator: [curator]
    conditional: [conditional]
`

// customersOnlyPolicy grants nothing on project files.
const customersOnlyPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: customers-only
default: deny
scopes:
  - path: /Customer
    rules:
      - id: list
        effect: allow
        operations: [query]
`

// serverPolicyDir stands in for a policy file's location on the serving
// machine, which a denial sent to a browser must never reveal.
const serverPolicyDir = "/srv/secret-policies/"

func decodeLoaded(t *testing.T, name, doc string) Loaded {
	t.Helper()
	source := serverPolicyDir + name
	loaded, err := DecodeLoaded([]byte(doc), access.YAMLCodec{}, source)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return loaded
}

func principalWithRoles(id string, roles ...string) *access.Principal {
	return &access.Principal{ID: id, Roles: roles}
}

func TestProjectQueryResource(t *testing.T) {
	got := ProjectQueryResource("demo-project-1", "customers/customer-invoices").String()
	if !strings.HasPrefix(got, "/"+ProjectsCollection+"/demo-project-1/"+ProjectQueriesCollection+"/") {
		t.Errorf("ProjectQueryResource = %q, want it under /%s/demo-project-1/%s/", got, ProjectsCollection, ProjectQueriesCollection)
	}
	if strings.Count(got, "/") != 4 {
		t.Errorf("ProjectQueryResource = %q: a folder-qualified query id must stay one path segment", got)
	}
}

func TestAuthorizeWrite(t *testing.T) {
	policies := []Loaded{decodeLoaded(t, "project-write.yaml", projectWritePolicy)}
	resource := ProjectQueryResource("demo-project-1", "customers/customer-invoices")
	tests := []struct {
		name       string
		principal  *access.Principal
		operation  access.Operations
		wantDenied bool
		wantReason string
	}{
		{name: "admin inserts", principal: principalWithRoles("admin", "admin"), operation: access.Insert},
		{name: "admin updates", principal: principalWithRoles("admin", "admin"), operation: access.Update},
		{name: "admin sets", principal: principalWithRoles("admin", "admin"), operation: access.Set},
		{name: "admin deletes", principal: principalWithRoles("admin", "admin"), operation: access.Delete},
		{name: "a query-scoped write grant", principal: principalWithRoles("cora", "curator"), operation: access.Insert},
		{name: "a data-only principal", principal: principalWithRoles("sam", "support"), operation: access.Insert, wantDenied: true},
		{name: "a read-only principal", principal: principalWithRoles("rita", "reader"), operation: access.Update, wantDenied: true},
		{name: "a principal with no binding", principal: principalWithRoles("mallory"), operation: access.Insert, wantDenied: true},
		{name: "no principal", principal: nil, operation: access.Insert, wantDenied: true},
		{name: "a row-conditioned write grant", principal: principalWithRoles("owen", "conditional"), operation: access.Update,
			wantDenied: true, wantReason: "condition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeWrite(context.Background(), WriteOptions{Principal: tt.principal, Policies: policies}, tt.operation, resource)
			if !tt.wantDenied {
				if err != nil {
					t.Fatalf("expected the write to be authorized, got %v", err)
				}
				return
			}
			if !errors.Is(err, access.ErrAccessDenied) {
				t.Fatalf("expected an access denial, got %v", err)
			}
			var denied *WriteDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected a *WriteDeniedError, got %T: %v", err, err)
			}
			if denied.Policy != "project-write-test" {
				t.Errorf("Policy = %q, want project-write-test", denied.Policy)
			}
			if strings.Contains(err.Error(), serverPolicyDir) {
				t.Errorf("denial %q reveals the policy file's server path", err)
			}
			if tt.wantReason != "" && !strings.Contains(denied.Reason, tt.wantReason) {
				t.Errorf("Reason = %q, want it to mention %q", denied.Reason, tt.wantReason)
			}
		})
	}
}

func TestAuthorizeWrite_DenyByDefaultWithoutPolicies(t *testing.T) {
	err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("admin", "admin")}, access.Insert,
		ProjectQueryResource("p", "q"))
	var denied *WriteDeniedError
	if !errors.As(err, &denied) || !errors.Is(err, access.ErrAccessDenied) {
		t.Fatalf("expected a deny-by-default *WriteDeniedError, got %v", err)
	}
	if denied.Policy != "" {
		t.Errorf("Policy = %q, want empty: no policy decided this", denied.Policy)
	}
}

func TestAuthorizeWrite_UnrestrictedSessionIsTheLocalOwner(t *testing.T) {
	if err := AuthorizeWrite(context.Background(), WriteOptions{Unrestricted: true}, access.Insert, ProjectQueryResource("p", "q")); err != nil {
		t.Fatalf("an unrestricted (--no-policies) session must be allowed to write, got %v", err)
	}
}

func TestAuthorizeWrite_EveryPolicyMustAllow(t *testing.T) {
	policies := []Loaded{
		decodeLoaded(t, "project-write.yaml", projectWritePolicy),
		decodeLoaded(t, "customers-only.yaml", customersOnlyPolicy),
	}
	err := AuthorizeWrite(context.Background(), WriteOptions{Principal: principalWithRoles("admin", "admin"), Policies: policies},
		access.Insert, ProjectQueryResource("p", "q"))
	var denied *WriteDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected the second policy to deny, got %v", err)
	}
	if denied.Policy != "customers-only" {
		t.Errorf("Policy = %q, want customers-only", denied.Policy)
	}
}

func TestAuthorizeWrite_RejectsNonWriteOperations(t *testing.T) {
	for _, op := range []access.Operations{access.Query, access.Get, access.Write, 0} {
		err := AuthorizeWrite(context.Background(), WriteOptions{Unrestricted: true}, op, ProjectQueryResource("p", "q"))
		if err == nil {
			t.Errorf("operation %v: expected an error, got nil", op)
		}
	}
}

func TestWriteDeniedError_Error(t *testing.T) {
	err := &WriteDeniedError{Policy: "demo", Operation: access.Insert, Resource: "/datatug_projects/p/queries/q", Reason: "no rule allows it"}
	got := err.Error()
	for _, want := range []string{`"demo"`, "insert", "/datatug_projects/p/queries/q", "no rule allows it"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
	noPolicy := (&WriteDeniedError{Operation: access.Update, Resource: "/x", Reason: "deny by default"}).Error()
	if strings.Contains(noPolicy, "policy") {
		t.Errorf("Error() = %q names a policy although none decided", noPolicy)
	}
}

// TestConstrainedWrite drives every branch of the check that refuses a
// grant a project file cannot enforce.
func TestConstrainedWrite(t *testing.T) {
	var condition dal.Condition = fakeCondition{}
	writes := func(w *access.WriteResidual) []*access.WriteResidual { return []*access.WriteResidual{w} }
	tests := []struct {
		name     string
		decision access.Decision
		want     bool
	}{
		{"an unconditional allow", access.Decision{Allowed: true}, false},
		{"nil residual and write entries", access.Decision{Allowed: true, Residuals: []dal.Condition{nil}, Writes: []*access.WriteResidual{nil}}, false},
		{"a row residual", access.Decision{Allowed: true, Residuals: []dal.Condition{condition}}, true},
		{"conditional alternatives", access.Decision{Allowed: true, Writes: writes(&access.WriteResidual{
			Alternatives: []access.WriteAlternative{{Where: condition}}, Terminal: &access.WriteAlternative{}})}, true},
		{"a walk that ends without an allow", access.Decision{Allowed: true, Writes: writes(&access.WriteResidual{})}, true},
		{"an unconstrained terminal allow", access.Decision{Allowed: true, Writes: writes(&access.WriteResidual{Terminal: &access.WriteAlternative{}})}, false},
		{"a terminal where", access.Decision{Allowed: true, Writes: writes(&access.WriteResidual{Terminal: &access.WriteAlternative{Where: condition}})}, true},
		{"a terminal check", access.Decision{Allowed: true, Writes: writes(&access.WriteResidual{Terminal: &access.WriteAlternative{Check: condition}})}, true},
		{"a terminal field list", access.Decision{Allowed: true, Writes: writes(&access.WriteResidual{Terminal: &access.WriteAlternative{Fields: []string{"title"}}})}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := constrainedWrite(tt.decision); got != tt.want {
				t.Errorf("constrainedWrite = %v, want %v", got, tt.want)
			}
		})
	}
}
