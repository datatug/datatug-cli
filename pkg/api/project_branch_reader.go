package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

const (
	maxQualifiedDirEntries = 10000
)

// qualifiedProjectReader confines every selected-branch file access to the
// registered project root and accounts bytes before decoding any document.
type qualifiedProjectReader struct {
	root  *os.Root
	bytes int
}

func openQualifiedProjectReader(projectDir string) (*qualifiedProjectReader, error) {
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return nil, err
	}
	return &qualifiedProjectReader{root: root}, nil
}

func (r *qualifiedProjectReader) close() { _ = r.root.Close() }

func (r *qualifiedProjectReader) readFile(name string) ([]byte, error) {
	return r.readFileLimit(name, maxQualifiedReadBytes)
}

func (r *qualifiedProjectReader) readFileLimit(name string, maxBytes int) ([]byte, error) {
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return nil, errors.New("invalid qualified project path")
	}
	f, err := r.root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maxBytes) {
		return nil, errors.New("qualified project document is unavailable or too large")
	}
	remaining := maxQualifiedReadBytes - r.bytes
	if remaining < 0 {
		return nil, fmt.Errorf("qualified project read exceeds %d bytes", maxQualifiedReadBytes)
	}
	limit := maxBytes
	if remaining < limit {
		limit = remaining
	}
	body, err := readQualifiedLimited(f, limit)
	if err != nil {
		return nil, err
	}
	r.bytes += len(body)
	return body, nil
}

func readQualifiedLimited(source io.Reader, limit int) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(source, int64(limit)+1))
	if err != nil || len(body) > limit {
		return nil, fmt.Errorf("qualified project read exceeds %d bytes", maxQualifiedReadBytes)
	}
	return body, nil
}

func (r *qualifiedProjectReader) readJSON(name string, target any) ([]byte, error) {
	body, err := r.readFile(name)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return nil, err
	}
	return body, nil
}

func (r *qualifiedProjectReader) dirEntries(name string) ([]os.DirEntry, error) {
	f, err := r.root.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	entries := make([]os.DirEntry, 0)
	for {
		batch, readErr := f.ReadDir(256)
		if len(entries)+len(batch) > maxQualifiedDirEntries {
			return nil, fmt.Errorf("qualified project directory exceeds %d entries", maxQualifiedDirEntries)
		}
		entries = append(entries, batch...)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return entries, nil
}

func itemIDs(entries []os.DirEntry, suffix string) []string {
	ids := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			ids[name] = struct{}{}
			continue
		}
		if id, ok := strings.CutSuffix(name, "."+suffix+".json"); ok {
			ids[id] = struct{}{}
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	return result
}

func readQualifiedOptionalFile(reader *qualifiedProjectReader, name string) ([]byte, error) {
	body, err := reader.readFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return body, err
}

func readQualifiedOptionalFileLimit(reader *qualifiedProjectReader, name string, limit int) ([]byte, error) {
	body, err := reader.readFileLimit(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return body, err
}

func entityFileName(id string) string { return storage.JsonFileName(id, storage.EntityFileSuffix) }
func boardFileName(id string) string  { return storage.JsonFileName(id, storage.BoardFileSuffix) }
func envFileName(id string) string    { return storage.JsonFileName(id, storage.EnvFileSuffix) }

func (r *qualifiedProjectReader) readQualifiedEntity(id string) (*datatug.Entity, error) {
	body, err := readQualifiedEntityDocument(r, id)
	if err != nil {
		return nil, err
	}
	var entity datatug.Entity
	if err := json.Unmarshal(body, &entity); err != nil {
		return nil, err
	}
	entity.SetID(id)
	return &entity, nil
}

func readQualifiedEntities(reader *qualifiedProjectReader) (datatug.Entities, error) {
	entries, err := reader.dirEntries(storage.EntitiesFolder)
	if err != nil {
		return nil, err
	}
	ids := itemIDs(entries, storage.EntityFileSuffix)
	if len(ids) > maxQualifiedProjectItems {
		return nil, fmt.Errorf("entity list exceeds %d items", maxQualifiedProjectItems)
	}
	sort.Strings(ids)
	entities := make(datatug.Entities, 0, len(ids))
	for _, id := range ids {
		if err := ValidateIdentifier("entity", id); err != nil {
			return nil, err
		}
		entity, err := reader.readQualifiedEntity(id)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && isDirectoryEntry(entries, id) {
				continue
			}
			return nil, err
		}
		entities = append(entities, entity)
	}
	return entities, nil
}

func isDirectoryEntry(entries []os.DirEntry, name string) bool {
	for _, entry := range entries {
		if entry.Name() == name {
			return entry.IsDir()
		}
	}
	return false
}

func (r *qualifiedProjectReader) readQualifiedBoard(id string) (*datatug.Board, error) {
	if err := ValidateIdentifier("board", id); err != nil {
		return nil, err
	}
	flatPath := path.Join(storage.BoardsFolder, boardFileName(id))
	nestedPath := path.Join(storage.BoardsFolder, id, storage.BoardFileSuffix+".json")
	nested, err := readQualifiedOptionalFile(r, nestedPath)
	if err != nil {
		return nil, err
	}
	flat, err := readQualifiedOptionalFile(r, flatPath)
	if err != nil {
		return nil, err
	}
	if nested == nil && flat == nil {
		return nil, fs.ErrNotExist
	}
	decode := func(body []byte) (*datatug.Board, error) {
		if body == nil {
			return nil, nil
		}
		if err := validateQualifiedStoredID(body, id, "board"); err != nil {
			return nil, err
		}
		board := new(datatug.Board)
		if err := json.Unmarshal(body, board); err != nil {
			return nil, err
		}
		board.SetID(id)
		return board, nil
	}
	nestedBoard, err := decode(nested)
	if err != nil {
		return nil, err
	}
	flatBoard, err := decode(flat)
	if err != nil {
		return nil, err
	}
	if nestedBoard != nil && flatBoard != nil && !reflect.DeepEqual(nestedBoard, flatBoard) {
		return nil, errors.New("board is stored in both layouts with different content")
	}
	if nestedBoard != nil {
		return nestedBoard, nil
	}
	return flatBoard, nil
}

func validateQualifiedStoredID(body []byte, expected, kind string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return fmt.Errorf("%s document is not a JSON object", kind)
	}
	rawID, exists := fields["id"]
	if !exists {
		return nil
	}
	var storedID string
	if err := json.Unmarshal(rawID, &storedID); err != nil || storedID != expected {
		return fmt.Errorf("%s document ID does not match its selected path", kind)
	}
	return nil
}

