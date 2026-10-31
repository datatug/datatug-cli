// Package localprojects manages projects inside an operator-selected creation
// root. HTTP callers supply project identities, never filesystem locations.
package localprojects

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/gofrs/flock"
)

var ErrRootRequired = errors.New("start the agent with --projects-root and --allow-writes to create local projects")
var ErrConflict = errors.New("local project operation conflicts with an existing project or operation")
var ErrInvalid = errors.New("invalid local project request")

var operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const stateName = ".datatug-project-state.json"
const maxMetadata = 1 << 20

type Project struct {
	datatug.ProjectSummary
	Archived bool `json:"archived"`
}

func (p Project) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(p.ProjectSummary)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	delete(fields, "localCreation")
	fields["archived"], _ = json.Marshal(p.Archived)
	return json.Marshal(fields)
}

func (p *Project) UnmarshalJSON(body []byte) error {
	if err := json.Unmarshal(body, &p.ProjectSummary); err != nil {
		return err
	}
	var flags struct {
		Archived bool `json:"archived"`
	}
	if err := json.Unmarshal(body, &flags); err != nil {
		return err
	}
	p.Archived = flags.Archived
	return nil
}

type Create struct {
	Store       string `json:"store"`
	ID          string `json:"id"`
	Title       string `json:"title"`
	OperationID string `json:"operationId"`
}

type Archive struct {
	Storage     string `json:"storage"`
	Project     string `json:"project"`
	Archived    bool   `json:"archived"`
	OperationID string `json:"operationId"`
}

type ArchiveResult struct {
	OperationID string  `json:"operationId"`
	Project     Project `json:"project"`
}

type creation struct {
	OperationID string `json:"operationId"`
	Digest      string `json:"digest"`
}

type projectDocument struct {
	datatug.ProjectFile
	LocalCreation *creation `json:"localCreation,omitempty"`
}

