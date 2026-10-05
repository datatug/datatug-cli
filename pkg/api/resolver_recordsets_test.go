package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// The recordset definitions of a project are sources, each by the ID that its file declares
// (or by its file name when it declares none). A source ID is joined into a path by the routes
// that read the definition, and a project file may hold anything: a definition whose source ID,
// whichever of the two it is, is not a plain source ID is not a source, and nothing is made of it.

func writeRecordsetFile(t *testing.T, projectDir, name, content string) {
	t.Helper()
	dir := filepath.Join(projectDir, storage.RecordsetsFolder)
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, name), content)
}

func TestRecordsetSources_AFileWhoseIDIsNotAPlainSourceIDIsSkippedWithOneLogLine(t *testing.T) {
	projectDir := t.TempDir()
	writeRecordsetFile(t, projectDir, "notes.recordset.json", `{"id":"support-notes","title":"Notes"}`)
	writeRecordsetFile(t, projectDir, "noid.recordset.json", `{"title":"A file name is its ID"}`)
	// A file that declares no ID is a source by its file name, which must be a plain source ID
	// too: the ID is "" here, and the name is what is checked.
	writeRecordsetFile(t, projectDir, "my notes.recordset.json", `{"title":"A file name with a space"}`)
	unsafe := map[string]string{
		"my notes.recordset.json":  "",
		"traversal.recordset.json": "../../outside/x",
		"slashed.recordset.json":   "a/b",
		"url.recordset.json":       "ingitdb://alice:s3cretpw@host/x",
		"absolute.recordset.json":  "/etc/passwd",
		"backslash.recordset.json": `a\b`,
		"dots.recordset.json":      "..",
		"long.recordset.json":      strings.Repeat("a", 129),
	}
	for name, id := range unsafe {
		if id != "" {
			writeRecordsetFile(t, projectDir, name, fmt.Sprintf(`{"id":%q}`, id))
		}
	}
	logged := captureLog(t)

	sources, err := recordsetSources(projectDir)

	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, source := range sources {
		ids = append(ids, source.ID)
		if source.Collection != source.ID || source.Kind != SourceKindInGitDB {
			t.Errorf("the source %+v is not one collection of the shared store", source)
		}
	}
	sort.Strings(ids)
	if got, want := strings.Join(ids, ","), "noid,support-notes"; got != want {
		t.Errorf("sources = %s, want %s: a file with a source ID that is not a plain source ID is not a source", got, want)
	}
	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != len(unsafe) {
		t.Errorf("%d lines were logged, want one for each of the %d files that are skipped:\n%s", len(lines), len(unsafe), logged.String())
	}
	for name, id := range unsafe {
		if got := strings.Count(logged.String(), name); got != 1 {
			t.Errorf("%s is named %d times in the log, want once", name, got)
		}
		if strings.Contains(logged.String(), id) && len(id) > 3 {
			t.Errorf("the log holds the ID of %s, which may be a source string: %q", name, logged.String())
		}
	}
}

// A project's recordset definitions that cannot be listed, read or parsed fail the listing
// with one sentence that names nothing of the file or of the folder: the cause is logged.
func TestRecordsetSources_AFailureIsOneBuiltSentence(t *testing.T) {
	const want = "the recordset definitions of the project could not be listed"
	for name, tc := range map[string]struct {
		prepare func(t *testing.T, projectDir string)
		// logged is what the log holds of the cause.
		logged func(projectDir string) string
	}{
		"a file where the folder is expected": {func(t *testing.T, projectDir string) {
			mustWrite(t, filepath.Join(projectDir, storage.RecordsetsFolder), "x")
		}, func(projectDir string) string { return projectDir }},
		"a file that is not JSON": {func(t *testing.T, projectDir string) {
			writeRecordsetFile(t, projectDir, "notes.recordset.json", "{not json")
		}, func(string) string { return "invalid character" }},
		"a file that cannot be read": {func(t *testing.T, projectDir string) {
			writeRecordsetFile(t, projectDir, "notes.recordset.json", "{}")
			previous := readRecordsetFile
			readRecordsetFile = func(path string) ([]byte, error) {
				return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("permission denied")}
			}
			t.Cleanup(func() { readRecordsetFile = previous })
		}, func(projectDir string) string { return projectDir }},
	} {
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			tc.prepare(t, projectDir)
			logged := captureLog(t)

			sources, err := recordsetSources(projectDir)

			if err == nil || err.Error() != want || sources != nil {
				t.Fatalf("got %v, %v, want exactly %q", sources, err, want)
			}
			if !strings.Contains(logged.String(), tc.logged(projectDir)) {
				t.Errorf("the cause is not in the log: %q", logged.String())
			}
			// The cause is not what errors.Is finds: the answer is the sentence.
			if errors.Unwrap(err) != nil {
				t.Errorf("the answer wraps %v", errors.Unwrap(err))
			}
		})
	}
}

// The error of the listing of the catalogs of an environment that a store gives quotes the
// path it built: the answer names the environment (when it is a plain name) and not the cause.
func TestResolveSource_AFailedListingOfCatalogsNamesNoPath(t *testing.T) {
	projectDir := t.TempDir()
	cause := fmt.Errorf("read %s: no such directory", filepath.Join(projectDir, "environments", "local", "catalogs"))
	store := mockProjectStore{loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
		return nil, cause
	}}
	logged := captureLog(t)

	_, err := ResolveSource(context.Background(), store, projectDir, "local", "chinook")

	if err == nil || err.Error() != `could not list catalogs for environment "local"` {
		t.Fatalf("got %v, want exactly the sentence that names the environment", err)
	}
	if errors.Is(err, cause) || strings.Contains(err.Error(), projectDir) {
		t.Errorf("the answer holds the cause: %v", err)
	}
	if !strings.Contains(logged.String(), projectDir) {
		t.Errorf("the cause, which names the path, is not in the log: %q", logged.String())
	}
}
