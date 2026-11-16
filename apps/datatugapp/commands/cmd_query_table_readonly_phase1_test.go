package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryExactTablesReadonlyNormalCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tables.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"A", "B", "C"} {
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (id INTEGER PRIMARY KEY, label TEXT); INSERT INTO ` + table + ` VALUES (1,'value-` + table + `')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	policies := t.TempDir()
	if err := os.WriteFile(filepath.Join(policies, "tables.yaml"), []byte(`apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: exact-tables}
default: deny
scopes:
  - path: /A
    rules: [{id: a, effect: allow, operations: [query]}]
  - path: /C
    rules: [{id: c, effect: allow, operations: [query]}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--db", "sqlite://" + path, "--policies-dir", policies, "--as", "alice", "--format", "json"}
	out, stderr, code := runQuery(t, "", append(append([]string{}, base...), "--from", "A")...)
	if code != 0 || !strings.Contains(out, "value-A") {
		t.Fatalf("A: code=%d stdout=%s stderr=%s", code, out, stderr)
	}
	out, _, code = runQuery(t, "", append(append([]string{}, base...), "--from", "B")...)
	if code != 5 || out != "" {
		t.Fatalf("B: code=%d stdout=%s", code, out)
	}
	for _, joined := range []string{"B", "C"} {
		query := filepath.Join(t.TempDir(), "join.tugql.yaml")
		doc := "from:\n  name: A\n  alias: a\n  joins:\n    - from: {name: " + joined + ", alias: j}\n      on: [{left: {field: id, source: a}, op: '==', right: {field: id, source: j}}]\ncolumns: [{field: label, source: a, as: a_label}, {field: label, source: j, as: joined_label}]\n"
		if err := os.WriteFile(query, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		out, stderr, code = runQuery(t, "", append(append([]string{}, base...), "-f", query)...)
		if joined == "B" {
			if code != 5 || out != "" {
				t.Fatalf("A+B: code=%d stdout=%s stderr=%s", code, out, stderr)
			}
		} else if code != 0 || !strings.Contains(out, "value-C") {
			t.Fatalf("A+C: code=%d stdout=%s stderr=%s", code, out, stderr)
		}
	}
	bad := filepath.Join(t.TempDir(), "mutation.tugql.yaml")
	if err := os.WriteFile(bad, []byte("delete: {from: A}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, code = runQuery(t, "", append(append([]string{}, base...), "-f", bad)...)
	if code == 0 || out != "" {
		t.Fatalf("mutation document: code=%d stdout=%s", code, out)
	}
	out, stderr, code = runQuery(t, "", append(append([]string{}, base...), "--sql", "DELETE FROM A")...)
	if code == 0 || out != "" || !strings.Contains(stderr, "--allow-opaque-sql") {
		t.Fatalf("ungranted SQL: code=%d stdout=%s stderr=%s", code, out, stderr)
	}
	out, stderr, code = runQuery(t, "", append(append([]string{}, base...), "--from", "A")...)
	if code != 0 || !strings.Contains(out, "value-A") {
		t.Fatalf("denied writes changed A: %d %s %s", code, out, stderr)
	}
}
