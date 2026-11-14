package dbmirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingitdb/ingitdb-go/ingitdb"
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
		{Copy: []string{"nope"}}:                                                                 `copy: collection "nope" is not defined`,
		{Exclude: []string{"nope"}}:                                                              `exclude: collection "nope" is not defined`,
		{Copy: []string{"teams"}, Exclude: []string{"teams"}}:                                    "both copy and exclude",
		{Mask: map[string][]string{"teams": {"x"}}}:                                              "must also be listed in copy",
		{Copy: []string{"teams"}, Mask: map[string][]string{"teams": {"a..b"}}}:                  "is not a field path",
		{Copy: []string{"teams"}, Mask: map[string][]string{"teams": {""}}}:                      "is not a field path",
		{Copy: []string{"teams"}, Mask: map[string][]string{"teams": {"titel"}}}:                 `"titel" is not a column of the collection (columns: $ID, createdAt,`,
		{Copy: []string{"teams"}, Mask: map[string][]string{"teams": {"title.x"}}}:               `"title" has type string, which holds no field "x"`,
		{Paths: map[string][]string{"teams/ext": {"teams/*/ext"}}}:                               "must also be listed in copy",
		{Copy: []string{"teams/ext"}, Paths: map[string][]string{"teams/ext": {}}}:               "lists no path",
		{Copy: []string{"teams/ext"}, Paths: map[string][]string{"teams/ext": {"teams"}}}:        "has 3 segments (a collection path) or 4",
		{Copy: []string{"teams/ext"}, Paths: map[string][]string{"teams/ext": {"teams//ext"}}}:   "a segment is empty",
		{Copy: []string{"teams/ext"}, Paths: map[string][]string{"teams/ext": {"teams/*/ex"}}}:   `segment 3 must be the collection id "ext"`,
		{Copy: []string{"teams/ext"}, Paths: map[string][]string{"teams/ext": {"teams/t*/ext"}}}: `segment 2 is a document id or "*"`,
	} {
		if err := policy.Check(defs); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v", policy, err)
		}
	}
	good := &Policy{Copy: []string{"teams/ext", "ext/customers"}, Paths: map[string][]string{"teams/ext": {"teams/*/ext", "teams/t1/ext/cal"}},
		Mask: map[string][]string{"teams/ext": {"data.a"}, "ext/customers": {"contacts", "contacts.0"}}}
	if err := good.Check(defs); err != nil {
		t.Error(err)
	}
}

func TestMaskFieldProblem(t *testing.T) {
	two, three, four := 2, 3, 4
	def := &ingitdb.CollectionDef{Columns: map[string]*ingitdb.ColumnDef{
		"tags": {Type: "[]string"}, "byKey": {Type: "map[string][]map[string]string"}, "loose": {Type: ingitdb.ColumnTypeAny},
		"names": {Type: ingitdb.ColumnTypeL10N}, "n": {Type: ingitdb.ColumnTypeInt},
		"data": {Type: "map[string]any"}, "items": {Type: "[]any"}, "scores": {Type: "[]int"}, "byScore": {Type: "map[string]int"},
		"count": {Type: ingitdb.ColumnTypeInt, Required: true}, "flag": {Type: ingitdb.ColumnTypeBool, RequiredWhen: "x"}, "kind": {Type: "string", Enum: []any{"a"}},
		"code": {Type: "string", Length: &two}, "short": {Type: "string", MaxLength: &two}, "long": {Type: "string", MinLength: &four}, "mail": {Type: "string", Format: "email"},
		"ok": {Type: "string", Length: &three, MaxLength: &three, MinLength: &three}, "anyRequired": {Type: ingitdb.ColumnTypeAny, Required: true},
	}}
	for path, want := range map[string]string{
		"tags.*": "", "tags.3": "", "byKey.k.*.x": "", "byKey.k.0.x": "", "loose": "", "names.en": "", "data": "", "data.k": "", "items": "", "items.*": "",
		"byScore.*": "", "n": "", "ok": "", "anyRequired": "", "scores": "",
		"loose.a.b.c":   `"loose" is untyped (any): the mirror cannot check a path below it`,
		"data.k.x":      `"data.k" is untyped (any)`,
		"items.*.x":     `"items.*" is untyped (any)`,
		"scores.*":      `the items of "scores" are int: a masked item becomes null`,
		"scores.2":      `the items of "scores.2" are int`,
		"count":         `"count" is a required int`,
		"flag":          `"flag" is a required bool`,
		"kind":          `"kind" takes one of a list of values (enum)`,
		"code":          `"code" has a length constraint`,
		"short":         `"short" has a length constraint`,
		"long":          `"long" has a length constraint`,
		"mail":          `"mail" declares a format or a foreign key`,
		"tags.x":        `"tags" is a list ([]string): name its items by "*" or an index, not "x"`,
		"tags.*.x":      `"tags.*" has type string, which holds no field "x"`,
		"byKey.k.x":     `"byKey.k" is a list`,
		"byKey.k.*.x.y": `"byKey.k.*.x" has type string`,
		"names.en.x":    `"names.en" has type string`,
		"n.x":           `"n" has type int`,
		"emial":         `"emial" is not a column`,
	} {
		if got := maskFieldProblem(def, path); !strings.HasPrefix(got, want) || (want == "") != (got == "") {
			t.Errorf("%s: %q", path, got)
		}
	}
}
