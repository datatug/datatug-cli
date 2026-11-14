package dbmirror

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// foldFS is a case-insensitive, case-preserving filesystem (as macOS APFS and
// Windows NTFS are by default) over the folder root of the real one, whatever
// that one is: every name below root is looked up whatever its case, a new
// name keeps the case it is created with, and a rename onto a name that
// differs only in case from an existing one replaces that file and keeps its
// old name, as APFS does. A rename of a name to itself in another case renames.
type foldFS struct {
	root string
	fail map[string]func(path string) error // method -> error to return (nil: none)
}

func newFoldFS(t *testing.T) (*foldFS, string) {
	t.Helper()
	root := t.TempDir()
	fake := &foldFS{root: root, fail: map[string]func(string) error{}}
	withSeam(t, &outFS, fileSystem(fake))
	return fake, root
}

// resolve is path with each existing name below root in the case it has on disk.
func (f *foldFS) resolve(path string) string {
	rel, err := filepath.Rel(f.root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return path
	}
	at := f.root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		at = filepath.Join(at, f.existing(at, name))
	}
	return at
}

// existing is the name in dir equal to name whatever its case, or name.
func (f *foldFS) existing(dir, name string) string {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return entry.Name()
		}
	}
	return name
}

func (f *foldFS) failed(method, path string) error {
	if hook := f.fail[method]; hook != nil {
		return hook(path)
	}
	return nil
}

func (f *foldFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(f.resolve(name)) }
func (f *foldFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := f.failed("ReadDir", name); err != nil {
		return nil, err
	}
	return os.ReadDir(f.resolve(name))
}
func (f *foldFS) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(f.resolve(name)) }
func (f *foldFS) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(f.resolve(path), perm)
}
func (f *foldFS) CreateTemp(dir, pattern string) (*os.File, error) {
	return os.CreateTemp(f.resolve(dir), pattern)
}
func (f *foldFS) Chmod(name string, mode fs.FileMode) error { return os.Chmod(f.resolve(name), mode) }
func (f *foldFS) Rename(from, to string) error {
	if err := f.failed("Rename", to); err != nil {
		return err
	}
	source := f.resolve(from)
	dir := f.resolve(filepath.Dir(to))
	target := filepath.Join(dir, f.existing(dir, filepath.Base(to)))
	if target == source {
		target = filepath.Join(dir, filepath.Base(to)) // a rename to another case of its own name
	}
	return os.Rename(source, target)
}
func (f *foldFS) Remove(name string) error {
	if err := f.failed("Remove", name); err != nil {
		return err
	}
	return os.Remove(f.resolve(name))
}
func (f *foldFS) RemoveAll(path string) error {
	if err := f.failed("RemoveAll", path); err != nil {
		return err
	}
	return os.RemoveAll(f.resolve(path))
}

// strictFS is a case-sensitive filesystem as far as Lstat tells (the one check
// alignID makes of a folder), on any host.
type strictFS struct{ osFS }

func (strictFS) Lstat(name string) (fs.FileInfo, error) {
	entries, err := os.ReadDir(filepath.Dir(name))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(name) {
			return os.Lstat(name)
		}
	}
	return nil, fs.ErrNotExist
}

