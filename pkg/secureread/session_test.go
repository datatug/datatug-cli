package secureread

import (
	"errors"
	"testing"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// TestNewSession_NoPrincipal_Errors covers "no principal → error": a secured
// session (policies loaded, NoPolicies unset) with no --as/--role/--group
// must fail closed rather than start an anonymous secured session
// (REQ:principal-selection).
func TestNewSession_NoPrincipal_Errors(t *testing.T) {
	dir := policyDir(t, map[string]string{"p.yaml": permissivePolicy})
	_, err := NewSession(SessionOptions{PoliciesDir: dir})
	if !errors.Is(err, ErrNoPrincipal) {
		t.Fatalf("NewSession without a principal = %v, want ErrNoPrincipal", err)
	}
}

// TestNewSession_Unrestricted_NoPrincipalRequired covers the Unrestricted
// bypass: --no-policies needs no principal at all.
func TestNewSession_Unrestricted_NoPrincipalRequired(t *testing.T) {
	session, err := NewSession(SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession(NoPolicies) = %v", err)
	}
	if !session.Unrestricted || session.Principal != nil || len(session.Policies) != 0 {
		t.Fatalf("session = %+v, want Unrestricted with no principal or policies", session)
	}
}

// TestNewSession_PrincipalFromAsRoleGroup covers building the principal from
// --as/--role/--group, mirroring cmd_query.go's own construction.
func TestNewSession_PrincipalFromAsRoleGroup(t *testing.T) {
	dir := policyDir(t, map[string]string{"p.yaml": permissivePolicy})
	session, err := NewSession(SessionOptions{
		As:          "alice",
		Roles:       []string{"support"},
		Groups:      []string{"eu"},
		PoliciesDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if session.Principal == nil {
		t.Fatal("Principal = nil")
	}
	if session.Principal.ID != "alice" {
		t.Errorf("Principal.ID = %v, want alice", session.Principal.ID)
	}
	if len(session.Principal.Roles) != 1 || session.Principal.Roles[0] != "support" {
		t.Errorf("Principal.Roles = %v", session.Principal.Roles)
	}
	if len(session.Principal.Groups) != 1 || session.Principal.Groups[0] != "eu" {
		t.Errorf("Principal.Groups = %v", session.Principal.Groups)
	}
	if len(session.Policies) != 1 {
		t.Errorf("Policies = %d, want 1", len(session.Policies))
	}
}

// TestNewSession_RoleOnly_NoAs covers a principal named only by --role (no
// --as ID) — REQ:principal-selection accepts role/group-only principals.
func TestNewSession_RoleOnly_NoAs(t *testing.T) {
	dir := policyDir(t, map[string]string{"p.yaml": permissivePolicy})
	session, err := NewSession(SessionOptions{Roles: []string{"support"}, PoliciesDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if session.Principal == nil || session.Principal.ID != nil {
		t.Fatalf("Principal = %+v, want an ID-less principal", session.Principal)
	}
}

// TestNewSession_PropagatesLoadErrors confirms NewSession surfaces
// accesspolicies.Load's own errors (e.g. no policies found and not
// unrestricted) rather than masking them.
func TestNewSession_PropagatesLoadErrors(t *testing.T) {
	_, err := NewSession(SessionOptions{As: "alice", PoliciesDir: t.TempDir()})
	if !errors.Is(err, accesspolicies.ErrNoPolicies) {
		t.Fatalf("NewSession(empty dir) = %v, want ErrNoPolicies", err)
	}
}
