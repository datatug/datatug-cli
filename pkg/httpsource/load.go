package httpsource

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// LoadedQuery is one HTTP-type QueryDef found by LoadHTTPQueries, together
// with the folder it was found in (relative to <projectDir>/queries) —
// needed to locate its sibling URL-template file alongside it.
type LoadedQuery struct {
	Def        *datatug.QueryDef
	FolderPath string
}

// LoadHTTPQueries scans <projectDir>/queries/** for *.query.json files
// declaring "type": "HTTP", parsing each into a datatug.QueryDef.
//
// This reads project files directly with encoding/json rather than going
// through pkg/datatug-core/storage/filestore's generic project-item store,
// for two reasons: that store's loader does not read a query's sibling
// text-body file back into QueryDef.Text (only the SAVE path writes it —
// see store_queries_saver.go; LoadURLTemplate below is this package's own
// replacement for that missing read), and pulling in the full store
// abstraction (which also knows about SQL/GraphQL queries, folders, and
// project-wide caching) for a read-only, HTTP-only scan would add a much
// larger dependency surface than this package needs.
func LoadHTTPQueries(projectDir string) ([]LoadedQuery, error) {
	queriesDir := filepath.Join(projectDir, storage.QueriesFolder)
	suffix := "." + storage.QueryFileSuffix + ".json"

	var out []LoadedQuery
	err := filepath.WalkDir(queriesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), suffix) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		var def datatug.QueryDef
		if err := json.Unmarshal(data, &def); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if def.Type != datatug.QueryTypeHTTP {
			return nil
		}
		if def.ID == "" {
			def.ID = strings.TrimSuffix(d.Name(), suffix)
		}
		rel, err := filepath.Rel(queriesDir, filepath.Dir(path))
		if err != nil {
			return fmt.Errorf("relativize %s: %w", path, err)
		}
		out = append(out, LoadedQuery{Def: &def, FolderPath: rel})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("httpsource: scan %s: %w", queriesDir, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Def.ID < out[j].Def.ID })
	return out, nil
}

// urlTemplateFileName is the sibling file a query's URL template is stored
// in, matching the naming convention store_queries_saver.go's saveQuery
// writes (fileExt = strings.ToLower(string(query.Type)), i.e. "http" for
// QueryTypeHTTP).
func urlTemplateFileName(id string) string {
	return fmt.Sprintf("%s.%s.http", id, storage.QueryFileSuffix)
}

// LoadURLTemplate reads the URL template for the HTTP query id found in
// folderPath (as returned by LoadHTTPQueries), trimming surrounding
// whitespace (the sibling .query.http files in the demo project end with a
// trailing newline).
func LoadURLTemplate(projectDir, folderPath, id string) (string, error) {
	path := filepath.Join(projectDir, storage.QueriesFolder, folderPath, urlTemplateFileName(id))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("httpsource: read URL template for query %q: %w", id, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// fixturesDir returns <projectDir>/fixtures/http, the recorded-snapshot
// directory convention the demo project (and REQ:http-reference-source)
// use.
func fixturesDir(projectDir string) string {
	return filepath.Join(projectDir, "fixtures", "http")
}

// readFixtureSample best-effort reads and decodes a query's recorded
// fixture (used only to infer RowsPath/KeyField defaults — see
// BuildCollection). A missing or unparseable fixture is not an error here:
// it just means those defaults fall back to their no-sample answer.
func readFixtureSample(dir, id string) map[string]any {
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil
	}
	var sample map[string]any
	if json.Unmarshal(data, &sample) != nil {
		return nil
	}
	return sample
}
