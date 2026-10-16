package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/dbcopy/faithful"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// treeDigest is the sha256 of the sorted "<sha256>  ./<path>" lines of every file under dir: the
// output of `find . -type f | sort | xargs shasum -a 256 | shasum -a 256`.
func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		sum := sha256.Sum256(data)
		lines = append(lines, "./"+filepath.ToSlash(rel)+"\x00"+hex.EncodeToString(sum[:]))
		return nil
	}))
	sort.Strings(lines)
	var all strings.Builder
	for _, line := range lines {
		parts := strings.SplitN(line, "\x00", 2)
		all.WriteString(parts[1] + "  " + parts[0] + "\n")
	}
	sum := sha256.Sum256([]byte(all.String()))
	return hex.EncodeToString(sum[:])
}

// The digests of what `datatug db export --from sqlite://chinook.db --to ingitdb://DIR --records-format F`
// wrote before `export` became a shortcut over the copy engine (the binary built from origin/main at 09d9dbdc).
var baselineChinookDigests = map[string]string{
	"json":  "73f3ae9442a237c5a79ccd96dceb3814c8c9117b4b7c89109e9835ee3427314b",
	"jsonl": "508e342002706abc90418ad68a737fb9ed54ad5c9255ee69fef866f1dde51450",
	"ingr":  "db85f6c3334acdcbe3f6a7e8e901748544851bf09fe945591aa62fecc1a554e8",
	"csv":   "89af7be1c79f14358f8c04d72453510fc14f7b218b3648dee5e633749c5f1c6b",
	"yaml":  "f6e7fe5b1f1c2b2efc048b28411418e98b93c068f15a977a52eedf5355194e9e",
}

func chinookURL(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../../pkg/dbcopy/testdata/chinook.db")
	require.NoError(t, err)
	return "sqlite://" + path
}

// An existing invocation, `export --to ingitdb://DIR`, writes the same bytes, prints the same line and exits with
// the same code as it did before it became a shortcut.
func TestExportOldInvocationIsByteIdentical(t *testing.T) {
	for format, digest := range baselineChinookDigests {
		dir := filepath.Join(t.TempDir(), "backup")
		stdout, stderr, err := runCopy(t, "db", "export", "--from", chinookURL(t), "--to", "ingitdb://"+dir, "--records-format", format)
		require.NoError(t, err, format)
		assert.Empty(t, stdout.String())
		assert.Equal(t, fmt.Sprintf("db export: exported 11 collections and 15607 rows to ingitdb://%s\n", dir), stderr.String(), format)
		assert.Equal(t, digest, treeDigest(t, dir), format)
		// The folder exists: the old refusal, exit 1.
		_, _, err = runCopy(t, "db", "export", "--from", chinookURL(t), "--to", "ingitdb://"+dir)
		require.Error(t, err)
		assert.Equal(t, 1, err.(ExitCoder).ExitCode())
		assert.Contains(t, err.Error(), "destination already exists: ")
	}
}

func normalizedReport(t *testing.T, raw string) map[string]any {
	t.Helper()
	var report map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &report), raw)
	delete(report, "command")
	delete(report, "to")
	return report
}

