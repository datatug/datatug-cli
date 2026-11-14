package dbmirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ingitdb/ingitdb-go/ingitdb"
)

// yamlDefinitions writes definitions of a root collection kept in one YAML
// file (tags), and of groups (one YAML file per record) with a sub-collection
// kept in one YAML file per group (members).
func yamlDefinitions(t *testing.T) *Definitions {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".ingitdb/root-collections.yaml": "tags: tags\ngroups: groups\n",
		"tags/.collection/definition.yaml": `record_file:
  name: tags.yaml
  type: "map[$record_id]map[$field_name]any"
  format: yaml
columns:
  title:
    type: string
    required: true
  email:
    type: string
`,
		"groups/.collection/definition.yaml": `record_file:
  name: "{key}.yaml"
  type: map[string]any
  format: yaml
  records_dir: .
columns:
  title:
    type: string
    required: true
  size:
    type: int
  score:
    type: float
  note:
    type: string
  at:
    type: datetime
  data:
    type: map[string]any
`,
		"groups/.collection/subcollections/members/definition.yaml": `record_file:
  name: members.yaml
  type: "map[$record_id]map[$field_name]any"
  format: yaml
columns:
  name:
    type: string
    required: true
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defs, err := LoadDefinitions(dir)
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func yamlPolicy() *Policy {
	return &Policy{Copy: []string{"tags", "groups", "groups/members"}, Mask: map[string][]string{"tags": {"email"}}}
}

func runYAML(t *testing.T, src Source, opts Options) *Report {
	t.Helper()
	report, err := Run(context.Background(), src, yamlDefinitions(t), yamlPolicy(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func yamlSource() *memSource {
	m := newMemSource()
	long := strings.Repeat("é", 200) // 400 bytes: too long for a file name, fine as a key
	m.docs["tags/"+long] = map[string]any{"title": "a long id", "email": "someone@example.com"}
	m.docs["tags/123"] = map[string]any{"title": "true"}
	m.docs["tags/Work"] = map[string]any{"title": "Work"}
	m.docs["tags/work"] = map[string]any{"title": "work, in another case"}
	m.docs["groups/g1"] = map[string]any{"title": "2026-01-01", "size": int64(math.MaxInt64), "score": 1.0, "note": "two\nlines", "at": created,
		"data": map[string]any{"null": nil, "t": created, "b": []byte("x"), "nan": math.NaN(), "list": []any{}, "map": map[string]any{}}}
	m.docs["groups/g1/members/m1"] = map[string]any{"name": "One"}
	m.docs["groups/g1/members/m2"] = map[string]any{"name": "Two"}
	m.docs["groups/g2/members/m3"] = map[string]any{"name": "Three"}
	m.docs["groups/g2"] = map[string]any{"title": "null"}
	m.docs["groups/g1/members/bad"] = map[string]any{"name": int64(1)}
	return m
}

// The definition decides the form: a collection kept in one YAML file is
// written there, keyed by the real ids; one file per record is YAML too.
func TestRunWritesCollectionsKeptInOneFile(t *testing.T) {
	out := t.TempDir()
	report := runYAML(t, yamlSource(), Options{Output: out})
	if len(report.Validation) != 0 || len(report.Rejected) != 1 || report.Rejected[0].Path != "groups/g1/members/bad" {
		t.Fatalf("%+v", report)
	}
	tags := readFile(t, filepath.Join(out, "tags/tags.yaml"))
	records, err := readRecords(filepath.Join(out, "tags/tags.yaml"), yamlDefinitions(t).Spec("tags"))
	if err != nil || records[strings.Repeat("é", 200)]["email"] != maskedString || strings.Contains(tags, "someone@example.com") || !strings.Contains(tags, `"123":`) || !strings.Contains(tags, `title: "true"`) {
		t.Errorf("tags.yaml (%v):\n%s", err, tags)
	}
	// Ids that differ only in case are two keys of one map: both are kept.
	if !strings.Contains(tags, "Work:") || !strings.Contains(tags, "work:") || result(t, report, "tags").Records != 4 {
		t.Errorf("%s", tags)
	}
	if got := readFile(t, filepath.Join(out, "groups/g1/members/members.yaml")); got != "m1:\n  name: One\nm2:\n  name: Two\n" {
		t.Errorf("members of g1:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(out, "groups/g2/members/members.yaml")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(partialDir(out)); !os.IsNotExist(err) {
		t.Error("a finished run left partial records")
	}
	// A re-run of the same source changes nothing.
	again := runYAML(t, yamlSource(), Options{Output: out})
	if again.Written != 0 || len(again.Stale) != 0 {
		t.Errorf("%+v", again)
	}
}

// YAML records read back as what was written: strings that look like other
// values, doubles that are whole numbers, int64, multi-line text, tags.
func TestEncodeYAMLRoundTrips(t *testing.T) {
	spec := &Spec{Def: &ingitdb.CollectionDef{RecordFile: &ingitdb.RecordFileDef{Format: ingitdb.RecordFormatYAML}}}
	data := map[string]any{
		"strings": []any{"true", "yes", "no", "on", "null", "~", "", "123", "0x1F", "1e3", "2026-01-01", "2026-01-01T00:00:00Z", ".inf", "-", "a: b", "#x", "- x",
			"two\nlines", "trailing\n", " lead\nx", "tab\tin", "cr\r\n", "bell\a", "bom\uFEFF", "'quote", `"dq`, "é", "*star", "&amp", "!tag", "%p", "@at", "`bt"},
		"numbers": []any{1.0, -0.0, 1e21, 1.5e-7, int64(math.MaxInt64), int64(math.MinInt64), int64(0)},
		"tags":    map[string]any{tagTime: "2026-11-01T09:00:00.000000Z"},
		"map":     map[string]any{tagMap: map[string]any{tagFloat: "NaN"}},
		"empty":   map[string]any{"list": []any{}, "map": map[string]any{}, "nil": nil, "bool": false},
	}
	content := spec.encode(data)
	if !bytes.Equal(content, spec.encode(data)) {
		t.Error("not deterministic")
	}
	back, err := spec.decode(content)
	if err != nil {
		t.Fatalf("%v\n%s", err, content)
	}
	if got := canonical(back); !reflect.DeepEqual(got, data) {
		t.Errorf("round trip:\n%s\ngot  %#v\nwant %#v", content, got, data)
	}
}

func TestCanonical(t *testing.T) {
	for in, want := range map[string]any{"1": int64(1), "1.0": 1.0, "1e2": 100.0, "-3": int64(-3)} {
		if got := canonical(jsonNumber(in)); got != want {
			t.Errorf("%s: %#v", in, got)
		}
	}
	if got := canonical(7); got != int64(7) {
		t.Errorf("%#v", got)
	}
}

// A run stopped at its budget in the middle of a collection kept in one file
// writes no record file of it (only partial chunks inGitDB does not read); the
// run that resumes it writes the whole file.
func TestRunResumesACollectionKeptInOneFile(t *testing.T) {
	out := t.TempDir()
	src := yamlSource()
	stopped := runYAML(t, src, Options{Output: out, MaxReads: 3, PageSize: 2, Collections: []string{"tags"}})
	if !stopped.BudgetReached {
		t.Fatalf("%+v", stopped)
	}
	if _, err := os.Stat(filepath.Join(out, "tags/tags.yaml")); !os.IsNotExist(err) {
		t.Error("a partly read collection has a record file")
	}
	if entries, _ := filepath.Glob(filepath.Join(partialDir(out), "tags/tags.yaml.parts", "*.yaml")); len(entries) != 1 {
		t.Errorf("partial chunks: %v", entries)
	}
	var report *Report
	for runs := 0; report == nil || report.BudgetReached; runs++ {
		report = runYAML(t, src, Options{Output: out, MaxReads: 3, PageSize: 2, Collections: []string{"tags"}})
		if runs > 30 {
			t.Fatal("no progress")
		}
	}
	tags := readFile(t, filepath.Join(out, "tags/tags.yaml"))
	full := readFile(t, filepath.Join(func() string { o := t.TempDir(); runYAML(t, yamlSource(), Options{Output: o}); return o }(), "tags/tags.yaml"))
	if tags != full || report.Records != 4 || !report.Resumed {
		t.Errorf("resumed:\n%s\nfull:\n%s\n%d records", tags, full, report.Records)
	}
}

// A record no longer in the source is reported, and --prune removes its key
// (and the file when no key is left).
func TestRunPrunesKeysOfACollectionKeptInOneFile(t *testing.T) {
	out := t.TempDir()
	src := yamlSource()
	runYAML(t, src, Options{Output: out})
	delete(src.docs, "groups/g1/members/m2")
	delete(src.docs, "groups/g2/members/m3")
	report := runYAML(t, src, Options{Output: out})
	if !slices.Equal(report.Stale, []string{"groups/g1/members/members.yaml#m2", "groups/g2/members/members.yaml#m3"}) {
		t.Errorf("%v", report.Stale)
	}
	pruned := runYAML(t, src, Options{Output: out, Prune: true})
	if len(pruned.Pruned) != 2 || readFile(t, filepath.Join(out, "groups/g1/members/members.yaml")) != "m1:\n  name: One\n" {
		t.Errorf("%+v", pruned)
	}
	if _, err := os.Stat(filepath.Join(out, "groups/g2/members/members.yaml")); !os.IsNotExist(err) {
		t.Error("an empty record file was kept")
	}
	if len(pruned.Validation) != 0 {
		t.Error(pruned.Validation)
	}
}

func TestRecordsHelpersFail(t *testing.T) {
	defs := yamlDefinitions(t)
	tags := defs.Spec("tags")
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	for content, want := range map[string]string{"a: [": "read ", "a: 1\n": "is not a map"} {
		if err := os.WriteFile(bad, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRecords(bad, tags); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", content, err)
		}
		if err := pruneRecords(dir, "bad.yaml", tags, nil); err == nil {
			t.Error("pruned a broken file")
		}
	}
	if _, err := readRecords(dir, tags); err == nil {
		t.Error("read a folder")
	}
	if spec := defs.specOfFile("groups/g1/other/members.yaml"); spec != nil {
		t.Error(spec.ID)
	}
	for _, file := range []string{"elsewhere/tags.yaml", "groups/~zz/members/members.yaml", "groups/g1", "tags/x/tags.yaml"} {
		for _, spec := range defs.Specs {
			if _, ok := spec.documentOfFile(file, "x"); ok && spec.multi {
				t.Errorf("%s: %s", file, spec.ID)
			}
		}
	}
	// A broken chunk, a broken record file and a failing write stop the run.
	out := t.TempDir()
	runYAML(t, yamlSource(), Options{Output: out, MaxReads: 3, PageSize: 2, Collections: []string{"tags"}})
	chunks, _ := filepath.Glob(filepath.Join(partialDir(out), "tags/tags.yaml.parts", "*.yaml"))
	if err := os.WriteFile(chunks[0], []byte("a: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), yamlSource(), defs, yamlPolicy(), Options{Output: out, MaxReads: 3, PageSize: 2, Collections: []string{"tags"}}); err == nil {
		t.Error("resumed from a broken chunk")
	}
	out = t.TempDir()
	runYAML(t, yamlSource(), Options{Output: out})
	if err := os.WriteFile(filepath.Join(out, "tags/tags.yaml"), []byte("a: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), yamlSource(), defs, yamlPolicy(), Options{Output: out}); err == nil {
		t.Error("merged into a broken record file")
	}
	withSeam(t, &createTemp, func(string, string) (*os.File, error) { return nil, errBoom })
	if _, err := Run(context.Background(), yamlSource(), defs, yamlPolicy(), Options{Output: t.TempDir(), MaxReads: 3, PageSize: 2, Collections: []string{"tags"}}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
}

// A partial chunk of a collection no longer defined is ignored.
func TestLoadPartialsSkipsUnknownFiles(t *testing.T) {
	out := t.TempDir()
	chunk := filepath.Join(partialDir(out), "gone/gone.yaml.parts", "00000001.yaml")
	if err := os.MkdirAll(filepath.Dir(chunk), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chunk, []byte("a: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &run{defs: yamlDefinitions(t), opts: Options{Output: out}, multi: map[string]*multiFile{}}
	if err := r.loadPartials(); err != nil || len(r.multi) != 0 {
		t.Errorf("%v %v", err, r.multi)
	}
	if err := os.Chmod(partialDir(out), 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(partialDir(out), 0o700) }()
	if err := r.loadPartials(); err == nil && os.Geteuid() != 0 {
		t.Error("read an unreadable folder")
	}
}

func jsonNumber(s string) any { return json.Number(s) }

func TestDocumentOfFile(t *testing.T) {
	parent := &ingitdb.CollectionDef{RecordFile: &ingitdb.RecordFileDef{Name: "{key}.yaml", RecordType: ingitdb.SingleRecord}}
	child := &ingitdb.CollectionDef{RecordFile: &ingitdb.RecordFileDef{Name: "all.yaml", RecordType: ingitdb.MapOfRecords}}
	spec := &Spec{Segments: []string{"p", "c"}, Def: child, chain: []*ingitdb.CollectionDef{parent, child}, rootRel: "r", multi: true}
	if doc, ok := spec.documentOfFile("r/$records/p~2F1/c/all.yaml", "x"); !ok || doc != "p/p/1/c/x" {
		t.Errorf("%q %v", doc, ok)
	}
	for _, file := range []string{"x/$records/p1/c/all.yaml", "r", "r/p1/c/all.yaml", "r/$records", "r/$records/~zz/c/all.yaml", "r/$records/p1/d/all.yaml", "r/$records/p1/c/other.yaml"} {
		if doc, ok := spec.documentOfFile(file, "x"); ok {
			t.Errorf("%s: %s", file, doc)
		}
	}
}

// Writes that fail stop the run: a partial chunk, a record file kept in one
// file, the records left after a prune.
func TestRunStopsWhenACollectionKeptInOneFileCannotBeWritten(t *testing.T) {
	failIn := func(match func(dir string) bool) func(string, string) (*os.File, error) {
		return func(dir, pattern string) (*os.File, error) {
			if match(dir) {
				return nil, errBoom
			}
			return os.CreateTemp(dir, pattern)
		}
	}
	for name, match := range map[string]func(string) bool{
		"chunk":       func(dir string) bool { return strings.HasSuffix(dir, ".parts") },
		"record file": func(dir string) bool { return filepath.Base(dir) == "tags" },
	} {
		withSeam(t, &createTemp, failIn(match))
		_, err := Run(context.Background(), yamlSource(), yamlDefinitions(t), yamlPolicy(), Options{Output: t.TempDir(), PageSize: 2, Collections: []string{"tags"}})
		if !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
	}
	withSeam(t, &createTemp, os.CreateTemp)
	out := t.TempDir()
	src := yamlSource()
	runYAML(t, src, Options{Output: out})
	delete(src.docs, "groups/g2/members/m3")
	withSeam(t, &removeFile, func(string) error { return errBoom })
	if _, err := Run(context.Background(), src, yamlDefinitions(t), yamlPolicy(), Options{Output: out, Prune: true}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
}

// A record file the mirror cannot read, below a parent it does not write, is
// left alone: it lists no stale record, and a prune does not touch it.
func TestRunLeavesAnUnreadableRecordFile(t *testing.T) {
	out := t.TempDir()
	runYAML(t, yamlSource(), Options{Output: out})
	broken := filepath.Join(out, "groups/g9/members/members.yaml")
	if err := os.MkdirAll(filepath.Dir(broken), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte("a: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	report := runYAML(t, yamlSource(), Options{Output: out, Prune: true})
	if len(report.Pruned) != 0 || readFile(t, broken) != "a: [" {
		t.Errorf("%+v", report)
	}
}

// Definitions that cannot be copied stop the run.
func TestRunStopsWhenDefinitionsCannotBeCopied(t *testing.T) {
	defs := yamlDefinitions(t)
	if err := os.RemoveAll(defs.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), yamlSource(), defs, yamlPolicy(), Options{Output: t.TempDir()}); err == nil || errors.Is(err, ErrUsage) {
		t.Error(err)
	}
}
