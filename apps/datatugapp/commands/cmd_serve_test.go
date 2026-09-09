package commands

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dtconfig"
)

// TestResolveServeAddr covers the bug fixed in cmd_serve.go: serveCommandAction
// used to dereference config.Server unconditionally (`serverConfig.Host` /
// `serverConfig.Port`), which panicked with a nil pointer whenever
// `datatug serve` ran without a `server:` section in ~/.datatug.yaml -
// including when there is no config file at all.
func TestResolveServeAddr(t *testing.T) {
	t.Run("nil server config defaults to localhost:8989", func(t *testing.T) {
		host, port := resolveServeAddr("", 0, dtconfig.Settings{})
		if host != "localhost" || port != 8989 {
			t.Fatalf("got %s:%d, want localhost:8989", host, port)
		}
	})

	t.Run("flags win over config", func(t *testing.T) {
		config := dtconfig.Settings{Server: &dtconfig.ServerConfig{
			UrlConfig: dtconfig.UrlConfig{Host: "example.com", Port: 1234},
		}}
		host, port := resolveServeAddr("0.0.0.0", 9000, config)
		if host != "0.0.0.0" || port != 9000 {
			t.Fatalf("got %s:%d, want 0.0.0.0:9000", host, port)
		}
	})

	t.Run("config fills the gap left by unset flags", func(t *testing.T) {
		config := dtconfig.Settings{Server: &dtconfig.ServerConfig{
			UrlConfig: dtconfig.UrlConfig{Host: "example.com", Port: 1234},
		}}
		host, port := resolveServeAddr("", 0, config)
		if host != "example.com" || port != 1234 {
			t.Fatalf("got %s:%d, want example.com:1234", host, port)
		}
	})

	t.Run("config present but empty still falls back to defaults", func(t *testing.T) {
		host, port := resolveServeAddr("", 0, dtconfig.Settings{Server: &dtconfig.ServerConfig{}})
		if host != "localhost" || port != 8989 {
			t.Fatalf("got %s:%d, want localhost:8989", host, port)
		}
	})
}

func TestReadServeFlags(t *testing.T) {
	cmd := serveCommandArgs()
	args := []string{
		"--host=0.0.0.0",
		"--port=9000",
		"--project=/tmp/demo-project",
		"--as=admin",
		"--role=support",
		"--role=readonly",
		"--group=team-a",
	}
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}

	flags, err := readServeFlags(cmd)
	if err != nil {
		t.Fatalf("readServeFlags: %v", err)
	}

	want := serveFlags{
		host:       "0.0.0.0",
		port:       9000,
		projectDir: "/tmp/demo-project",
		as:         "admin",
		roles:      []string{"support", "readonly"},
		groups:     []string{"team-a"},
	}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("got %+v, want %+v", flags, want)
	}
}

func TestReadServeFlagsDefaults(t *testing.T) {
	cmd := serveCommandArgs()
	if err := cmd.Flags().Parse(nil); err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}

	flags, err := readServeFlags(cmd)
	if err != nil {
		t.Fatalf("readServeFlags: %v", err)
	}
	if flags.host != "" || flags.port != 0 || flags.projectDir != "" {
		t.Fatalf("expected zero-value flags, got %+v", flags)
	}
}

// noPoliciesFallbackDir points accesspolicies' default-dir fallback
// ($DATATUG_POLICIES_DIR) at an empty, EXISTING directory for the duration
// of the test, so "no policy set anywhere" is deterministic regardless of
// what the machine running the test actually has under ~/.datatug/policies.
// It must exist (not merely be absent): accesspolicies.ResolveDir treats an
// env-configured directory as "explicit", so a missing one is a hard load
// error ("policies directory ... does not exist"), not accesspolicies.ErrNoPolicies —
// only an existing-but-empty directory resolves to the latter.
func noPoliciesFallbackDir(t *testing.T) {
	t.Helper()
	t.Setenv(accesspolicies.DirEnv, t.TempDir())
}

