package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbmirror"
)

// mirrorTestSource is a dbmirror.Source over a few documents: queries match
// the documents of the collection id (any depth for a group) under Under.
type mirrorTestSource struct {
	docs   map[string]map[string]any
	failed error
}

func (s *mirrorTestSource) RootCollections(context.Context) ([]string, error) {
	return []string{"teams", "other"}, s.failed
}

func (s *mirrorTestSource) Page(_ context.Context, q dbmirror.Query) ([]dbmirror.Doc, error) {
	var paths []string
	for path := range s.docs {
		parts := strings.Split(path, "/")
		if parts[len(parts)-2] == q.Collection && (q.Group || len(parts) == 2) && strings.HasPrefix(path, q.Under) && path > q.After {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	docs := []dbmirror.Doc{}
	for _, path := range paths {
		if len(docs) < q.Limit {
			docs = append(docs, dbmirror.Doc{Path: path, Data: s.docs[path]})
		}
	}
	return docs, nil
}

func (s *mirrorTestSource) Get(_ context.Context, path string) (*dbmirror.Doc, error) {
	if data, ok := s.docs[path]; ok {
		return &dbmirror.Doc{Path: path, Data: data}, nil
	}
	return nil, nil
}

func (s *mirrorTestSource) Count(_ context.Context, q dbmirror.Query) (int64, error) {
	docs, _ := s.Page(context.Background(), dbmirror.Query{Collection: q.Collection, Group: q.Group, Under: q.Under, Limit: 1000})
	return int64(len(docs)), nil
}

// mirrorProject is a DataTug project in a git repository that ignores /data/,
// holding the example mirror of pkg/dbmirror and a policy without an output.
func mirrorProject(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	if err := os.CopyFS(filepath.Join(project, "mirror"), os.DirFS(filepath.Join("..", "..", "..", "pkg", "dbmirror", "testdata", "mirror"))); err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "pkg", "dbmirror", "testdata", "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	policy = bytes.Replace(policy, []byte("output: out\n"), nil, 1)
	if err := os.MkdirAll(filepath.Join(project, "mirrors"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy = bytes.Replace(policy, []byte("definitions: mirror"), []byte("definitions: ../mirror"), 1)
	for name, content := range map[string][]byte{
		"mirrors/backup.yaml":  policy,
		"datatug-project.json": []byte("{}\n"),
		".gitignore":           []byte("/data/\n"),
	} {
		if err := os.WriteFile(filepath.Join(project, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", project, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return project
}

func mirrorDocs() map[string]map[string]any {
	return map[string]map[string]any{
		"teams/t1":                   {"title": "Team one", "createdAt": "2026-11-01T09:00:00Z"},
		"teams/t1/ext/cal":           {"data": map[string]any{"k": int64(1)}},
		"teams/t1/ext/cal/events/e1": {"title": "Event"},
		"teams/t2":                   {"title": "Team two", "createdAt": "2026-11-01T09:00:00Z"},
	}
}

func useMirrorSource(t *testing.T, source dbmirror.Source, openErr error) *[]dbcopy.BackendRef {
	t.Helper()
	var opened []dbcopy.BackendRef
	saved := openMirrorSource
	t.Cleanup(func() { openMirrorSource = saved })
	openMirrorSource = func(_ context.Context, ref dbcopy.BackendRef) (dbmirror.Source, func() error, error) {
		opened = append(opened, ref)
		if openErr != nil {
			return nil, nil, openErr
		}
		return source, func() error { return nil }, nil
	}
	return &opened
}

func runMirror(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	command := dbMirrorCommand()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(args)
	err := command.Execute()
	var coder ExitCoder
	switch {
	case err == nil:
	case errors.As(err, &coder):
		code = coder.ExitCode()
		errOut.WriteString(err.Error())
	default:
		code = -1
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), code
}

func TestDBMirrorCopiesIntoTheIgnoredDataFolder(t *testing.T) {
	project := mirrorProject(t)
	opened := useMirrorSource(t, &mirrorTestSource{docs: mirrorDocs()}, nil)
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:1")
	t.Setenv("MIRROR_SOURCE", "firestore://demo-example")
	stdout, stderr, code := runMirror(t, "--from", "env:MIRROR_SOURCE", "--policy", filepath.Join(project, "mirrors", "backup.yaml"),
		"--collections", "teams, ,teams/ext/**", "--json", "--progress")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if len(*opened) != 1 || (*opened)[0].ProjectID != "demo-example" {
		t.Errorf("%+v", *opened)
	}
	out := filepath.Join(project, "data", "backup")
	for _, file := range []string{"teams/t1.json", "teams/t1/ext/cal.json", "teams/t1/ext/cal/events/e1.json", ".datatug-mirror/report.json"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Error(err)
		}
	}
	var report dbmirror.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil || report.Records != 4 || report.Source != "env:MIRROR_SOURCE" {
		t.Errorf("%v %+v", err, report)
	}
	for _, want := range []string{"database (default) on the emulator at 127.0.0.1:1 (read-only)", "writing " + out, "documents read"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

func TestDBMirrorDryRunAndOneSpace(t *testing.T) {
	project := mirrorProject(t)
	useMirrorSource(t, &mirrorTestSource{docs: map[string]map[string]any{"spaces/s1": {}}}, nil)
	policy := filepath.Join(project, "mirrors", "backup.yaml")
	_, stderr, code := runMirror(t, "--from", "firestore://demo-example", "--policy", policy, "--dry-run", "--collections", "teams")
	if code != 0 || !strings.Contains(stderr, "nothing was written") || strings.Contains(stderr, "on the emulator") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(project, "data")); !os.IsNotExist(err) {
		t.Error("a dry run wrote")
	}
	// --space selects spaces/<id>: the example mirror defines no spaces, which is an error, not an empty copy.
	_, stderr, code = runMirror(t, "--from", "firestore://demo-example", "--policy", policy, "--space", "s1", "--to", "ingitdb://"+filepath.Join(project, "data", "s1"))
	if code != exitCodeUsage || !strings.Contains(stderr, "no collection defined in the mirror is at or below spaces/s1") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}

func TestDBMirrorRefusals(t *testing.T) {
	project := mirrorProject(t)
	useMirrorSource(t, &mirrorTestSource{docs: mirrorDocs()}, nil)
	policy := filepath.Join(project, "mirrors", "backup.yaml")
	noDefinitions := filepath.Join(project, "mirrors", "bare.yaml")
	if err := os.WriteFile(noDefinitions, []byte("format: datatug-mirror-policy/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	from := []string{"--from", "firestore://demo-example", "--policy", policy}
	for name, test := range map[string]struct {
		args []string
		code int
		want string
	}{
		"space and path":      {append(from, "--space", "a", "--path", "b"), exitCodeUsage, "mutually exclusive"},
		"space with a slash":  {append(from, "--space", "a/b"), exitCodeUsage, "one space ID"},
		"negative budget":     {append(from, "--max-reads", "-1"), exitCodeUsage, "--max-reads"},
		"zero page size":      {append(from, "--page-size", "0"), exitCodeUsage, "--page-size"},
		"bad source":          {[]string{"--from", "nope://x", "--policy", policy}, exitCodeUsage, "--from"},
		"unresolved datatug":  {[]string{"--from", "datatug:///" + filepath.Join(project, "none") + "/db", "--policy", policy}, 0, ""},
		"not firestore":       {[]string{"--from", "ingitdb://" + project, "--policy", policy}, exitCodeUsage, "reads Firestore only"},
		"missing policy":      {[]string{"--from", "firestore://demo-example", "--policy", filepath.Join(project, "none.yaml")}, exitCodeUsage, "--policy"},
		"no definitions":      {[]string{"--from", "firestore://demo-example", "--policy", noDefinitions}, exitCodeUsage, "definitions of the mirror are not known"},
		"bad definitions":     {append(from, "--definitions", filepath.Join(project, "mirrors")), exitCodeUsage, "define no collection"},
		"output in mirror":    {append(from, "--to", filepath.Join(project, "mirror", "out")), exitCodeUsage, "outside the definitions folder"},
		"output holds mirror": {append(from, "--to", project), exitCodeUsage, "outside the definitions folder"},
		"committable output":  {append(from, "--to", filepath.Join(project, "kept")), exitCodeUsage, "not ignored by it"},
		"unknown collection":  {append(from, "--dry-run", "--collections", "nope"), exitCodeUsage, `"nope" is not defined`},
	} {
		t.Run(name, func(t *testing.T) {
			_, stderr, code := runMirror(t, test.args...)
			if test.want == "" {
				if code == 0 {
					t.Errorf("exit 0\n%s", stderr)
				}
				return
			}
			if code != test.code || !strings.Contains(stderr, test.want) {
				t.Errorf("exit %d\n%s", code, stderr)
			}
		})
	}
}

func TestDBMirrorFailures(t *testing.T) {
	project := mirrorProject(t)
	policy := filepath.Join(project, "mirrors", "backup.yaml")
	args := []string{"--from", "firestore://demo-example", "--policy", policy}
	useMirrorSource(t, nil, errors.New("no credentials"))
	if _, stderr, code := runMirror(t, args...); code != exitCodeDatabase || !strings.Contains(stderr, "no credentials") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
	useMirrorSource(t, &mirrorTestSource{failed: errors.New("unavailable")}, nil)
	if _, stderr, code := runMirror(t, args...); code != 1 || !strings.Contains(stderr, "unavailable") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
	// A document that does not fit its definition makes the copy incomplete.
	useMirrorSource(t, &mirrorTestSource{docs: map[string]map[string]any{"teams/t1": {"title": int64(1)}}}, nil)
	if _, stderr, code := runMirror(t, append(args, "--collections", "teams")...); code != exitCodeIncomplete || !strings.Contains(stderr, "did not fit") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
	// The output path cannot be made absolute.
	saved := mirrorAbs
	t.Cleanup(func() { mirrorAbs = saved })
	mirrorAbs = func(string) (string, error) { return "", errors.New("no working folder") }
	if _, stderr, code := runMirror(t, args...); code != exitCodeUsage || !strings.Contains(stderr, "no working folder") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}

func TestCommittable(t *testing.T) {
	project := mirrorProject(t)
	if tracked, err := committable(filepath.Join(project, "data", "x", "y")); err != nil || tracked {
		t.Errorf("an ignored folder: %v %v", tracked, err)
	}
	if tracked, err := committable(filepath.Join(project, "kept")); err != nil || !tracked {
		t.Errorf("a folder git would commit: %v %v", tracked, err)
	}
	if tracked, err := committable(t.TempDir()); err != nil || tracked {
		t.Errorf("a folder outside git: %v %v", tracked, err)
	}
	saved := gitCommand
	t.Cleanup(func() { gitCommand = saved })
	gitCommand = func(string, ...string) *exec.Cmd { return exec.Command(filepath.Join(project, "no-git")) }
	if tracked, err := committable(project); err != nil || tracked {
		t.Errorf("no git: %v %v", tracked, err)
	}
	gitCommand = func(_ string, args ...string) *exec.Cmd {
		if args[0] == "rev-parse" {
			return exec.Command("git", "-C", project, "rev-parse", "--show-toplevel")
		}
		return exec.Command("git", "-C", project, "check-ignore", "--no-such-flag")
	}
	if _, err := committable(project); err == nil {
		t.Error("a failing git check is no error")
	}
	if _, stderr, code := runMirror(t, "--from", "firestore://demo-example", "--policy", filepath.Join(project, "mirrors", "backup.yaml")); code != 1 || !strings.Contains(stderr, "check the output against .gitignore") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}

func TestProjectRootOfAndWithin(t *testing.T) {
	project := mirrorProject(t)
	if got := projectRootOf(filepath.Join(project, "mirrors")); got != project {
		t.Error(got)
	}
	outside := t.TempDir()
	if got := projectRootOf(outside); got != outside {
		t.Error(got)
	}
	if !within("/a/b", "/a") || !within("/a", "/a") || within("/ab", "/a") || within("/", "/a") || within("x", "/a") {
		t.Error("within")
	}
}

func TestOpenMirrorSourceOpensFirestore(t *testing.T) {
	t.Setenv("FIRESTORE_EMULATOR_HOST", "127.0.0.1:1") // the client dials lazily: nothing is reached
	source, closeSource, err := openMirrorSource(context.Background(), dbcopy.BackendRef{Scheme: "firestore", ProjectID: "demo-example", DatasetID: "(default)"})
	if err != nil || source == nil {
		t.Fatal(err)
	}
	if err := closeSource(); err != nil {
		t.Error(err)
	}
	if _, _, err := openMirrorSource(context.Background(), dbcopy.BackendRef{Scheme: "firestore"}); err == nil {
		t.Error("opened Firestore without a project")
	}
}

func TestDBCopyFromFirestoreHintsAtDBMirror(t *testing.T) {
	var hint bytes.Buffer
	hintFirestoreMirror(&hint, dbcopy.BackendRef{Scheme: "sqlite"})
	if hint.Len() != 0 {
		t.Error(hint.String())
	}
	hintFirestoreMirror(&hint, dbcopy.BackendRef{Scheme: "firestore"})
	if !strings.Contains(hint.String(), "datatug db mirror") {
		t.Error(hint.String())
	}
}