func caseDefinitions(t *testing.T) *Definitions {
	t.Helper()
	defs, err := LoadDefinitions(filepath.Join("testdata", "casefold"))
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func casePolicy(t *testing.T) *Policy {
	t.Helper()
	policy, err := ReadPolicy(filepath.Join("testdata", "casefold", "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func caseRun(t *testing.T, src Source, opts Options) *Report {
	t.Helper()
	report, err := Run(context.Background(), src, caseDefinitions(t), casePolicy(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// names lists the entries of a folder as the filesystem keeps them.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var list []string
	for _, entry := range entries {
		list = append(list, entry.Name())
	}
	return list
}

// bobSource is the report's case 3: a record of each form, and a parent with
// a sub-collection, all with the id id.
func bobSource(id string) *memSource {
	src := newMemSource()
	src.docs["perrec/"+id] = map[string]any{"name": id}
	src.docs["single/"+id] = map[string]any{"name": id}
	src.docs["parents/"+id] = map[string]any{"name": id}
	src.docs["parents/"+id+"/kids/k1"] = map[string]any{"name": "kid of " + id}
	return src
}

// The report's cases 3, 3b and 3c: an id that changes only in case (Bob
// becomes bob) on a filesystem that folds case. The file and the folder of
// its sub-collection take the new case, and --prune deletes nothing live and
// settles at once.
func TestRunRenamesACaseOnlyIDChangeOnAFoldingFilesystem(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out})
	if got := names(t, filepath.Join(out, "parents", "$records")); !slices.Equal(got, []string{"Bob", "Bob.yaml"}) {
		t.Fatalf("%v", got)
	}
	src := bobSource("bob")
	renamed := caseRun(t, src, Options{Output: out})
	if !slices.Equal(renamed.Renamed, []string{"parents/$records/Bob.yaml -> parents/$records/bob.yaml", "parents/$records/Bob -> parents/$records/bob", "perrec/$records/Bob.yaml -> perrec/$records/bob.yaml"}) {
		t.Errorf("%v", renamed.Renamed)
	}
	if renamed.Written == 0 || len(renamed.Rejected) != 0 {
		t.Errorf("%+v", renamed)
	}
	// 3c: the record is read back under its new key, with its new data.
	if got := names(t, filepath.Join(out, "perrec", "$records")); !slices.Equal(got, []string{"bob.yaml"}) {
		t.Errorf("%v", got)
	}
	if got := readFile(t, filepath.Join(out, "perrec", "$records", "bob.yaml")); got != "\"name\": \"bob\"\n" {
		t.Errorf("%q", got)
	}
	if got := names(t, filepath.Join(out, "parents", "$records")); !slices.Equal(got, []string{"bob", "bob.yaml"}) {
		t.Errorf("%v", got)
	}
	// 3 and 3b: --prune deletes only the one-file record that is really gone,
	// and the next --prune finds nothing to do.
	for round := range 2 {
		pruned := caseRun(t, src, Options{Output: out, Prune: true})
		want := map[int][]string{0: {"single/records.yaml#Bob"}}[round]
		if !slices.Equal(pruned.Pruned, want) || len(pruned.Stale) != 0 || pruned.Written != 0 || len(pruned.Renamed) != 0 || pruned.Records != 4 {
			t.Errorf("round %d: %+v", round, pruned)
		}
		for _, file := range []string{"perrec/$records/bob.yaml", "parents/$records/bob.yaml", "parents/$records/bob/kids/kids.yaml"} {
			if _, err := os.Stat(filepath.Join(out, file)); err != nil {
				t.Errorf("round %d: %v", round, err)
			}
		}
	}
	if got := readFile(t, filepath.Join(out, "single", "records.yaml")); got != "\"bob\":\n  \"name\": \"bob\"\n" {
		t.Errorf("%q", got)
	}
	if got := readFile(t, filepath.Join(out, "parents", "$records", "bob", "kids", "kids.yaml")); got != "\"k1\":\n  \"name\": \"kid of bob\"\n" {
		t.Errorf("%q", got)
	}
}

// With --prune in the run that sees the rename itself, nothing live is deleted either.
func TestRunPrunesARenameInOneRun(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out})
	report := caseRun(t, bobSource("bob"), Options{Output: out, Prune: true})
	if !slices.Equal(report.Pruned, []string{"single/records.yaml#Bob"}) || len(report.Renamed) != 3 {
		t.Errorf("%+v", report)
	}
	for _, file := range []string{"perrec/$records/bob.yaml", "parents/$records/bob.yaml", "parents/$records/bob/kids/kids.yaml"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Error(err)
		}
	}
}

