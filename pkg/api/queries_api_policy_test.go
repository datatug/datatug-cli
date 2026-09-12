package api

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// denyQueryPolicy grants the admin role read and write on everything,
// except writes to the resources the policy path pattern names.
func denyQueryPolicy(pattern string) string {
	return `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: deny-test
default: deny
ruleSets:
  admin:
    - path: /**
      rules:
        - id: admin-full-access
          effect: allow
          operations: [readwrite]
    - path: ` + strconv.Quote(pattern) + `
      rules:
        - id: protect-query
          effect: deny
          operations: [write]
bindings:
  roles:
    admin: [admin]
`
}

// servedDenyProject serves a fresh project, with --allow-writes, as an
// admin principal under denyQueryPolicy(pattern).
func servedDenyProject(t *testing.T, pattern string) (projectID, queriesDir string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deny.yaml"), []byte(denyQueryPolicy(pattern)), 0o600); err != nil {
		t.Fatal(err)
	}
	return servedWritableProject(t, secureread.SessionOptions{As: "alice", Roles: []string{"admin"}, PoliciesDir: dir}, true)
}

// plantQuery writes an existing query file at queriesDir/rel, creating its
// folders, and returns its path and content.
func plantQuery(t *testing.T, queriesDir, rel, id string) (path, content string) {
	t.Helper()
	path = filepath.Join(queriesDir, filepath.FromSlash(rel))
	content = `{"id":` + strconv.Quote(id) + `,"title":"PROTECTED","type":"SQL"}` + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, content
}