// `export` and `copy --to ingitdb://<new folder>` are one engine: the same bytes, the same report, the same exit.
func TestExportAndCopyShareOneEngine(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO p VALUES (1,'a'),(2,'b')`, `INSERT INTO c VALUES (1,1),(2,2)`)
	for _, extra := range [][]string{nil, {"--include", "c"}, {"--where", "p:id:>:1", "--limit", "c:1"}, {"--dry-run"}} {
		root := t.TempDir()
		viaExport, viaPlain, viaCopy := filepath.Join(root, "e"), filepath.Join(root, "p"), filepath.Join(root, "c")
		eOut, _, eErr := runCopy(t, append([]string{"db", "export", "--from", src, "--to", "ingitdb://" + viaExport, "--json"}, extra...)...)
		pOut, _, pErr := runCopy(t, append([]string{"db", "export", "--from", src, "--to", viaPlain, "--json"}, extra...)...)
		cOut, _, cErr := runCopy(t, append([]string{"db", "copy", "--from", src, "--to", "ingitdb://" + viaCopy, "--json"}, extra...)...)
		require.NoError(t, eErr, extra)
		require.NoError(t, pErr, extra)
		require.NoError(t, cErr, extra)
		assert.Equal(t, normalizedReport(t, eOut.String()), normalizedReport(t, cOut.String()), extra)
		assert.Equal(t, normalizedReport(t, eOut.String()), normalizedReport(t, pOut.String()), extra)
		var rep faithful.Report
		require.NoError(t, json.Unmarshal(eOut.Bytes(), &rep))
		assert.Equal(t, "db export", rep.Command)
		assert.Equal(t, 0, rep.ExitCode)
		if len(extra) > 0 && extra[0] == "--dry-run" {
			for _, dir := range []string{viaExport, viaPlain, viaCopy} {
				assert.NoDirExists(t, dir)
			}
			assert.Equal(t, "dry-run", rep.Status)
			continue
		}
		assert.Equal(t, treeDigest(t, viaExport), treeDigest(t, viaCopy), extra)
		assert.Equal(t, treeDigest(t, viaExport), treeDigest(t, viaPlain), extra)
		var copyRep faithful.Report
		require.NoError(t, json.Unmarshal(cOut.Bytes(), &copyRep))
		assert.Equal(t, "db copy", copyRep.Command)
		assert.Equal(t, "ingitdb", string(copyRep.To.Engine))
	}
	// Both fail the same way on the same input: a destination that appeared meanwhile is exit 1.
	existing := t.TempDir()
	_, _, eErr := runCopy(t, "db", "export", "--from", src, "--to", existing)
	require.Error(t, eErr)
	assert.Equal(t, 1, eErr.(ExitCoder).ExitCode())
}

func exportExitCode(t *testing.T, err error) int {
	t.Helper()
	require.Error(t, err)
	coder, ok := err.(ExitCoder)
	require.True(t, ok, err)
	return coder.ExitCode()
}

func TestExportDefaultFolderAndPlainPaths(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1)`) // .../source.db
	cwd := t.TempDir()
	t.Chdir(cwd)
	_, stderr, err := runCopy(t, "db", "export", "--from", src)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db export: exported 1 collections and 1 rows to ")
	assert.DirExists(t, filepath.Join(cwd, "source.ingitdb"))
	// The folder is there now: the second run refuses it.
	_, _, err = runCopy(t, "db", "export", "--from", src)
	assert.Equal(t, 1, exportExitCode(t, err))
	// Relative plain paths, also with an "@" in them.
	for _, to := range []string{"rel", "./rel2", "team@x"} {
		_, _, err = runCopy(t, "db", "export", "--from", src, "--to", to)
		require.NoError(t, err, to)
		assert.DirExists(t, filepath.Join(cwd, to))
	}
	// A source with no name of its own has no default folder.
	_, _, err = runCopy(t, "db", "export", "--from", "sqlite://:memory:")
	assert.Equal(t, 2, exportExitCode(t, err))
	assert.Contains(t, err.Error(), "--to is required here")
	entries, _ := os.ReadDir(cwd)
	assert.Len(t, entries, 4)
}