// Two ids of one run that differ only in case are still a collision, which
// the second loses: the rename never moves the first one's file.
func TestRunKeepsRejectingTwoIDsThatDifferOnlyInCase(t *testing.T) {
	_, out := newFoldFS(t)
	src := newMemSource()
	src.docs["perrec/Alice"] = map[string]any{"name": "Alice"}
	src.docs["perrec/alice"] = map[string]any{"name": "alice"}
	report := caseRun(t, src, Options{Output: out})
	if len(report.Rejected) != 1 || report.Rejected[0].Path != "perrec/alice" || len(report.Renamed) != 0 {
		t.Errorf("%+v", report)
	}
	if got := names(t, filepath.Join(out, "perrec", "$records")); !slices.Equal(got, []string{"Alice.yaml"}) {
		t.Errorf("%v", got)
	}
}

// On a case-sensitive filesystem Bob.yaml and bob.yaml are two files: nothing
// is renamed, and --prune deletes the old one, which is really gone.
func TestRunPrunesTheOldCaseOnACaseSensitiveFilesystem(t *testing.T) {
	out := t.TempDir()
	if folds(t, out) {
		t.Skip("the temporary folder folds case (macOS, Windows): see TestAlignIDLeavesACaseSensitiveFolder")
	}
	caseRun(t, bobSource("Bob"), Options{Output: out})
	report := caseRun(t, bobSource("bob"), Options{Output: out, Prune: true})
	if len(report.Renamed) != 0 || !slices.Contains(report.Pruned, "perrec/$records/Bob.yaml") {
		t.Errorf("%+v", report)
	}
	if got := names(t, filepath.Join(out, "perrec", "$records")); !slices.Equal(got, []string{"bob.yaml"}) {
		t.Errorf("%v", got)
	}
}

// folds reports whether the real folder dir folds case.
func folds(t *testing.T, dir string) bool {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(filepath.Join(dir, "probe")) }()
	_, err := os.Stat(filepath.Join(dir, "PROBE"))
	return err == nil
}

func TestAlignIDLeavesACaseSensitiveFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Bob.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	withSeam(t, &outFS, fileSystem(strictFS{}))
	r := &run{opts: Options{Output: dir}, report: &Report{}, names: map[string]map[string]string{}}
	if renamed, err := r.alignID(dir, "bob.yaml", []string{"perrec", "bob"}, nil, nil); renamed || err != nil {
		t.Errorf("%v %v", renamed, err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"Bob.yaml"}) {
		t.Errorf("%v", got)
	}
}

