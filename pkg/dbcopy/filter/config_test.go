package filter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseConfigFile_Full(t *testing.T) {
	d, err := ParseConfigFile("testdata/full.yaml")
	if err != nil {
		t.Fatalf("ParseConfigFile: %v", err)
	}
	if got := d.IncludeTables; len(got) != 1 || got[0] != "Customer" {
		t.Errorf("IncludeTables = %v, want [Customer]", got)
	}
	if got := d.LimitsByTable["Customer"]; got != 5 {
		t.Errorf("Limit[Customer] = %d, want 5", got)
	}
	grp := d.Where["Customer"]
	if grp == nil || len(grp.Conditions) != 1 {
		t.Fatalf("Where[Customer] missing single condition: %+v", grp)
	}
	if grp.Conditions[0].Field != "Country" || grp.Conditions[0].Value != "USA" {
		t.Errorf("Where condition = %+v", grp.Conditions[0])
	}
}

func TestParseConfigFile_ReservedColumnsKey(t *testing.T) {
	_, err := ParseConfigFile("testdata/reserved-columns.yaml")
	if err == nil {
		t.Fatal("expected error for reserved `columns:` key")
	}
	if !strings.Contains(err.Error(), "columns") {
		t.Errorf("error %q must name the reserved `columns` key", err)
	}
	if !strings.Contains(err.Error(), "deferred") && !strings.Contains(err.Error(), "future") {
		t.Errorf("error %q should mention the deferral", err)
	}
}

func TestParseConfigFile_ReservedOrKey(t *testing.T) {
	_, err := ParseConfigFile("testdata/reserved-or.yaml")
	if err == nil {
		t.Fatal("expected error for reserved `or:` subkey")
	}
	if !strings.Contains(err.Error(), "or") {
		t.Errorf("error %q must name the reserved `or` key", err)
	}
	if !strings.Contains(err.Error(), "deferred") && !strings.Contains(err.Error(), "future") {
		t.Errorf("error %q should mention the deferral", err)
	}
}

func TestParseConfigFile_BadKey(t *testing.T) {
	_, err := ParseConfigFile("testdata/bad-key.yaml")
	if err == nil || !strings.Contains(err.Error(), "subset") {
		t.Fatalf("expected error naming unknown key 'subset', got %v", err)
	}
}

// TestParseConfigFile_ReadError covers the os.ReadFile error branch (line 36-38).
func TestParseConfigFile_ReadError(t *testing.T) {
	_, err := ParseConfigFile(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "read config file") {
		t.Fatalf("error %q should wrap read error", err)
	}
}

// TestParseConfigFile_InvalidYAML covers the yaml.Unmarshal error on the generic pass (line 43-45).
func TestParseConfigFile_InvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte(":\tinvalid: yaml: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseConfigFile(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("error %q should wrap parse error", err)
	}
}

// TestParseConfigFile_UnknownNestedField covers the KnownFields strict-decode error (line 85-87).
func TestParseConfigFile_UnknownNestedField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown-nested.yaml")
	content := "include:\n  - users\nwhere:\n  users:\n    - field: id\n      op: \"=\"\n      value: \"1\"\n      unknownkey: bad\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseConfigFile(path)
	if err == nil {
		t.Fatal("expected error for unknown nested field under where entry")
	}
	if !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("error %q should wrap strict-decode error", err)
	}
}

// TestParseConfigFile_WhereNotSlice covers the list-not-slice continue branch (line 63-65).
// When where:<table> is a scalar (not a list), the generic walk skips that entry.
func TestParseConfigFile_WhereNotSlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "where-scalar.yaml")
	// "where: {users: scalar}" — entries will be a string, not []any, triggering the continue.
	content := "where:\n  users: \"scalar-not-a-list\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// The strict decoder will reject this because configFile.Where expects []configWhereEntry.
	_, err := ParseConfigFile(path)
	if err == nil {
		t.Fatal("expected decode error for scalar where value")
	}
}

// TestParseConfigFile_WhereEntryNotMap covers the entry-not-map continue branch (line 68-70).
// When a list entry under where:<table> is a scalar (not a map), the generic walk skips it.
func TestParseConfigFile_WhereEntryNotMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "where-entry-scalar.yaml")
	// "where: {users: [scalar]}" — the list entry is a string, not map[string]any.
	content := "where:\n  users:\n    - scalar_entry\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// The strict decoder will reject this because configWhereEntry is a struct, not a string.
	_, err := ParseConfigFile(path)
	if err == nil {
		t.Fatal("expected decode error for scalar entry under where list")
	}
}