// projectWithPolicies creates a project directory with a policies/ subfolder
// carrying one permissive policy document, matching the
// "<project>/policies/" resolution resolveServeSession is documented to try
// first (REQ:server-acl-all-reads, brief item 1).
func projectWithPolicies(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	policiesDir := filepath.Join(dir, "policies")
	if err := os.MkdirAll(policiesDir, 0o755); err != nil {
		t.Fatalf("mkdir policies dir: %v", err)
	}
	policy := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata:\n  name: test\ndefault: deny\nscopes:\n  - path: /**\n    rules:\n      - id: all\n        effect: allow\n        operations: [readwrite]\n"
	if err := os.WriteFile(filepath.Join(policiesDir, "policy.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return dir
}

// TestResolveServeSession_ProjectPoliciesNoPrincipal_Refuses covers the hub
// Feature's resolved Open Question: a policy set exists (<project>/policies)
// but --as/--role/--group are all absent, so serve MUST refuse to start
// rather than run an anonymous secured session.
func TestResolveServeSession_ProjectPoliciesNoPrincipal_Refuses(t *testing.T) {
	projectDir := projectWithPolicies(t)
	_, err := resolveServeSession(projectDir, serveFlags{})
	if err == nil {
		t.Fatal("resolveServeSession with a policy set and no principal = nil error, want a refusal")
	}
}

// TestResolveServeSession_ProjectPoliciesWithAs_Builds covers the same
// scenario with --as supplied: serve must build a secured (non-Unrestricted)
// Session naming that principal and carrying the project's policies.
func TestResolveServeSession_ProjectPoliciesWithAs_Builds(t *testing.T) {
	projectDir := projectWithPolicies(t)
	session, err := resolveServeSession(projectDir, serveFlags{as: "alice"})
	if err != nil {
		t.Fatalf("resolveServeSession: %v", err)
	}
	if session.Unrestricted {
		t.Fatal("session.Unrestricted = true, want false (a policy set was found)")
	}
	if session.Principal == nil || session.Principal.ID != "alice" {
		t.Fatalf("session.Principal = %+v, want ID=alice", session.Principal)
	}
	if len(session.Policies) != 1 {
		t.Fatalf("len(session.Policies) = %d, want 1", len(session.Policies))
	}
}

// TestResolveServeSession_RoleOnly_Builds covers a role/group-only principal
// (no --as) still counting as "a principal was named" — REQ:principal-selection
// accepts role/group-only principals, same as secureread.NewSession.
func TestResolveServeSession_RoleOnly_Builds(t *testing.T) {
	projectDir := projectWithPolicies(t)
	session, err := resolveServeSession(projectDir, serveFlags{roles: []string{"support"}})
	if err != nil {
		t.Fatalf("resolveServeSession: %v", err)
	}
	if session.Unrestricted || session.Principal == nil {
		t.Fatalf("session = %+v, want a secured session with a role-only principal", session)
	}
}

// TestResolveServeSession_NoPolicySet_DefaultsToAdmin covers the other half
// of the resolved Open Question: no policy set exists anywhere
// (<project>/policies absent AND the accesspolicies default resolves to
// nothing), so serve MUST NOT refuse to start — it runs Unrestricted with
// the reported principal defaulted to "admin".
func TestResolveServeSession_NoPolicySet_DefaultsToAdmin(t *testing.T) {
	noPoliciesFallbackDir(t)
	projectDir := t.TempDir() // no policies/ subfolder
	session, err := resolveServeSession(projectDir, serveFlags{})
	if err != nil {
		t.Fatalf("resolveServeSession: %v", err)
	}
	if !session.Unrestricted {
		t.Fatal("session.Unrestricted = false, want true (no policy set exists)")
	}
	if session.Principal == nil || session.Principal.ID != "admin" {
		t.Fatalf("session.Principal = %+v, want ID=admin (default)", session.Principal)
	}
}

// TestResolveServeSession_NoPolicySetButAsGiven_KeepsGivenPrincipal confirms
// an explicit --as is never silently overridden by the "admin" default, even
// when no policy set exists to enforce it against.
func TestResolveServeSession_NoPolicySetButAsGiven_KeepsGivenPrincipal(t *testing.T) {
	noPoliciesFallbackDir(t)
	projectDir := t.TempDir()
	session, err := resolveServeSession(projectDir, serveFlags{as: "bob"})
	if err != nil {
		t.Fatalf("resolveServeSession: %v", err)
	}
	if !session.Unrestricted {
		t.Fatal("session.Unrestricted = false, want true (no policy set exists)")
	}
	if session.Principal == nil || session.Principal.ID != "bob" {
		t.Fatalf("session.Principal = %+v, want ID=bob (caller's own --as)", session.Principal)
	}
}

// TestResolveServeSession_NoProjectDir_FallsBackToDefaultResolution covers
// multi-project serve (no --project flag, so projectDir == ""): policy
// resolution falls back to the plain accesspolicies default, same as
// `datatug query run`, rather than trying a per-project policies/ folder.
func TestResolveServeSession_NoProjectDir_FallsBackToDefaultResolution(t *testing.T) {
	noPoliciesFallbackDir(t)
	session, err := resolveServeSession("", serveFlags{})
	if err != nil {
		t.Fatalf("resolveServeSession: %v", err)
	}
	if !session.Unrestricted || session.Principal == nil || session.Principal.ID != "admin" {
		t.Fatalf("session = %+v, want Unrestricted with default admin principal", session)
	}
}

// TestResolveServeSession_PropagatesSessionType is a light type-level sanity
// check that resolveServeSession really returns a usable secureread.Session
// (NewExecutor must accept it without further conversion).
func TestResolveServeSession_PropagatesSessionType(t *testing.T) {
	noPoliciesFallbackDir(t)
	session, err := resolveServeSession(t.TempDir(), serveFlags{})
	if err != nil {
		t.Fatalf("resolveServeSession: %v", err)
	}
	if executor := secureread.NewExecutor(session); executor == nil {
		t.Fatal("secureread.NewExecutor(session) = nil")
	}
}

// TestResolveServeSession_MalformedPolicyPropagatesError confirms a genuine
// policy-loading failure (not "no policies found") is surfaced as an error
// rather than silently treated as "no policy set".
func TestResolveServeSession_MalformedPolicyPropagatesError(t *testing.T) {
	dir := t.TempDir()
	policiesDir := filepath.Join(dir, "policies")
	if err := os.MkdirAll(policiesDir, 0o755); err != nil {
		t.Fatalf("mkdir policies dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(policiesDir, "broken.yaml"), []byte("not: [valid, yaml"), 0o600); err != nil {
		t.Fatalf("write broken policy: %v", err)
	}
	_, err := resolveServeSession(dir, serveFlags{as: "alice"})
	if err == nil {
		t.Fatal("resolveServeSession with a malformed policy document = nil error")
	}
	if errors.Is(err, accesspolicies.ErrNoPolicies) {
		t.Fatalf("resolveServeSession masked a load error as ErrNoPolicies: %v", err)
	}
}