// A prune never deletes a file that is the one this run wrote under a name
// that differs only in case; a file that is another one is not protected.
func TestIsOwnedComparesTheFilesNotTheNames(t *testing.T) {
	_, out := newFoldFS(t)
	if err := os.WriteFile(filepath.Join(out, "Bob.yaml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := &run{opts: Options{Output: out}, names: map[string]map[string]string{}, owned: map[string][]string{}}
	r.noteWritten("bob.yaml")
	if !r.isOwned("Bob.yaml") || r.isOwned("bob.yaml") || r.isOwned("other.yaml") {
		t.Error("a file this run wrote is not recognised under another case")
	}
	withSeam(t, &outFS, fileSystem(strictFS{}))
	if r.isOwned("Bob.yaml") {
		t.Error("two files on a case-sensitive filesystem are taken for one")
	}
}

// The report's case 4b: a run that stopped at a long id is resumed with
// --continue-on-long-ids. The records it had written are not collisions with
// themselves, and are counted once.
func TestRunResumesALongIDStopWithoutSelfCollisions(t *testing.T) {
	_, out := newFoldFS(t)
	src := newMemSource()
	for _, id := range []string{"a1", "a2", "a3", "a4", "a5", strings.Repeat("z", 300)} {
		src.docs["perrec/"+id] = map[string]any{"name": "n"}
	}
	src.docs["single/s1"] = map[string]any{"name": "n"}
	if _, err := Run(context.Background(), src, caseDefinitions(t), casePolicy(t), Options{Output: out}); !errors.Is(err, ErrIDTooLong) {
		t.Fatal(err)
	}
	report := caseRun(t, src, Options{Output: out, ContinueOnLongIDs: true})
	if !report.Resumed || len(report.Rejected) != 0 || result(t, report, "perrec").Records != 5 || result(t, report, "perrec").IDTooLong != 1 || report.Records != 6 {
		t.Errorf("%+v %+v", report, report.Rejected)
	}
}

// A record kept in one file that was read in the page a run stopped in is
// read again by the resume, and counted once.
func TestRunResumesALongParentIDStopCountingOnce(t *testing.T) {
	out := t.TempDir()
	src := newMemSource()
	src.docs["parents/p1/kids/k1"] = map[string]any{"name": "n"}
	src.docs["parents/"+strings.Repeat("z", 300)+"/kids/k2"] = map[string]any{"name": "n"}
	if _, err := Run(context.Background(), src, caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"parents/kids"}}); !errors.Is(err, ErrIDTooLong) {
		t.Fatal(err)
	}
	report := caseRun(t, src, Options{Output: out, Collections: []string{"parents/kids"}, ContinueOnLongIDs: true})
	if kids := result(t, report, "parents/kids"); !report.Resumed || kids.Records != 1 || kids.IDTooLong != 1 {
		t.Errorf("%+v", kids)
	}
}

// The rule: a run never deletes or writes over a live record because the
// filesystem folds case. When the old id is still in the source (as a
// document, or as documents of its sub-collections), or the old name is not
// one the mirror writes, the run stops, names both ids, and writes neither.
func TestRunStopsWhenItCannotKeepTwoIDsApart(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("bob"), Options{Output: out})
	both := bobSource("bob")
	both.docs["perrec/Bob"] = map[string]any{"name": "Bob"}
	report, err := Run(context.Background(), both, caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"perrec"}})
	if !errors.Is(err, ErrCaseFold) || !strings.Contains(err.Error(), `the ids "Bob" (document perrec/Bob) and "bob" (document perrec/bob, in the output as perrec/$records/bob.yaml) differ only in case, and both are in the source`) || !strings.Contains(err.Error(), `diskutil apfs addVolume <container disk> "Case-sensitive APFS" <name>`) {
		t.Fatal(err)
	}
	if report == nil || report.Complete || !strings.Contains(strings.Join(report.Incomplete, ";"), "stopped: collection perrec: the ids") {
		t.Errorf("%+v", report)
	}
	if got := readFile(t, filepath.Join(out, "perrec", "$records", "bob.yaml")); got != "\"name\": \"bob\"\n" || !slices.Equal(names(t, filepath.Join(out, "perrec", "$records")), []string{"bob.yaml"}) {
		t.Errorf("bob's record was written over: %q", got)
	}
	// The parent document is gone, but its sub-collection is not: its folder is kept.
	kids := bobSource("Bob")
	delete(kids.docs, "parents/bob")
	kids.docs["parents/bob/kids/k1"] = map[string]any{"name": "kid of bob"}
	_, err = Run(context.Background(), kids, caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"parents"}, Restart: true})
	if !errors.Is(err, ErrCaseFold) || !strings.Contains(err.Error(), `the ids "Bob" (document parents/Bob) and "bob" (document parents/bob`) {
		t.Fatal(err)
	}
	if got := names(t, filepath.Join(out, "parents", "$records")); !slices.Equal(got, []string{"bob", "bob.yaml"}) {
		t.Errorf("%v", got)
	}
	// A name the mirror does not write (lower-case hex) is not proved anything.
	if err := os.WriteFile(filepath.Join(out, "perrec", "$records", "star~2a.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	star := newMemSource()
	star.docs["perrec/star*"] = map[string]any{"name": "s"}
	if _, err := Run(context.Background(), star, caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"perrec"}, Restart: true}); !errors.Is(err, ErrCaseFold) || !strings.Contains(err.Error(), "the document perrec/star* would be written into perrec/$records/star~2a.yaml, whose name differs only in case and is not one the mirror writes") {
		t.Fatal(err)
	}
}

