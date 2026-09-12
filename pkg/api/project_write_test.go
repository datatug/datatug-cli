package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// writeTestPolicy mirrors datatug-demo-projects/demo-project-1's own
// policy: admin may read and write everything, support may only query
// customers.
const writeTestPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: write-test
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
bindings:
  roles:
    admin: [admin]
    support: [support]
`

// writePoliciesDir writes writeTestPolicy into a fresh directory and
// returns it.
func writePoliciesDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "write-test.yaml"), []byte(writeTestPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// configureWriteSession configures this process's serve session the way
// `datatug serve` does, restoring an empty session afterwards.
func configureWriteSession(t *testing.T, opts secureread.SessionOptions, pathsByID map[string]string, caps Capabilities) {
	t.Helper()
	session, err := secureread.NewSession(opts)
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	ConfigureSecureSession(session, pathsByID, caps)
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
}

func TestAuthorizeProjectQueryWrite(t *testing.T) {
	policiesDir := writePoliciesDir(t)
	tests := []struct {
		name       string
		opts       secureread.SessionOptions
		caps       Capabilities
		wantDenied string // "" means allowed; otherwise a substring of the denial
	}{
		{name: "unrestricted local owner with --allow-writes", opts: secureread.SessionOptions{NoPolicies: true}, caps: Capabilities{AllowWrites: true}},
		{name: "admin with --allow-writes", opts: secureread.SessionOptions{As: "alice", Roles: []string{"admin"}, PoliciesDir: policiesDir},
			caps: Capabilities{AllowWrites: true}},
		{name: "without --allow-writes even the local owner is refused", opts: secureread.SessionOptions{NoPolicies: true},
			wantDenied: "--allow-writes"},
		{name: "without --allow-writes admin is refused", opts: secureread.SessionOptions{As: "alice", Roles: []string{"admin"}, PoliciesDir: policiesDir},
			wantDenied: "--allow-writes"},
		{name: "a read-only principal is refused", opts: secureread.SessionOptions{As: "sam", Roles: []string{"support"}, PoliciesDir: policiesDir},
			caps: Capabilities{AllowWrites: true}, wantDenied: `policy "write-test"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configureWriteSession(t, tt.opts, nil, tt.caps)
			err := AuthorizeProjectQueryWrite(context.Background(), "demo-project-1", "customers/customer-invoices", access.Insert)
			if tt.wantDenied == "" {
				if err != nil {
					t.Fatalf("expected the write to be authorized, got %v", err)
				}
				return
			}
			if !errors.Is(err, secureread.ErrAccessDenied) {
				t.Fatalf("expected an access denial, got %v", err)
			}
			var denied *accesspolicies.WriteDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("expected a *accesspolicies.WriteDeniedError, got %T", err)
			}
			if !strings.Contains(err.Error(), tt.wantDenied) {
				t.Errorf("denial %q does not mention %q", err, tt.wantDenied)
			}
			if strings.Contains(err.Error(), policiesDir) {
				t.Errorf("denial %q reveals the policies directory", err)
			}
		})
	}
}

func TestAuthorizeProjectQueryWrite_UnconfiguredSessionIsRefused(t *testing.T) {
	secureMu.Lock()
	saved := securityContextID
	savedCaps := capabilities
	securityContextID = ""
	capabilities = Capabilities{AllowWrites: true}
	secureMu.Unlock()
	t.Cleanup(func() {
		secureMu.Lock()
		securityContextID = saved
		capabilities = savedCaps
		secureMu.Unlock()
	})
	err := AuthorizeProjectQueryWrite(context.Background(), "p", "q", access.Insert)
	if !errors.Is(err, secureread.ErrAccessDenied) {
		t.Fatalf("expected an unconfigured agent to refuse project writes, got %v", err)
	}
}