func (p projectDocument) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(p.ProjectFile)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	if p.LocalCreation != nil {
		fields["localCreation"], err = json.Marshal(p.LocalCreation)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func (p *projectDocument) UnmarshalJSON(body []byte) error {
	if err := json.Unmarshal(body, &p.ProjectFile); err != nil {
		return err
	}
	var metadata struct {
		LocalCreation *creation `json:"localCreation"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return err
	}
	p.LocalCreation = metadata.LocalCreation
	return nil
}

type archiveReceipt struct {
	Archived bool `json:"archived"`
}

type projectState struct {
	Version    int                       `json:"version"`
	Archived   bool                      `json:"archived"`
	Operations map[string]archiveReceipt `json:"operations"`
}

type Registry struct {
	mu   sync.Mutex
	root string
	dirs map[string]string
}

// Open loads only exact configured projects and immediate child projects of the
// operator-approved root. It does not modify user-wide CLI configuration.
func Open(root string, configured map[string]string) (*Registry, error) {
	r := &Registry{dirs: make(map[string]string, len(configured))}
	for id, dir := range configured {
		r.dirs[id] = dir
	}
	if root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, err
		}
		r.root, err = filepath.Abs(resolved)
		if err != nil {
			return nil, err
		}
		if err = r.discover(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) CanCreate() bool { return r != nil && r.root != "" }

func (r *Registry) Paths() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.dirs))
	for id, dir := range r.dirs {
		out[id] = dir
	}
	return out
}

func (r *Registry) discover() error {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || dto.ValidateProjectID(entry.Name()) != nil {
			continue
		}
		dir := filepath.Join(r.root, entry.Name())
		file, err := readProject(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue // An unrelated directory is not a project.
		}
		if err != nil || file.ID != entry.Name() {
			return fmt.Errorf("invalid project metadata in creation root")
		}
		if old, ok := r.dirs[file.ID]; ok && filepath.Clean(old) != dir {
			return ErrConflict
		}
		r.dirs[file.ID] = dir
	}
	return nil
}

func (r *Registry) List() ([]Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.root != "" {
		if err := r.discover(); err != nil {
			return nil, err
		}
	}
	projects := make([]Project, 0, len(r.dirs))
	for id, dir := range r.dirs {
		project, err := readProjectState(id, dir)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	return projects, nil
}

func validateOperation(operation string) bool { return operationPattern.MatchString(operation) }

func (r *Registry) Create(ctx context.Context, request Create, authorize ...func() error) (Project, error) {
	var out Project
	if !r.CanCreate() {
		return out, ErrRootRequired
	}
	if request.Store != "local" || dto.ValidateProjectID(request.ID) != nil || !validateOperation(request.OperationID) ||
		request.Title != strings.TrimSpace(request.Title) || len(request.Title) < 2 || len(request.Title) > 128 || strings.ContainsAny(request.Title, "\x00\r\n") {
		return out, ErrInvalid
	}
	digest := sha256.Sum256([]byte(request.ID + "\x00" + request.Title))
	wantCreation := creation{OperationID: request.OperationID, Digest: hex.EncodeToString(digest[:])}
	r.mu.Lock()
	defer r.mu.Unlock()
	err := withLock(ctx, r.root, ".datatug-create.lock", func() error {
		if err := checkAuthority(authorize); err != nil {
			return err
		}
		if err := r.discover(); err != nil {
			return err
		}
		// The same command cannot silently create another project after retry.
		for id, dir := range r.dirs {
			file, err := readProject(dir)
			if err != nil {
				return err
			}
			if file.LocalCreation != nil && file.LocalCreation.OperationID == request.OperationID {
				if *file.LocalCreation != wantCreation || id != request.ID {
					return ErrConflict
				}
				out, err = readProjectState(id, dir)
				return err
			}
		}
		if _, exists := r.dirs[request.ID]; exists {
			return ErrConflict
		}
		root, err := os.OpenRoot(r.root)
		if err != nil {
			return err
		}
		defer root.Close()
		if _, err = root.Lstat(request.ID); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return ErrConflict
			}
			return err
		}
		// A complete project is staged before it becomes discoverable.
		staging, err := temporaryName(".datatug-create-")
		if err != nil {
			return err
		}
		if err = root.Mkdir(staging, 0700); err != nil {
			return err
		}
		defer root.RemoveAll(staging)
		stagingRoot, err := root.OpenRoot(staging)
		if err != nil {
			return err
		}
		defer stagingRoot.Close()
		file := projectDocument{ProjectFile: datatug.ProjectFile{
			ProjectItem:      datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: request.ID, Title: request.Title}, Access: "private"},
			Created:          &datatug.ProjectCreated{At: time.Now().UTC()},
			EnvironmentOrder: []string{"dev"},
		}, LocalCreation: &wantCreation}
		if err = file.ProjectFile.Validate(); err != nil {
			return err
		}
		if err = writeJSONRoot(stagingRoot, storage.ProjectSummaryFileName, file); err != nil {
			return err
		}
		// Initial files share the whole-project staging transaction. Existing
		// environment mutation journals are bound to a stable project path and
		// must not be created in a directory that is about to be published.
		if err = stagingRoot.Mkdir(storage.EnvironmentsFolder, 0700); err != nil {
			return err
		}
		envDir := filepath.Join(storage.EnvironmentsFolder, "dev")
		if err = stagingRoot.Mkdir(envDir, 0700); err != nil {
			return err
		}
		envRoot, err := stagingRoot.OpenRoot(envDir)
		if err != nil {
			return err
		}
		err = writeJSONRoot(envRoot, storage.JsonFileName("dev", storage.EnvFileSuffix), datatug.Environment{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "dev", Title: "Development"}, Access: "private"}})
		_ = envRoot.Close()
		if err != nil {
			return err
		}
		environmentsRoot, err := stagingRoot.OpenRoot(storage.EnvironmentsFolder)
		if err != nil {
			return err
		}
		err = syncRootDirectory(environmentsRoot)
		_ = environmentsRoot.Close()
		if err != nil {
			return err
		}
		if err = syncRootDirectory(stagingRoot); err != nil {
			return err
		}
		if err = checkAuthority(authorize); err != nil {
			return err
		}
		if err = root.Rename(staging, request.ID); err != nil {
			return err
		}
		dir := filepath.Join(r.root, request.ID)
		r.dirs[request.ID] = dir
		out = Project{ProjectSummary: datatug.ProjectSummary{ProjectFile: file.ProjectFile}}
		return syncRootDirectory(root)
	})
	return out, err
}

func (r *Registry) SetArchived(ctx context.Context, request Archive, authorize ...func() error) (ArchiveResult, error) {
	var out ArchiveResult
	if request.Storage != "local" || request.Project == "" || len(request.Project) > 512 || !validateOperation(request.OperationID) {
		return out, ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	dir, ok := r.dirs[request.Project]
	if !ok {
		return out, ErrConflict
	}
	err := withLock(ctx, dir, ".datatug-project-state.lock", func() error {
		if err := checkAuthority(authorize); err != nil {
			return err
		}
		project, err := readProjectState(request.Project, dir)
		if err != nil {
			return err
		}
		state, err := readState(dir)
		if err != nil {
			return err
		}
		if previous, exists := state.Operations[request.OperationID]; exists {
			if previous.Archived != request.Archived {
				return ErrConflict
			}
			// Replay reports the original acknowledgement, never undoes a later restore.
			project.Archived = previous.Archived
			out = ArchiveResult{OperationID: request.OperationID, Project: project}
			return nil
		}
		if len(state.Operations) >= 10000 {
			return ErrConflict // Never discard receipts that make retries safe.
		}
		state.Archived = request.Archived
		state.Operations[request.OperationID] = archiveReceipt{Archived: request.Archived}
		if err = checkAuthority(authorize); err != nil {
			return err
		}
		if err = writeJSON(dir, stateName, state); err != nil {
			return err
		}
		project.Archived = state.Archived
		out = ArchiveResult{OperationID: request.OperationID, Project: project}
		return nil
	})
	return out, err
}

func checkAuthority(checks []func() error) error {
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func readProjectState(id, dir string) (Project, error) {
	file, err := readProject(dir)
	if err != nil {
		return Project{}, ErrConflict
	}
	// The registered identity is authoritative, including existing single-project
	// aliases. It is never interpreted as a path or written back into metadata.
	file.ID = id
	state, err := readState(dir)
	return Project{ProjectSummary: datatug.ProjectSummary{ProjectFile: file.ProjectFile}, Archived: state.Archived}, err
}

func readProject(dir string) (projectDocument, error) {
	var file projectDocument
	if err := readJSON(dir, storage.ProjectSummaryFileName, &file); err != nil {
		return file, err
	}
	return file, file.ProjectFile.Validate()
}

func readState(dir string) (projectState, error) {
	state := projectState{Version: 1, Operations: map[string]archiveReceipt{}}
	err := readJSON(dir, stateName, &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err == nil && (state.Version != 1 || state.Operations == nil || len(state.Operations) > 10000) {
		err = ErrConflict
	}
	return state, err
}

func readJSON(dir, name string, value any) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxMetadata {
		return ErrConflict
	}
	file, err := root.OpenFile(name, os.O_RDONLY|noFollowFlags, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxMetadata+1))
	if err != nil || len(body) > maxMetadata {
		return ErrConflict
	}
	return json.Unmarshal(body, value)
}

func writeJSON(dir, name string, value any) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeJSONRoot(root, name, value)
}

func writeJSONRoot(root *os.Root, name string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > maxMetadata {
		return ErrConflict
	}
	temp, err := temporaryName(".datatug-metadata-")
	if err != nil {
		return err
	}
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	return syncRootDirectory(root)
}

func temporaryName(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(bytes[:]), nil
}

func withLock(ctx context.Context, dir, name string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, name)
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return ErrConflict
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock := flock.New(lockPath, flock.SetFlag(os.O_CREATE|os.O_RDWR|noFollowFlags), flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil || !locked {
		return errors.Join(err, ctx.Err())
	}
	defer lock.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func syncRootDirectory(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