// A name whose id is the same, in another case only around it, is renamed without a read.
func TestRunRenamesTheCaseAroundTheSameID(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("bob"), Options{Output: out})
	if err := os.Rename(filepath.Join(out, "perrec", "$records", "bob.yaml"), filepath.Join(out, "perrec", "$records", "bob.YAML")); err != nil {
		t.Fatal(err)
	}
	src := bobSource("bob")
	report := caseRun(t, src, Options{Output: out, Collections: []string{"perrec"}})
	if !slices.Equal(report.Renamed, []string{"perrec/$records/bob.YAML -> perrec/$records/bob.yaml"}) || len(src.gets) != 0 {
		t.Errorf("%+v %v", report, src.gets)
	}
}

// A source that cannot be read for the proof fails the run.
func TestRunReportsAFailedProof(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out})
	get := bobSource("bob")
	get.fail["Get"] = errBoom // only the proof reads one document
	for method, src := range map[string]Source{"Get": get, "Page": &failingPage{memSource: bobSource("bob")}} {
		if _, err := Run(context.Background(), src, caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"parents"}, Restart: true}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", method, err)
		}
	}
}

// failingPage fails a group read under a document: the proof's read.
type failingPage struct{ *memSource }

func (f *failingPage) Page(ctx context.Context, q Query) ([]Doc, error) {
	if q.Group && q.Under != "" {
		return nil, errBoom
	}
	return f.memSource.Page(ctx, q)
}

// The renames' failures fail the run, and a failed second step puts the file back.
func TestAlignCaseReportsFailures(t *testing.T) {
	fake, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out})
	cases := map[string]func(string) error{
		"ReadDir": func(path string) error {
			if strings.HasSuffix(path, "$records") {
				return errBoom
			}
			return nil
		},
		"first rename": func(path string) error {
			if strings.Contains(path, ".tmp-case-") {
				return errBoom
			}
			return nil
		},
		"second rename": func(path string) error {
			if strings.HasSuffix(path, "bob.yaml") || strings.HasSuffix(path, "bob") {
				return errBoom
			}
			return nil
		},
	}
	for name, hook := range cases {
		method := map[bool]string{true: "ReadDir", false: "Rename"}[name == "ReadDir"]
		fake.fail = map[string]func(string) error{method: hook}
		if _, err := Run(context.Background(), bobSource("bob"), caseDefinitions(t), casePolicy(t), Options{Output: out}); !errors.Is(err, errBoom) {
			t.Errorf("%s: %v", name, err)
		}
		fake.fail = map[string]func(string) error{}
		if got := names(t, filepath.Join(out, "parents", "$records")); !slices.Equal(got, []string{"Bob", "Bob.yaml"}) {
			t.Errorf("%s: %v", name, got)
		}
	}
	// A parent's sub-collection folder that cannot be renamed fails the run too.
	fake.fail["Rename"] = func(path string) error {
		if strings.HasSuffix(path, string(filepath.Separator)+"bob") {
			return errBoom
		}
		return nil
	}
	if _, err := Run(context.Background(), bobSource("bob"), caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"parents"}}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
	fake.fail = map[string]func(string) error{}
	// A file kept in one file whose folder cannot be renamed fails the run.
	fresh := t.TempDir()
	fake.root = fresh
	caseRun(t, bobSource("Bob"), Options{Output: fresh})
	fake.fail["Rename"] = func(path string) error {
		if strings.HasSuffix(path, string(filepath.Separator)+"bob") {
			return errBoom
		}
		return nil
	}
	if _, err := Run(context.Background(), bobSource("bob"), caseDefinitions(t), casePolicy(t), Options{Output: fresh, Collections: []string{"parents/kids"}}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
}

