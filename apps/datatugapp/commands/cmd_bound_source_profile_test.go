package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/spf13/cobra"
)

func TestBoundSourceNamedCLIRefusesBeforeLegacyResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	dir := writeChatRunProjectFixture(t)
	if err := os.MkdirAll(filepath.Join(dir, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	// Even malformed declared bindings must refuse, never select fallback policies.
	if err := os.WriteFile(filepath.Join(dir, ".datatug", "security.json"), []byte(`{"profile":"exact-table-readonly-v1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveQueryProject(dir); !errors.Is(err, secureread.ErrSourceBinding) {
		t.Fatalf("named CLI ignored profile: %v", err)
	}
}
func TestBoundSourceServeRequiresDeclaredPolicyAndFixedFlags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	// Use an actual owned file registry; startup itself never opens a database.
	dir := writeChatRunProjectFixture(t)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "orders.sqlite"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "policies"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "policies", "tables.yaml"), []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: tables}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: read, effect: allow, operations: [query]}]}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": "chat-run-test", "environment": "local", "catalog": "orders", "sqliteFile": filepath.Join(dir, "orders.sqlite"), "requiredPolicies": []string{"policies/tables.yaml"}, "principal": map[string]any{"id": "operator"}})
	if err = os.WriteFile(filepath.Join(dir, ".datatug", "security.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []serveFlags{{}, {securityProfile: secureread.ExactTableReadProfile}, {as: "operator"}} {
		session, err := resolveServeSession(dir, flag)
		if err != nil || !session.SourceBound() || session.Principal.ID != "operator" {
			t.Fatalf("configured startup: %v", err)
		}
	}
	for _, flag := range []serveFlags{{as: "admin"}, {roles: []string{"admin"}}, {allowWrites: true}, {allowOpaqueSQL: true}, {allowLiveConns: true}, {projectsRoot: dir}, {securityProfile: "unknown"}, {policyFiles: []string{filepath.Join(dir, "missing.yaml")}}} {
		if _, err := resolveServeSession(dir, flag); err == nil {
			t.Fatalf("unsafe startup accepted: %+v", flag)
		}
	}
	if err = os.Remove(filepath.Join(dir, "policies", "tables.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveServeSession(dir, serveFlags{}); err == nil {
		t.Fatal("empty required set fell back")
	}
	if _, declared, err := secureread.LoadBoundSession(context.Background(), t.TempDir(), secureread.BoundSessionOptions{Required: true}); !declared || err == nil {
		t.Fatal("explicit profile absent fell back")
	}
}

func TestBoundSourceServeRejectsIncompleteFlagDefinitions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	for _, name := range []string{"profile", "policy", "directory"} {
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{}
			if name != "profile" {
				cmd.Flags().String(serveSecurityProfileFlag, "", "")
			}
			if name == "directory" {
				cmd.Flags().StringArray(queryPolicyFlag, nil, "")
			}
			if _, err := readServeFlags(cmd); err == nil {
				t.Fatal("missing security flag definition did not refuse startup")
			}
		})
	}
	noPoliciesFallbackDir(t)
	if _, err := resolveServeSession(t.TempDir(), serveFlags{policyFiles: []string{"unused.yaml"}}); err == nil {
		t.Fatal("extra documents silently selected legacy mode")
	}
}

func TestBoundSourceServePreListenerRegisteredProjectFence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	dir := writeChatRunProjectFixture(t)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "orders.sqlite"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "policies"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "policies", "read.yaml"), []byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: read}\ndefault: deny\nscopes: [{path: '/A', rules: [{id: read, effect: allow, operations: [query]}]}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, ".datatug", "security.json")
	cfg := map[string]any{"profile": secureread.ExactTableReadProfile, "project": "chat-run-test", "environment": "local", "catalog": "orders", "sqliteFile": filepath.Join(dir, "orders.sqlite"), "requiredPolicies": []string{"policies/read.yaml"}, "principal": map[string]any{"id": "operator"}}
	save := func() {
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	settings := filepath.Join(t.TempDir(), "settings.yaml")
	if err = os.WriteFile(settings, []byte("projects:\n  - id: chat-run-test\n    path: "+dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dtconfig.SetConfigFilePath(settings)
	t.Cleanup(func() { dtconfig.SetConfigFilePath("") })
	noPoliciesFallbackDir(t)
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	call, _ := cov100eSeams(t, nil)
	oldWrite := serveWriteKey
	t.Cleanup(func() { serveWriteKey = oldWrite })
	keyWrites := 0
	serveWriteKey = func(string, int, []byte) error { keyWrites++; return nil }
	// Actual command entry point: plain global startup must stop before keys or listener.
	if err = serveCommandAction(serveCommandArgs(), nil); !errors.Is(err, secureread.ErrSourceBinding) || keyWrites != 0 || call.paths != nil {
		t.Fatalf("implicit registered profile reached state/listener: %v writes=%d call=%+v", err, keyWrites, call)
	}
	// An explicit correctly bound selection reaches the existing listener seam,
	// never a real listener; source and fixed session are constructed normally.
	cmd := serveCommandArgs()
	if err = cmd.Flags().Set(serveProjectFlag, dir); err != nil {
		t.Fatal(err)
	}
	if err = serveCommandAction(cmd, nil); err != nil || keyWrites != 1 || len(call.paths) != 1 {
		t.Fatalf("explicit source failed normal startup wiring: %v writes=%d call=%+v", err, keyWrites, call)
	}
	call.paths = nil
	keyWrites = 0
	cfg["project"] = "different-project"
	save()
	if err = serveCommandAction(cmd, nil); !errors.Is(err, secureread.ErrSourceBinding) || keyWrites != 0 || call.paths != nil {
		t.Fatalf("project mismatch reached listener: %v writes=%d", err, keyWrites)
	}
	if err = (secureread.Session{}).ValidateBoundServer(map[string]string{"remote": "https://example.invalid/project", "legacy": t.TempDir()}, false, false, false); err != nil {
		t.Fatalf("legacy no-declaration startup changed: %v", err)
	}
}
