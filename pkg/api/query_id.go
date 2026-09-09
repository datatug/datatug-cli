package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/storage"
)

// ErrQueryNotFound is returned by ResolveQueryID when id (bare or
// folder-qualified) does not match any query file under a project's
// queries/ tree — the contract-route/legacy-envelope translation for this
// is NOT_FOUND (404), never a raw filesystem error (S97).
var ErrQueryNotFound = errors.New("query not found")

// ErrAmbiguousQueryID is returned by ResolveQueryID when a bare id (no "/")
// matches more than one query across different folders — translated to
// INVALID_REQUEST (400) naming every candidate, never a 500.
var ErrAmbiguousQueryID = errors.New("query id is ambiguous across folders")

// ResolveQueryID resolves id to the canonical, folder-qualified query id
// datatug-core's fsQueriesStore.LoadQuery actually needs to find a query
// that lives in a subfolder (every query in the demo project does —
// GET /datatug/queries/get_query?project=...&query=customer-invoices used
// to 500 with the store's own raw "open .../customer-invoices.query.json:
// no such file or directory" for exactly this reason).
//
// Canonical-id convention (this stream's decision, marked as an assumption
// — not a founder ruling; api-contract.md's own Candidate/ExecutionRequest
// types declare `queryId: string` with no format guidance either way): the
// folder-qualified id ("<folder>/<bare-id>", "/"-joined, matching
// fsQueriesStore.LoadQuery's own id-splitting convention) is canonical
// because it is exactly what the filesystem layout already encodes and is
// guaranteed unique. A bare id is still accepted on input, but only when it
// names exactly one query across every folder in the project.
//
//   - id containing "/": trusted as already folder-qualified; resolved
//     (returned as-is) only when a query file exists at that exact path —
//     ErrQueryNotFound otherwise.
//   - id with no "/": matched against every query file's bare
//     (folder-relative) id. Exactly one match resolves to its canonical
//     form; zero is ErrQueryNotFound; more than one is ErrAmbiguousQueryID,
//     naming every canonical candidate so the caller can pick one.
func ResolveQueryID(projectDir, id string) (string, error) {
	canonicalToBare, err := QueryIDIndex(projectDir)
	if err != nil {
		return "", err
	}
	if strings.Contains(id, "/") {
		if _, ok := canonicalToBare[id]; ok {
			return id, nil
		}
		return "", fmt.Errorf("%w: %q", ErrQueryNotFound, id)
	}
	var matches []string
	for canonical, bare := range canonicalToBare {
		if bare == id {
			matches = append(matches, canonical)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: %q", ErrQueryNotFound, id)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("%w: %q matches %s", ErrAmbiguousQueryID, id, strings.Join(matches, ", "))
	}
}

// QueryIDIndex walks every queries/**/*.query.json file under projectDir,
// mapping each one's canonical (folder-qualified, "/"-joined) id to its
// bare (folder-relative) id — a query directly under queries/ (no
// subfolder) has canonical == bare. A missing queries/ directory is not an
// error (an empty project tree is valid); it simply produces an empty
// index.
func QueryIDIndex(projectDir string) (map[string]string, error) {
	queriesDir := filepath.Join(projectDir, storage.QueriesFolder)
	suffix := "." + storage.QueryFileSuffix + ".json"
	index := map[string]string{}
	err := filepath.WalkDir(queriesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), suffix) {
			return nil
		}
		bareID := strings.TrimSuffix(d.Name(), suffix)
		rel, relErr := filepath.Rel(queriesDir, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		canonical := bareID
		if rel != "." {
			canonical = filepath.ToSlash(filepath.Join(rel, bareID))
		}
		index[canonical] = bareID
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return index, nil
		}
		return nil, fmt.Errorf("list queries under %s: %w", queriesDir, err)
	}
	return index, nil
}