func TestOSFS(t *testing.T) {
	dir := t.TempDir()
	var fsys fileSystem = osFS{}
	path := filepath.Join(dir, "a", "b")
	if err := fsys.MkdirAll(path, dirMode); err != nil {
		t.Fatal(err)
	}
	temp, err := fsys.CreateTemp(path, "x-*")
	if err != nil {
		t.Fatal(err)
	}
	_ = temp.Close()
	file := filepath.Join(path, "f")
	if err := firstError(fsys.Chmod(temp.Name(), fileMode), fsys.Rename(temp.Name(), file)); err != nil {
		t.Fatal(err)
	}
	if content, err := fsys.ReadFile(file); err != nil || len(content) != 0 {
		t.Error(content, err)
	}
	if entries, err := fsys.ReadDir(path); err != nil || len(entries) != 1 {
		t.Error(entries, err)
	}
	if _, err := fsys.Lstat(file); err != nil {
		t.Error(err)
	}
	if err := firstError(fsys.Remove(file), fsys.RemoveAll(filepath.Join(dir, "a"))); err != nil {
		t.Error(err)
	}
}

// The review's major: an output an earlier version left in case 3c's state
// (Bob.yaml holds bob's data), and bob is rejected in this run. --prune must
// keep the file: it is the file of a document this run read.
func TestRunPruneKeepsTheFileOfARejectedRecord(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"perrec"}})
	file := filepath.Join(out, "perrec", "$records", "Bob.yaml")
	if err := os.WriteFile(file, []byte("\"name\": \"bob\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := newMemSource()
	src.docs["perrec/bob"] = map[string]any{"name": "bob", "undeclared": "x"}
	report := caseRun(t, src, Options{Output: out, Collections: []string{"perrec"}, Prune: true})
	if len(report.Rejected) != 1 || len(report.Pruned) != 0 || len(report.Stale) != 0 || !slices.Equal(report.Kept, []string{"perrec/$records/Bob.yaml"}) {
		t.Errorf("rejected %v, pruned %v, stale %v, kept %v", report.Rejected, report.Pruned, report.Stale, report.Kept)
	}
	if got := readFile(t, file); got != "\"name\": \"bob\"\n" {
		t.Errorf("%q", got)
	}
}

// The same for a record kept in one file below a parent folder in the old case.
func TestRunPruneKeepsTheFileOfARejectedRecordInOneFile(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"parents/kids"}})
	src := newMemSource()
	src.docs["parents/bob/kids/k1"] = map[string]any{"name": "kid of Bob", "undeclared": "x"}
	report := caseRun(t, src, Options{Output: out, Collections: []string{"parents/kids"}, Prune: true})
	if len(report.Rejected) != 1 || len(report.Pruned) != 0 || !slices.Equal(report.Kept, []string{"parents/$records/Bob/kids/kids.yaml"}) {
		t.Errorf("rejected %v, pruned %v, kept %v", report.Rejected, report.Pruned, report.Kept)
	}
	if got := readFile(t, filepath.Join(out, "parents", "$records", "Bob", "kids", "kids.yaml")); got != "\"k1\":\n  \"name\": \"kid of Bob\"\n" {
		t.Errorf("%q", got)
	}
}

// The proof that an old id is gone spends the read budget: when it would pass
// --max-reads, the run stops as at its budget, renames nothing, and the
// resume renames.
func TestRunProofSpendsTheReadBudget(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"perrec"}})
	src := bobSource("bob")
	stopped := caseRun(t, src, Options{Output: out, Collections: []string{"perrec"}, MaxReads: 2})
	if !stopped.BudgetReached || stopped.Complete || stopped.Reads != 2 || len(stopped.Renamed) != 0 || len(src.gets) != 0 {
		t.Errorf("%+v %v", stopped, src.gets)
	}
	if got := names(t, filepath.Join(out, "perrec", "$records")); !slices.Equal(got, []string{"Bob.yaml"}) {
		t.Errorf("%v", got)
	}
	resumed := caseRun(t, src, Options{Output: out, Collections: []string{"perrec"}})
	if !resumed.Resumed || !slices.Equal(resumed.Renamed, []string{"perrec/$records/Bob.yaml -> perrec/$records/bob.yaml"}) || result(t, resumed, "perrec").Records != 1 {
		t.Errorf("%+v", resumed)
	}
}