func readQualifiedBoards(reader *qualifiedProjectReader) (datatug.Boards, error) {
	entries, err := reader.dirEntries(storage.BoardsFolder)
	if err != nil {
		return nil, err
	}
	ids := itemIDs(entries, storage.BoardFileSuffix)
	if len(ids) > maxQualifiedProjectItems {
		return nil, fmt.Errorf("board list exceeds %d items", maxQualifiedProjectItems)
	}
	sort.Strings(ids)
	boards := make(datatug.Boards, 0, len(ids))
	for _, id := range ids {
		board, err := reader.readQualifiedBoard(id)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && isDirectoryEntry(entries, id) {
				continue
			}
			return nil, err
		}
		boards = append(boards, board)
	}
	return boards, nil
}

type qualifiedEnvironmentData struct {
	env          *datatug.Environment
	presentation *LocalEnvironmentPresentation
}

func readQualifiedEnvironments(reader *qualifiedProjectReader) ([]qualifiedEnvironmentData, error) {
	entries, err := reader.dirEntries(storage.EnvironmentsFolder)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	if len(ids) > maxQualifiedProjectItems {
		return nil, fmt.Errorf("environment list exceeds %d items", maxQualifiedProjectItems)
	}
	sort.Strings(ids)
	environments := make([]qualifiedEnvironmentData, 0, len(ids))
	for _, id := range ids {
		if err := ValidateIdentifier("environment", id); err != nil {
			return nil, err
		}
		env, selected, err := readQualifiedEnvironment(reader, id)
		if err != nil {
			return nil, err
		}
		presentation, err := parseEnvironmentPresentation(selected)
		if err != nil {
			return nil, err
		}
		environments = append(environments, qualifiedEnvironmentData{env: env, presentation: presentation})
	}
	return environments, nil
}

func readQualifiedEnvironment(reader *qualifiedProjectReader, id string) (*datatug.Environment, []byte, error) {
	base := path.Join(storage.EnvironmentsFolder, id)
	current, err := readQualifiedOptionalFileLimit(reader, path.Join(base, envFileName(id)), maxEnvironmentDocumentBytes)
	if err != nil {
		return nil, nil, err
	}
	legacy, err := readQualifiedOptionalFileLimit(reader, path.Join(base, storage.EnvironmentSummaryFileName), maxEnvironmentDocumentBytes)
	if err != nil {
		return nil, nil, err
	}
	env := &datatug.Environment{}
	var selected []byte
	if current != nil {
		if err := validateQualifiedStoredID(current, id, "environment"); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(current, env); err != nil {
			return nil, nil, err
		}
		selected = current
	} else if legacy != nil {
		if err := validateQualifiedStoredID(legacy, id, "environment"); err != nil {
			return nil, nil, err
		}
		var summary datatug.EnvironmentSummary
		if err := json.Unmarshal(legacy, &summary); err != nil {
			return nil, nil, err
		}
		env.ProjectItem = summary.ProjectItem
		env.DbServers = summary.Servers
		selected = legacy
	}
	env.SetID(id)
	return env, selected, nil
}
