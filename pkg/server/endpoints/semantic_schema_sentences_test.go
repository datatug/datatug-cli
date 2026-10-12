package endpoints

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// The definition of a source that the project keeps as a recordset is read from a file of the
// project, by the ID of the source, which is joined into a path: only a plain source ID is
// (the ID of a catalog's model is read out of a project file, and may be anything). A
// definition that is missing, that cannot be read, and one that cannot be parsed are one
// answer, a sentence built from the source, and holds no path of the server.

// resolveWith runs resolveSource for an inGitDB source that the registry resolves as given.
func resolveWith(t *testing.T, projectDir string, resolved api.ResolvedSource) (resolvedSource, error) {
	t.Helper()
	previous := apiResolveSource
	t.Cleanup(func() { apiResolveSource = previous })
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return resolved, nil
	}
	return resolveSource(context.Background(), nil, projectDir, "local", resolved.ID, resolved.ID)
}

func inGitDBSource(id string) api.ResolvedSource {
	return api.ResolvedSource{ID: id, Kind: api.SourceKindInGitDB, URL: "ingitdb://store", Collection: id}
}

func TestResolveSource_ARecordsetDefinitionThatCannotBeUsedIsOneBuiltSentence(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, recordsets string){
		"missing": func(t *testing.T, recordsets string) {},
		"a folder where the file is expected": func(t *testing.T, recordsets string) {
			mustMkdirAll(t, filepath.Join(recordsets, "notes.recordset.json"))
		},
		"a file that is not JSON": func(t *testing.T, recordsets string) {
			mustWriteFile(t, filepath.Join(recordsets, "notes.recordset.json"), "{not json")
		},
		"a file where the folder is expected": func(t *testing.T, recordsets string) {
			if err := os.RemoveAll(recordsets); err != nil {
				t.Fatal(err)
			}
			mustWriteFile(t, recordsets, "x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			recordsets := filepath.Join(projectDir, "recordsets")
			mustMkdirAll(t, recordsets)
			prepare(t, recordsets)

			_, err := resolveWith(t, projectDir, inGitDBSource("notes"))

			var contract *contractError
			if !errors.As(err, &contract) || contract.Code != apicontract.ErrCodeSourceUnavailable {
				t.Fatalf("got %v, want a SOURCE_UNAVAILABLE contract error", err)
			}
			if want := `source "notes" has no readable recordset definition`; contract.Message != want {
				t.Errorf("message = %q, want exactly %q", contract.Message, want)
			}
			for _, leak := range []string{projectDir, filepath.Base(projectDir), "no such file", "is a directory", "not a directory", "invalid character", "read ", "parse "} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("the answer holds %q: %v", leak, err)
				}
			}
		})
	}
}

// A source whose ID is not a plain source ID (a model that a catalog's file names) is never
// joined into a path: a definition that the ID would lead to, outside the folder of the
// recordsets, is not read.
func TestResolveSource_ARecordsetSourceThatIsNotAPlainSourceIDIsNeverJoinedIntoAPath(t *testing.T) {
	projectDir := t.TempDir()
	mustMkdirAll(t, filepath.Join(projectDir, "recordsets"))
	// What "../outside" leads to from the folder of the recordsets, a definition that parses.
	mustWriteFile(t, filepath.Join(projectDir, "outside.recordset.json"), `{"columns":[{"name":"leaked","type":"string"}]}`)

	for _, id := range []string{"../outside", "../../x", "a/b", `a\b`, "..", "/etc/passwd", "ingitdb://alice:s3cretpw@host/x"} {
		resolved, err := resolveWith(t, projectDir, inGitDBSource(id))
		var contract *contractError
		if !errors.As(err, &contract) || contract.Code != apicontract.ErrCodeSourceUnavailable {
			t.Errorf("%q: got %v, %v, want a SOURCE_UNAVAILABLE contract error", id, resolved, err)
			continue
		}
		if len(resolved.Columns) != 0 {
			t.Errorf("%q: columns were read from a file outside the recordsets", id)
		}
		if strings.Contains(err.Error(), id) && len(id) > 3 {
			t.Errorf("%q: the answer echoes the ID of the source: %v", id, err)
		}
	}

	// A plain ID is read.
	mustWriteFile(t, filepath.Join(projectDir, "recordsets", "notes.recordset.json"), `{"columns":[{"name":"id","type":"integer"}]}`)
	resolved, err := resolveWith(t, projectDir, inGitDBSource("notes"))
	if err != nil || len(resolved.Columns) != 1 || resolved.Columns[0].Name != "id" {
		t.Errorf("a plain source ID: %+v, %v, want its column", resolved, err)
	}
}