func TestDefaultExportFolderNames(t *testing.T) {
	tests := []struct {
		name, scheme, path, want string
		fail                     bool
	}{
		{name: "sqlite file", scheme: "sqlite", path: "/data/shop.sqlite", want: "shop.ingitdb"},
		{name: "sqlite windows path", scheme: "sqlite", path: `C:\data\shop.db`, want: "shop.ingitdb"},
		{name: "sqlite odd characters", scheme: "sqlite", path: "/data/my shop (v2).db", want: "my_shop__v2.ingitdb"},
		{name: "sqlite memory", scheme: "sqlite", path: ":memory:", fail: true},
		{name: "sqlite memory mode", scheme: "sqlite", path: "file:x?mode=memory", fail: true},
		{name: "sqlite extension only", scheme: "sqlite", path: "/data/.db", fail: true},
		{name: "postgres database", scheme: "postgres", path: "postgres://alice:s3cret@db.internal:5432/shop?sslmode=disable", want: "shop.ingitdb"},
		{name: "postgres without a database", scheme: "postgres", path: "postgres://alice:s3cret@db.internal", fail: true},
		{name: "postgres malformed", scheme: "postgres", path: "postgres://%zz", fail: true},
		{name: "ingitdb folder", scheme: "ingitdb", path: "/backups/shop.ingitdb", want: "shop.ingitdb"},
		{name: "http project", scheme: "http", path: "./proj", want: "proj.ingitdb"},
		{name: "openvaultdb descriptor", scheme: "openvaultdb", path: "/v/vault.json", want: "vault.ingitdb"},
		{name: "long name", scheme: "sqlite", path: "/d/" + strings.Repeat("a", 150) + ".db", want: strings.Repeat("a", 100) + ".ingitdb"},
		{name: "unsupported scheme", scheme: "bigquery", path: "x", fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := defaultExportFolder(dbcopy.BackendRef{Scheme: tt.scheme, Path: tt.path})
			if tt.fail {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "s3cret")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "s3cret")
			assert.NotContains(t, got, "alice")
		})
	}
}

func TestSplitInGitDBOptions(t *testing.T) {
	for _, raw := range []string{"ingitdb://dir", "sqlite://x?records-format=csv", "ingitdb://dir?x=1", "ingitdb://dir?%zz=1", "env:NAME"} {
		base, format, err := splitInGitDBOptions(raw)
		require.NoError(t, err)
		assert.Equal(t, raw, base)
		assert.Empty(t, format)
	}
	base, format, err := splitInGitDBOptions("INGITDB:///a/b?records-format=jsonl")
	require.NoError(t, err)
	assert.Equal(t, "INGITDB:///a/b", base)
	assert.Equal(t, "jsonl", format)
	_, _, err = splitInGitDBOptions("ingitdb://d?records-format=csv&other=1")
	assert.ErrorContains(t, err, `unknown ingitdb destination option "other"`)
	_, _, err = splitInGitDBOptions("ingitdb://d?records-format=csv&records-format=json")
	assert.ErrorContains(t, err, "more than once")
}