// What an interrupted run left in a folder: a rename's temporary name is put
// back and the rename starts again; one that cannot be put back is left and
// reported; a temporary file of a write is removed.
func TestRunRecoversWhatAnInterruptedRunLeft(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"perrec"}})
	records := filepath.Join(out, "perrec", "$records")
	// Killed between the two steps of the rename of Bob.yaml.
	if err := os.Rename(filepath.Join(records, "Bob.yaml"), filepath.Join(records, ".tmp-case-0011223344556677-Bob.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, ".tmp-123"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	report := caseRun(t, bobSource("bob"), Options{Output: out, Collections: []string{"perrec"}, Log: &log})
	want := []string{"removed perrec/$records/.tmp-123", "perrec/$records/.tmp-case-0011223344556677-Bob.yaml -> perrec/$records/Bob.yaml"}
	if !slices.Equal(report.Recovered, want) || len(report.Renamed) != 1 || !report.Complete || !strings.Contains(log.String(), "recovered: removed perrec/$records/.tmp-123 (left by an interrupted run)") {
		t.Errorf("%+v", report)
	}
	if got := names(t, records); !slices.Equal(got, []string{"bob.yaml"}) {
		t.Errorf("%v", got)
	}
	// A leftover whose old name is taken, or unknown, is left as it is and reported.
	for _, leftover := range []string{".tmp-case-0011223344556677-BOB.yaml", ".tmp-case-0011223344556677"} {
		if err := os.WriteFile(filepath.Join(records, leftover), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		report := caseRun(t, bobSource("bob"), Options{Output: out, Collections: []string{"perrec"}})
		if report.Complete || !strings.Contains(strings.Join(report.Incomplete, ";"), "perrec/$records/"+leftover+" was left by an interrupted rename and cannot be put back") {
			t.Errorf("%s: %v", leftover, report.Incomplete)
		}
		if _, err := os.Stat(filepath.Join(records, leftover)); err != nil {
			t.Error(err)
		}
		_ = os.Remove(filepath.Join(records, leftover))
	}
}

func TestRunReportsAFailedRecovery(t *testing.T) {
	fake, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"perrec"}})
	records := filepath.Join(out, "perrec", "$records")
	if err := os.WriteFile(filepath.Join(records, ".tmp-123"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fake.fail["Remove"] = func(path string) error {
		if strings.HasSuffix(path, ".tmp-123") {
			return errBoom
		}
		return nil
	}
	if _, err := Run(context.Background(), bobSource("Bob"), caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"perrec"}}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
	fake.fail = map[string]func(string) error{}
	_ = os.Remove(filepath.Join(records, ".tmp-123"))
	if err := os.Rename(filepath.Join(records, "Bob.yaml"), filepath.Join(records, ".tmp-case-0011223344556677-Bob.yaml")); err != nil {
		t.Fatal(err)
	}
	fake.fail["Rename"] = func(path string) error {
		if strings.HasSuffix(path, string(filepath.Separator)+"Bob.yaml") {
			return errBoom
		}
		return nil
	}
	if _, err := Run(context.Background(), bobSource("Bob"), caseDefinitions(t), casePolicy(t), Options{Output: out, Collections: []string{"perrec"}}); !errors.Is(err, errBoom) {
		t.Error(err)
	}
}

