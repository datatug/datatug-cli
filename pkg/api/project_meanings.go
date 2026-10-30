package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/gofrs/flock"
	"github.com/strongo/validation"
)

// Filesystem seams exercise interruption and I/O failures at the authoritative boundary.
var meaningOpenRoot = os.OpenRoot
var meaningLstat = (*os.Root).Lstat
var meaningOpen = (*os.Root).Open
var meaningOpenFile = (*os.Root).OpenFile
var meaningRename = (*os.Root).Rename
var meaningReadAll = io.ReadAll
var meaningWalkDir = fs.WalkDir
var meaningReadDir = (*os.File).ReadDir
var meaningMarshalIndent = json.MarshalIndent

const localMeaningsFile = "datatug-meanings.json"

type LocalMeaningWrite struct {
	dto.ProjectRef
	Branch string `json:"branch,omitempty"`
	datatug.ProjectMeaningWrite
}

func authorizeMeaningWrite(ctx context.Context, project string) error {
	secureMu.RLock()
	configured := securityContextID != ""
	caps := capabilities
	session := secureSession
	secureMu.RUnlock()
	if !configured || !caps.AllowWrites {
		return secureread.ErrAccessDenied
	}
	key := record.NewKeyWithParentAndID(record.NewKeyWithID(accesspolicies.ProjectsCollection, project), "meanings", "project")
	return accesspolicies.AuthorizeWrite(ctx, accesspolicies.WriteOptions{Principal: session.Principal, Policies: session.Policies, Unrestricted: session.Unrestricted}, access.Set, access.RecordResourceForKey(key))
}

// withLocalMeanings holds the same lock as revisioned query writes. Branches
// other than the checked-out branch and pinned/detached checkouts are refused.
func withLocalMeanings(ctx context.Context, ref dto.ProjectRef, branch string, mutating bool, fn func(*os.Root, datatug.MeaningScope, gitState) error) error {
	return withLocalProjectFiles(ctx, ref, branch, mutating, func(root *os.Root, state gitState) error {
		return fn(root, datatug.MeaningScope{Storage: LocalStoreID, Project: ref.ProjectID, Branch: state.branch}, state)
	})
}

