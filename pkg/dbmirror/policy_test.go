package dbmirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPolicy(t *testing.T) {
	policy := testPolicy(t)
	if policy.DefinitionsDir() != filepath.Join("testdata", "mirror") || policy.OutputDir() != filepath.Join("testdata", "out") {
		t.Errorf("%q %q", policy.DefinitionsDir(), policy.OutputDir())
	}
	if (&Policy{Path: "x/p.yaml", Output: "/abs"}).OutputDir() != "/abs" || (&Policy{Output: "rel"}).OutputDir() != "rel" || (&Policy{Path: "x/p.yaml"}).OutputDir() != "" {
		t.Error("resolve")
	}
	if _, err := ReadPolicy(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("read a missing file")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("format: other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPolicy(bad); err == nil || !strings.HasPrefix(err.Error(), bad+": ") {
		t.Errorf("%v", err)
	}
	for text, want := range map[string]string{
		"": "empty",
		"format: datatug-mirror-policy/v1\ntoken: x\n":                              "field token not found",
		"format: datatug-mirror-policy/v1\n---\nformat: datatug-mirror-policy/v1\n": "one YAML document",
		"format: [": "did not find",
	} {
		if _, err := ParsePolicy([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestPolicyCheck(t *testing.T) {
	defs := testDefinitions(t)
	for policy, want := range map[*Policy]string{
		{Copy: []string{"nope"}}:                                                `copy: collection "nope" is not defined`,
		{Exclude: []string{"nope"}}:                                             `exclude: collection "nope" is not defined`,
		{Copy: []string{"teams"}, Exclude: []string{"teams"}}:                   "both copy and exclude",
		{Mask: map[string][]string{"teams": {"x"}}}:                             "must also be listed in copy",
		{Copy: []string{"teams"}, Mask: map[string][]string{"teams": {"a..b"}}}: "is not a field path",
		{Copy: []string{"teams"}, Mask: map[string][]string{"teams": {""}}}:     "is not a field path",
	} {
		if err := policy.Check(defs); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v", policy, err)
		}
	}
}
