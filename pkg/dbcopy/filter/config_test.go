package filter

import (
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