// Only the temporary file of an interrupted write (a regular file named
// ".tmp-" and digits) is removed; any other ".tmp-" entry may be the user's
// own content, so it is left untouched and reported.
func TestRunLeavesUserContentThatLooksLikeALeftover(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"perrec"}})
	records := filepath.Join(out, "perrec", "$records")
	mine := filepath.Join(records, ".tmp-notes", "mine.txt")
	if err := os.MkdirAll(filepath.Dir(mine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(records, ".tmp-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, ".tmp-words"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(records, ".tmp-123456"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"perrec"}})
	if got := names(t, records); !slices.Equal(got, []string{".tmp-123", ".tmp-notes", ".tmp-words", "Bob.yaml"}) {
		t.Errorf("%v", got)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error(err)
	}
	if !slices.Equal(report.Recovered, []string{"removed perrec/$records/.tmp-123456"}) || report.Complete {
		t.Errorf("%+v", report)
	}
	for _, name := range []string{".tmp-123", ".tmp-notes", ".tmp-words"} {
		if !strings.Contains(strings.Join(report.Incomplete, ";"), "perrec/$records/"+name+" ") {
			t.Errorf("%s not reported: %v", name, report.Incomplete)
		}
	}
}

// Two spellings of one file in one run (astra-r2 blocker 1): the legacy
// output keeps bob's kid k1 in Bob/kids/kids.yaml; Bob/k2 is read first and
// writes that file under its exact name, then bob/k1 is read and rejected (its
// parent folder differs only in case). The prune must keep k1: its file is the
// one of bob/k1, under another spelling.
func TestRunPruneRemembersEverySpellingOfAFile(t *testing.T) {
	_, out := newFoldFS(t)
	caseRun(t, bobSource("Bob"), Options{Output: out, Collections: []string{"parents/kids"}})
	src := newMemSource()
	src.docs["parents/Bob/kids/k2"] = map[string]any{"name": "k2 of Bob"}
	src.docs["parents/bob/kids/k1"] = map[string]any{"name": "kid of Bob"}
	report := caseRun(t, src, Options{Output: out, Collections: []string{"parents/kids"}, Prune: true})
	if len(report.Rejected) != 1 || len(report.Pruned) != 0 || !slices.Equal(report.Kept, []string{"parents/$records/Bob/kids/kids.yaml"}) {
		t.Errorf("rejected %v, pruned %v, kept %v", report.Rejected, report.Pruned, report.Kept)
	}
	if got := readFile(t, filepath.Join(out, "parents", "$records", "Bob", "kids", "kids.yaml")); !strings.Contains(got, "\"k1\":") {
		t.Errorf("k1 of bob was pruned: %q", got)
	}
}

// A resume with the same --max-reads makes progress (astra-r2 major 3): with
// a budget of 10, the root listing and a page of 9 documents would spend it
// all, and the first document (Bob became bob) needs a proof read; every
// resume would read the same page again and stop at the same place. The page
// leaves room for one document's proof, so the runs settle.
func TestRunProofLeavesRoomInTheReadBudget(t *testing.T) {
	_, out := newFoldFS(t)
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"}
	seed := newMemSource()
	seed.docs["perrec/Bob"] = map[string]any{"name": "Bob"}
	for _, id := range ids {
		seed.docs["perrec/"+id] = map[string]any{"name": id}
	}
	caseRun(t, seed, Options{Output: out, Collections: []string{"perrec"}})
	src := newMemSource()
	src.docs["perrec/bob"] = map[string]any{"name": "bob"}
	for _, id := range ids {
		src.docs["perrec/"+id] = map[string]any{"name": id}
	}
	var report *Report
	for range 4 {
		report = caseRun(t, src, Options{Output: out, Collections: []string{"perrec"}, MaxReads: 10})
		if report.Complete {
			break
		}
	}
	if !report.Complete || !slices.Contains(names(t, filepath.Join(out, "perrec", "$records")), "bob.yaml") {
		t.Errorf("no progress with an unchanged --max-reads: %+v", report)
	}
}
