package dbmirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingitdb/ingitdb-go/ingitdb"
)

func TestLoadDefinitions(t *testing.T) {
	defs := testDefinitions(t)
	var ids []string
	for _, spec := range defs.Specs {
		ids = append(ids, spec.ID)
	}
	if got := strings.Join(ids, ","); got != "ext,ext/bundles,ext/customers,ext/loose,ext/secrets,teams,teams/ext,teams/ext/events,teams/ext/events/notes,teams/logs" {
		t.Error(got)
	}
	spec := defs.Spec("teams/ext/events/notes")
	if spec.Leaf() != "notes" || spec.Depth() != 4 || defs.Spec("nope") != nil {
		t.Errorf("%+v", spec)
	}
	if got := spec.recordFile(strings.Split("teams/a/ext/b/events/c/notes/d", "/")); got != filepath.FromSlash("teams/a/ext/b/events/c/notes/d.json") {
		t.Error(got)
	}
	if !spec.matches(strings.Split("teams/a/ext/b/events/c/notes/d", "/")) || spec.matches(strings.Split("teams/a/ext/b/other/c/notes/d", "/")) || spec.matches([]string{"teams", "a"}) {
		t.Error("matches")
	}
}

func TestLoadDefinitionsFailures(t *testing.T) {
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".ingitdb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".ingitdb", "root-collections.yaml"), []byte("a: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDefinitions(broken); err == nil || !strings.Contains(err.Error(), "read inGitDB definitions") {
		t.Errorf("%v", err)
	}
	withSeam(t, &absPath, func(string) (string, error) { return "", errBoom })
	if _, err := LoadDefinitions("."); err != errBoom {
		t.Error(err)
	}
	withSeam(t, &absPath, filepath.Abs)
	withSeam(t, &readDefinition, func(dir string, _ ...ingitdb.ReadOption) (*ingitdb.Definition, error) {
		return &ingitdb.Definition{}, nil
	})
	if _, err := LoadDefinitions("."); err == nil || !strings.Contains(err.Error(), "define no collection") {
		t.Errorf("%v", err)
	}
	withSeam(t, &readDefinition, func(dir string, _ ...ingitdb.ReadOption) (*ingitdb.Definition, error) {
		return &ingitdb.Definition{Collections: map[string]*ingitdb.CollectionDef{"a": {DirPath: filepath.Dir(dir)}}}, nil
	})
	if _, err := LoadDefinitions("."); err == nil || !strings.Contains(err.Error(), "outside the definitions folder") {
		t.Errorf("%v", err)
	}
}

func TestSpecLayout(t *testing.T) {
	single := func(name string, format ingitdb.RecordFormat) *ingitdb.CollectionDef {
		return &ingitdb.CollectionDef{RecordFile: &ingitdb.RecordFileDef{Name: name, Format: format, RecordType: ingitdb.SingleRecord}}
	}
	for want, chain := range map[string][]*ingitdb.CollectionDef{
		"has no record_file":    {{}},
		"must name the file":    {single("{key}-{title}.json", ingitdb.RecordFormatJSON)},
		"must name the file by": {single("a/{key}.json", ingitdb.RecordFormatJSON)},
		"is not written yet":    {single("{key}.yaml", "yaml")},
		"keeps several records": {single("{key}.json", ingitdb.RecordFormatJSON), {RecordFile: &ingitdb.RecordFileDef{Name: "all.json", RecordType: ingitdb.ListOfRecords}}},
	} {
		spec := &Spec{chain: chain, Def: chain[len(chain)-1]}
		if got := spec.layout(); !strings.Contains(got, want) {
			t.Errorf("%s: %q", want, got)
		}
	}
	records := "$records"
	spec := &Spec{chain: []*ingitdb.CollectionDef{{RecordFile: &ingitdb.RecordFileDef{Name: "{key}.json", Format: "json", RecordType: ingitdb.SingleRecord, RecordsDir: &records}}}}
	spec.Def = spec.chain[0]
	if spec.layout() != "" || spec.recordFile([]string{"a", "1"}) != filepath.FromSlash("$records/1.json") {
		t.Error(spec.recordFile([]string{"a", "1"}))
	}
}

func TestDefinitionFileAndCopyTo(t *testing.T) {
	defs := testDefinitions(t)
	for rel, want := range map[string]bool{
		".ingitdb/root-collections.yaml":    true,
		"teams/.collection/definition.yaml": true,
		".git/config":                       false,
		"teams/t0.json":                     false,
		"teams/definition.yaml":             true,
		"teams/t1/definition.yaml":          false,
		"README.md":                         false,
	} {
		if got := defs.definitionFile(filepath.FromSlash(rel)); got != want {
			t.Errorf("%s: %v", rel, got)
		}
	}
	// Folders .git and .datatug-mirror of the definitions are not walked.
	src := t.TempDir()
	if err := os.CopyFS(src, os.DirFS(defs.Dir)); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".git", stateDirName} {
		if err := os.MkdirAll(filepath.Join(src, dir, ".collection"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, dir, ".collection", "x"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("teams", filepath.Join(src, ".ingitdb", "link")); err != nil {
		t.Fatal(err)
	}
	copied := &Definitions{Dir: src, Specs: defs.Specs}
	out := t.TempDir()
	if err := copied.CopyTo(out); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{".git", stateDirName, ".ingitdb/link", "teams/t0.json"} {
		if _, err := os.Lstat(filepath.Join(out, missing)); !os.IsNotExist(err) {
			t.Errorf("%s copied", missing)
		}
	}
	// A definition that cannot be read, a walk that fails, and an output that cannot be written.
	unreadable := filepath.Join(src, ".ingitdb", "root-collections.yaml")
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	if err := copied.CopyTo(t.TempDir()); err == nil {
		t.Error("copied an unreadable definition")
	}
	if err := os.Chmod(unreadable, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&Definitions{Dir: filepath.Join(src, "missing")}).CopyTo(out); err == nil {
		t.Error("walked a missing folder")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copied.CopyTo(file); err == nil {
		t.Error("wrote into a file")
	}
}
