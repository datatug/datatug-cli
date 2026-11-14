package dbmirror

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ingitdb/ingitdb-go/ingitdb"
	"google.golang.org/api/option"
)

// memSource is a Source over documents held in memory, with the ordering and
// range semantics of Firestore and a hook to fail any method.
type memSource struct {
	docs    map[string]map[string]any
	fail    map[string]error // method -> error
	queries []Query
	gets    []string
	counts  []Query
}

func newMemSource() *memSource {
	return &memSource{docs: map[string]map[string]any{}, fail: map[string]error{}}
}

func (m *memSource) RootCollections(context.Context) ([]string, error) {
	if err := m.fail["RootCollections"]; err != nil {
		return nil, err
	}
	var ids []string
	for path := range m.docs {
		root, _, _ := strings.Cut(path, "/")
		if !slices.Contains(ids, root) {
			ids = append(ids, root)
		}
	}
	return ids, nil
}

func (m *memSource) matching(q Query) []string {
	var paths []string
	for path := range m.docs {
		parts := strings.Split(path, "/")
		if parts[len(parts)-2] != q.Collection || (!q.Group && len(parts) != 2) {
			continue
		}
		if q.Under != "" && (comparePaths(path, q.Under) < 0 || comparePaths(path, q.Under+"") >= 0) {
			continue
		}
		paths = append(paths, path)
	}
	slices.SortFunc(paths, comparePaths)
	return paths
}

func (m *memSource) Page(_ context.Context, q Query) ([]Doc, error) {
	m.queries = append(m.queries, q)
	if err := m.fail["Page"]; err != nil {
		return nil, err
	}
	var docs []Doc
	for _, path := range m.matching(q) {
		if q.After != "" && comparePaths(path, q.After) <= 0 {
			continue
		}
		if len(docs) == q.Limit {
			break
		}
		docs = append(docs, Doc{Path: path, Data: maps.Clone(m.docs[path])})
	}
	return docs, nil
}

func (m *memSource) Get(_ context.Context, path string) (*Doc, error) {
	m.gets = append(m.gets, path)
	if err := m.fail["Get"]; err != nil {
		return nil, err
	}
	data, ok := m.docs[path]
	if !ok {
		return nil, nil
	}
	return &Doc{Path: path, Data: maps.Clone(data)}, nil
}

func (m *memSource) Count(_ context.Context, q Query) (int64, error) {
	m.counts = append(m.counts, q)
	if err := m.fail["Count"]; err != nil {
		return 0, err
	}
	return int64(len(m.matching(q))), nil
}

var errBoom = errors.New("boom")

var created = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// exampleSource holds invented documents for the test mirror (testdata/mirror).
func exampleSource() *memSource {
	m := newMemSource()
	m.docs["teams/t1"] = map[string]any{"title": "Team one", "createdAt": created, "size": int64(3), "score": 2.0,
		"tags": []any{"a", "b"}, "settings": map[string]any{"z": true, "a": nil}}
	m.docs["teams/t2"] = map[string]any{"title": "Team two", "createdAt": created}
	m.docs["teams/t1x"] = map[string]any{"title": "Sibling of t1", "createdAt": created}
	m.docs["teams/t1/ext/cal"] = map[string]any{"data": map[string]any{"k": int64(1)}}
	m.docs["teams/t1/ext/cal/events/e1"] = map[string]any{"title": "Event", "at": created.Add(90 * time.Minute)}
	m.docs["teams/t1/ext/cal/events/e1/notes/n1"] = map[string]any{"text": "a note"}
	m.docs["teams/t1/ext/gone/events/e2"] = map[string]any{"title": "Below a missing parent"}
	m.docs["teams/t1x/ext/cal/events/e3"] = map[string]any{"title": "Event of the sibling"}
	m.docs["teams/t2/ext/cal/events/bad"] = map[string]any{"title": int64(5)}
	m.docs["teams/t2/ext/cal/events/.hidden"] = map[string]any{"title": "hidden"}
	m.docs["teams/t2/logs/l1"] = map[string]any{"text": "a log"}
	m.docs["ext/crm/customers/c1"] = map[string]any{"email": "someone@example.com", "name": "Some One",
		"contacts": []any{map[string]any{"phone": "+000", "kind": "home"}, "not a map"}}
	m.docs["ext/crm/secrets/s1"] = map[string]any{"code": "SECRET"}
	m.docs["ext/crm/loose/x1"] = map[string]any{"text": "undeclared"}
	m.docs["other/o1"] = map[string]any{"title": "a root collection the mirror does not define"}
	m.docs["other/o1/events/e9"] = map[string]any{"title": "an undefined path"}
	m.docs["other/o1/notes/n9"] = map[string]any{"text": "another undefined path"}
	return m
}

func testDefinitions(t *testing.T) *Definitions {
	t.Helper()
	defs, err := LoadDefinitions(filepath.Join("testdata", "mirror"))
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func testPolicy(t *testing.T) *Policy {
	t.Helper()
	policy, err := ReadPolicy(filepath.Join("testdata", "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

type (
	ingitdbReadOption      = ingitdb.ReadOption
	ingitdbDefinition      = ingitdb.Definition
	ingitdbValidationError = ingitdb.ValidationError
	optionClientOption     = option.ClientOption
)