func TestRecordsFormatAsDestinationOption(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1)`)
	root := t.TempDir()
	for command, args := range map[string][]string{"export": {"db", "export", "--from", src}, "copy": {"db", "copy", "--from", src}} {
		dir := filepath.Join(root, command)
		_, _, err := runCopy(t, append(args, "--to", "ingitdb://"+dir+"?records-format=jsonl")...)
		require.NoError(t, err, command)
		assert.FileExists(t, filepath.Join(dir, "p", "records.jsonl"))
		_, _, err = runCopy(t, append(args, "--to", "ingitdb://"+dir+"?records-format=jsonl&x=1")...)
		assert.Equal(t, 2, exportExitCode(t, err))
	}
	// The flag and the option agree, or are refused.
	dir := filepath.Join(root, "agree")
	_, _, err := runCopy(t, "db", "export", "--from", src, "--to", "ingitdb://"+dir+"?records-format=csv", "--records-format", "CSV")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "p", "records.csv"))
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", "ingitdb://"+filepath.Join(root, "x")+"?records-format=csv", "--records-format", "json")
	assert.Equal(t, 2, exportExitCode(t, err))
	// An invalid format is the engine's refusal, as it always was.
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "bad"), "--records-format", "xml")
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.NoDirExists(t, filepath.Join(root, "bad"))
	// Copy into a project that exists takes no format.
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", "ingitdb://"+dir+"?records-format=csv")
	assert.Equal(t, 2, exportExitCode(t, err))
	assert.ErrorContains(t, err, "does not exist yet")
}

func TestExportRefusesFlagsThatDoNotApply(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	root := t.TempDir()
	flags := [][]string{{"--overwrite", "reload"}, {"--to-schema", "x"}, {"--decimals", "text"}, {"--parallel-streams", "2"}, {"--progress"}, {"--recover-job-ref", "{}"}}
	for _, flag := range flags {
		name := strings.TrimPrefix(flag[0], "--")
		for _, command := range []string{"export", "copy"} {
			dir := filepath.Join(root, command+name)
			_, _, err := runCopy(t, append([]string{"db", command, "--from", src, "--to", "ingitdb://" + dir}, flag...)...)
			// copy handles --recover-job-ref before anything else.
			assert.Equal(t, 2, exportExitCode(t, err), command+" "+name)
			assert.NoDirExists(t, dir)
			if command != "copy" || name != "recover-job-ref" {
				assert.ErrorContains(t, err, "--"+name+" is not supported for a new inGitDB destination")
			}
		}
	}
	// A JSON caller gets the refusal as a report.
	stdout, _, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "j"), "--json", "--progress")
	assert.Equal(t, 2, exportExitCode(t, err))
	var rep faithful.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep))
	assert.Equal(t, "failed", rep.Status)
	assert.Equal(t, 2, rep.ExitCode)
	assert.Equal(t, "ingitdb", string(rep.To.Engine))
	assert.Equal(t, "sqlite", string(rep.From.Engine))
	// A flag copy only has for PostgreSQL sources.
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "k"), "--from-schema", "s")
	assert.Equal(t, 2, exportExitCode(t, err))
}

func TestExportDestinationAndSourceArgumentErrors(t *testing.T) {
	src := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	root := t.TempDir()
	t.Setenv("HOME", root)
	cases := map[string][]string{
		"bad from":            {"--from", "nope", "--to", filepath.Join(root, "a")},
		"datatug ref":         {"--from", "datatug:///x", "--to", filepath.Join(root, "a")},
		"empty to":            {"--from", src, "--to", ""},
		"bad ingitdb to":      {"--from", src, "--to", "ingitdb://"},
		"remote ingitdb to":   {"--from", src, "--to", "ingitdb://github.com/org/repo"},
		"not ingitdb to":      {"--from", src, "--to", "sqlite://" + filepath.Join(root, "x.db")},
		"include and exclude": {"--from", src, "--to", filepath.Join(root, "a"), "--include", "p", "--exclude", "p"},
	}
	for name, args := range cases {
		stdout, _, err := runCopy(t, append([]string{"db", "export"}, args...)...)
		assert.GreaterOrEqual(t, exportExitCode(t, err), 2, name)
		assert.Empty(t, stdout.String(), name)
	}
	// A plain path that looks like a Windows drive is a path, not a scheme.
	ref, format, err := parseExportDestination(dbExportCommand(), `C:\backups\shop`)
	if err == nil {
		assert.Equal(t, "ingitdb", ref.Scheme)
	} else {
		assert.NotContains(t, err.Error(), "unsupported scheme")
	}
	assert.Empty(t, format)
	// A copy to a new folder reports a malformed filter as usage.
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", "ingitdb://"+filepath.Join(root, "n"), "--include", "p", "--exclude", "p")
	assert.Equal(t, 2, exportExitCode(t, err))
	_, _, err = runCopy(t, "db", "copy", "--from", src, "--to", "ingitdb://"+filepath.Join(root, "n")+"?records-format=a&records-format=b")
	assert.Equal(t, 2, exportExitCode(t, err))
	// A folder whose parent is missing is the engine's refusal.
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "missing", "deeper"))
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.ErrorContains(t, err, "destination parent")
}

func warningFixture(t *testing.T) string {
	return sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY, name TEXT)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`,
		`INSERT INTO p VALUES (1,'a'),(2,'b'),(3,'c')`, `INSERT INTO c VALUES (1,1),(2,2),(3,3)`)
}