// Shared branch/HEAD and project-file lock boundary. The owning command must
// authorize its own resource before invoking it; it grants no lifecycle coverage.
func withLocalProjectFiles(ctx context.Context, ref dto.ProjectRef, branch string, mutating bool, fn func(*os.Root, gitState) error) error {
	if err := requireLocalReadPrincipal(); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.StoreID != LocalStoreID {
		return ErrUnknownStoreID
	}
	dir, err := servedProjectDir(ref.ProjectID)
	if err != nil {
		return err
	}
	dir, err = filepathEvalSymlinks(dir)
	if err != nil {
		return err
	}
	state, err := localGitStateSeam(ctx, dir)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return err
	}
	lock := flock.New(filepath.Join(state.gitDir, "datatug-project-api.lock"))
	locked, err := flockTryLockContextSeam(lock, ctx, 25*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return context.DeadlineExceeded
	}
	defer func() { _ = lock.Unlock() }()
	state, err = localGitStateSeam(ctx, dir)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return canceled
		}
		return err
	}
	if branch != "" && branch != state.branch {
		return dto.ErrBranchHeadConflict
	}
	root, err := meaningOpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err = fn(root, state); err != nil {
		return err
	}
	after, err := localGitStateSeam(ctx, dir)
	if err != nil || after.branch != state.branch || after.head != state.head {
		if mutating {
			return ErrProjectMutationOutcomeUncertain
		}
		return dto.ErrBranchHeadConflict
	}
	return nil
}
func readLocalMeanings(root *os.Root) (datatug.ProjectMeaningsDocument, error) {
	d := datatug.ProjectMeaningsDocument{Version: 1, Entities: []datatug.MeaningEntity{}}
	info, err := meaningLstat(root, localMeaningsFile)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return d, datatug.ErrMeaningIncomplete
	}
	f, err := meaningOpen(root, localMeaningsFile)
	if err != nil {
		return d, err
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(io.LimitReader(f, 4<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&d); err != nil {
		return d, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return d, datatug.ErrMeaningIncomplete
	}
	return d, d.Validate()
}
func scanLocalMeaningSources(root *os.Root) ([]datatug.MeaningQuerySource, []datatug.MeaningBoardSource, error) {
	envs, err := readLocalMeaningEnvironments(root)
	if err != nil {
		return nil, nil, err
	}
	queries := []datatug.MeaningQuerySource{}
	boards := []datatug.MeaningBoardSource{}
	boardDefinitions := map[string]datatug.Board{}
	count := 0
	for _, dir := range []string{"queries", "boards"} {
		if info, err := meaningLstat(root, dir); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, nil, err
		} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, datatug.ErrMeaningIncomplete
		}
		err := meaningWalkDir(root.FS(), dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return datatug.ErrMeaningIncomplete
			}
			if entry.IsDir() {
				if dir == "queries" && entry.Name() == ".dt-query-txn" {
					return fs.SkipDir
				}
				return nil
			}
			relative := strings.TrimPrefix(path, dir+"/")
			id := ""
			legacySQL := false
			if dir == "queries" {
				if strings.HasSuffix(relative, ".query.json") {
					id = strings.TrimSuffix(relative, ".query.json")
				} else if strings.HasSuffix(relative, ".sql.json") {
					legacySQL = true
				} else {
					return nil
				}
			} else {
				parts := strings.Split(relative, "/")
				if len(parts) == 1 && strings.HasSuffix(relative, ".board.json") {
					id = strings.TrimSuffix(relative, ".board.json")
				} else if len(parts) == 2 && parts[1] == "board.json" {
					id = parts[0]
				} else {
					return nil
				}
			}
			count++
			if count > 10000 {
				return datatug.ErrMeaningIncomplete
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > 2<<20 {
				return datatug.ErrMeaningIncomplete
			}
			f, err := meaningOpen(root, path)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			data, err := meaningReadAll(io.LimitReader(f, 2<<20+1))
			if err != nil || len(data) > 2<<20 {
				return datatug.ErrMeaningIncomplete
			}
			if dir == "queries" {
				var q datatug.QueryDef
				if err = json.Unmarshal(data, &q); err != nil {
					return err
				}
				if legacySQL {
					if q.SemanticModel != nil {
						return datatug.ErrMeaningIncomplete
					}
					return nil
				}
				queries = append(queries, datatug.MeaningQuerySource{ID: id, Title: q.Title, SemanticModel: q.SemanticModel, Databases: datatug.MeaningDatabasesForQueryTargets(q, envs)})
			} else {
				var b datatug.Board
				if err = json.Unmarshal(data, &b); err != nil {
					return err
				}
				if previous, exists := boardDefinitions[id]; exists && !reflect.DeepEqual(previous, b) {
					return datatug.ErrMeaningIncomplete
				}
				boardDefinitions[id] = b
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	for id, b := range boardDefinitions {
		b.ID = id
		source, err := datatug.MeaningBoardSourceFromBoard(b)
		if err != nil {
			return nil, nil, err
		}
		boards = append(boards, source)
	}
	return queries, boards, nil
}
func writeLocalMeanings(root *os.Root, d datatug.ProjectMeaningsDocument) error {
	data, err := meaningMarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("meanings size limit exceeded")
	}
	temp := ".datatug-meanings-" + randomMeaningSuffix() + ".tmp"
	f, err := meaningOpenFile(root, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(temp) }()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = meaningRename(root, temp, localMeaningsFile); err != nil {
		return err
	}
	dir, err := meaningOpen(root, ".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
func randomMeaningSuffix() string {
	return strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
}
func LocalProjectMeaningsCatalog(ctx context.Context, ref dto.ProjectRef, branch string) (datatug.ProjectMeaningsCatalog, error) {
	var result datatug.ProjectMeaningsCatalog
	err := withLocalMeanings(ctx, ref, branch, false, func(root *os.Root, scope datatug.MeaningScope, _ gitState) error {
		d, err := readLocalMeanings(root)
		if err != nil {
			return err
		}
		q, b, err := scanLocalMeaningSources(root)
		if err != nil {
			return err
		}
		result, err = datatug.BuildProjectMeaningsCatalog(d, q, b, scope, authorizeMeaningWrite(ctx, ref.ProjectID) == nil, true)
		return err
	})
	return result, err
}
func LocalProjectMeaningChange(ctx context.Context, r LocalMeaningWrite) (datatug.ProjectMeaningAck, error) {
	var result datatug.ProjectMeaningAck
	if r.Branch == "" {
		return result, dto.ErrUnsupportedCapability
	}
	if err := authorizeMeaningWrite(ctx, r.ProjectID); err != nil {
		return result, err
	}
	err := withLocalMeanings(ctx, r.ProjectRef, r.Branch, true, func(root *os.Root, scope datatug.MeaningScope, state gitState) error {
		d, err := readLocalMeanings(root)
		if err != nil {
			return err
		}
		q, b, err := scanLocalMeaningSources(root)
		if err != nil {
			return err
		}
		catalog, err := datatug.BuildProjectMeaningsCatalog(d, q, b, scope, true, true)
		if err != nil {
			return err
		}
		next, ack, err := datatug.ApplyProjectMeaningChange(d, catalog, r.ProjectMeaningWrite)
		if err != nil {
			return err
		}
		nextCatalog, err := datatug.BuildProjectMeaningsCatalog(next, q, b, scope, true, true)
		if err != nil {
			return err
		}
		before, err := localGitStateSeam(ctx, root.Name())
		if err != nil || before.branch != state.branch || before.head != state.head {
			return dto.ErrBranchHeadConflict
		}
		if err = writeLocalMeanings(root, next); err != nil {
			return errors.Join(ErrProjectMutationOutcomeUncertain, err)
		}
		if err = stageLocalMeaningDocument(ctx, root, state, next); err != nil {
			return errors.Join(ErrProjectMutationOutcomeUncertain, err)
		}
		ack.Catalog = nextCatalog
		result = ack
		return nil
	})
	return result, err
}

// Called only while the revisioned SaveQuery holds datatug-project-api.lock.
func validateLocalQueryMeaningReferences(projectDir string, scope datatug.MeaningScope, model *datatug.QuerySemanticModel) error {
	if model == nil {
		return nil
	}
	root, err := meaningOpenRoot(projectDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	d, err := readLocalMeanings(root)
	if err != nil {
		return err
	}
	if err = datatug.ValidateProjectMeaningReferences(model, d, scope); err != nil {
		return validation.NewBadRequestError(err)
	}
	return nil
}
func hasProjectMeaningRefs(model *datatug.QuerySemanticModel) bool {
	if model == nil {
		return false
	}
	for _, c := range model.Meaning.Concepts {
		if c.MeaningRef != nil {
			return true
		}
	}
	return false
}

func readLocalMeaningEnvironments(root *os.Root) (datatug.Environments, error) {
	envs := datatug.Environments{}
	info, err := meaningLstat(root, "environments")
	if errors.Is(err, os.ErrNotExist) {
		return envs, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, datatug.ErrMeaningIncomplete
	}
	dir, err := meaningOpen(root, "environments")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := meaningReadDir(dir, 1001)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 1000 {
		return nil, datatug.ErrMeaningIncomplete
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, datatug.ErrMeaningIncomplete
		}
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		var selected *datatug.Environment
		for _, name := range []string{id + ".env.json", "environment-summary.json"} {
			path := filepath.ToSlash(filepath.Join("environments", id, name))
			stat, err := meaningLstat(root, path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !stat.Mode().IsRegular() || stat.Size() > 2<<20 {
				return nil, datatug.ErrMeaningIncomplete
			}
			f, err := meaningOpen(root, path)
			if err != nil {
				return nil, err
			}
			var env datatug.Environment
			decoder := json.NewDecoder(io.LimitReader(f, 2<<20))
			err = decoder.Decode(&env)
			if err == nil {
				if tailErr := decoder.Decode(new(any)); tailErr != io.EOF {
					err = datatug.ErrMeaningIncomplete
				}
			}
			_ = f.Close()
			if err != nil {
				return nil, err
			}
			env.ID = id
			if selected != nil {
				a, _ := json.Marshal(selected)
				b, _ := json.Marshal(env)
				if string(a) != string(b) {
					return nil, datatug.ErrMeaningIncomplete
				}
			}
			selected = &env
		}
		if selected != nil {
			envs = append(envs, selected)
		}
	}
	return envs, nil
}

func stageLocalMeaningDocument(ctx context.Context, root *os.Root, state gitState, d datatug.ProjectMeaningsDocument) error {
	current, err := localGitStateSeam(ctx, root.Name())
	if err != nil || current.branch != state.branch || current.head != state.head {
		return ErrProjectMutationOutcomeUncertain
	}
	data, err := meaningMarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	rel, err := filepathRel(state.root, root.Name())
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrProjectMutationOutcomeUncertain
	}
	path := filepath.ToSlash(filepath.Join(rel, localMeaningsFile))
	oid, err := localGitInputSeam(ctx, state.root, data, "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	if _, err = localGitOutputSeam(ctx, state.root, "update-index", "--add", "--cacheinfo", "100644", oid, path); err != nil {
		return err
	}
	onDisk, err := fs.ReadFile(root.FS(), localMeaningsFile)
	if err != nil || !bytes.Equal(onDisk, data) {
		return ErrProjectMutationOutcomeUncertain
	}
	return nil
}