func TestExportWarningsAndFailOnWarning(t *testing.T) {
	src := warningFixture(t)
	root := t.TempDir()
	// --include leaves out the table c refers to: the key is dropped and said so; the export is still made.
	_, stderr, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "w"), "--include", "c")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db export: exported 1 collections and 3 rows to ")
	assert.Contains(t, stderr.String(), "db export warning: foreign-key-dropped: c.")
	// --fail-on-warning turns the warning into exit 3, with the export made and the report saying so.
	stdout, _, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "f"), "--include", "c", "--fail-on-warning", "--json")
	assert.Equal(t, 3, exportExitCode(t, err))
	assert.ErrorContains(t, err, "1 warning(s) and --fail-on-warning is set")
	assert.DirExists(t, filepath.Join(root, "f"))
	var rep faithful.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep))
	assert.Equal(t, "ok-with-warnings", rep.Status)
	assert.Equal(t, 3, rep.ExitCode)
	require.Len(t, rep.Warnings, 1)
	assert.Equal(t, "foreign-key-dropped", rep.Warnings[0].Kind)
	// --fail-on-warning with nothing to warn about is exit 0.
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "g"), "--fail-on-warning")
	require.NoError(t, err)
	// Narrowing the parent's rows warns that the child may point at rows that are not exported.
	_, stderr, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "h"), "--where", "p:id:>:1", "--limit", "c:2")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db export: exported 2 collections and 4 rows to ")
	assert.Contains(t, stderr.String(), "filtered-parent")
}

func TestExportDryRunText(t *testing.T) {
	src := warningFixture(t)
	root := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	_, stderr, err := runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "d"), "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db export: would export 2 collections and 6 rows to ")
	entries, _ := os.ReadDir(root)
	assert.Empty(t, entries)
}

func TestExportFilterErrors(t *testing.T) {
	src := warningFixture(t)
	root := t.TempDir()
	for name, args := range map[string][]string{
		"unknown include":      {"--include", "nope"},
		"where on left out":    {"--include", "c", "--where", "p:id:=:1"},
		"where on missing col": {"--where", "p:nope:=:1"},
	} {
		_, _, err := runCopy(t, append([]string{"db", "export", "--from", src, "--to", filepath.Join(root, "x")}, args...)...)
		assert.Equal(t, 1, exportExitCode(t, err), name)
		assert.NoDirExists(t, filepath.Join(root, "x"), name)
	}
}

func TestExportNothingToExport(t *testing.T) {
	empty := sqliteFixture(t, `CREATE VIEW v AS SELECT 1 AS x`)
	root := t.TempDir()
	// export: exit 1, as it always was.
	_, _, err := runCopy(t, "db", "export", "--from", empty, "--to", filepath.Join(root, "a"))
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.ErrorContains(t, err, "source has no collections to export")
	// copy: a note and exit 0, nothing created, like every other copy of an empty source.
	_, stderr, err := runCopy(t, "db", "copy", "--from", empty, "--to", "ingitdb://"+filepath.Join(root, "b"))
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "source has no tables; nothing to copy")
	assert.NoDirExists(t, filepath.Join(root, "b"))
	// --include/--exclude that leave nothing.
	src := warningFixture(t)
	_, _, err = runCopy(t, "db", "export", "--from", src, "--to", filepath.Join(root, "c"), "--exclude", "p,c")
	assert.Equal(t, 1, exportExitCode(t, err))
	assert.ErrorContains(t, err, "no collection is selected to export")
	_, stderr, err = runCopy(t, "db", "copy", "--from", src, "--to", "ingitdb://"+filepath.Join(root, "d"), "--exclude", "p,c")
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "nothing to copy")
}

func TestExportOpenFailureReportsJSON(t *testing.T) {
	root := t.TempDir()
	stdout, _, err := runCopy(t, "db", "export", "--from", "sqlite://"+filepath.Join(root, "missing.db"), "--to", filepath.Join(root, "o"), "--json")
	assert.Equal(t, 4, exportExitCode(t, err))
	var rep faithful.Report
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rep))
	assert.Equal(t, "failed", rep.Status)
	assert.Equal(t, 4, rep.ExitCode)
	assert.NoDirExists(t, filepath.Join(root, "o"))
}
