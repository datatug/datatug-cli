package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/strongo/validation"
)

func setupTestGitProject(t *testing.T, projectID string) (string, func(args ...string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"),
		[]byte(fmt.Sprintf(`{"id":%q,"title":"Test Project","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`, projectID)), 0o600))
	git("add", "-A")
	git("commit", "-q", "-m", "initial commit")
	return dir, git
}

// Not parallel: mutates secure session, seams, and git state.
func TestCoverageFinish100_ProjectQueryAdapter_BasicsAndCapabilities(t *testing.T) {
	ctx := context.Background()
	const pID = "proj-adapter-basics"
	dir, git := setupTestGitProject(t, pID)
	paths := map[string]string{pID: dir}

	// 1. RequireLocalReadPrincipal unauthenticated
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	assert.ErrorIs(t, RequireLocalReadPrincipal(), secureread.ErrAccessDenied)

	// 2. SecureProjectMutationScope unconfigured
	_, err := SecureProjectMutationScope(dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, "main", "save-query")
	assert.Error(t, err)

	// Configure valid session
	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	// RequireLocalReadPrincipal authenticated
	assert.NoError(t, RequireLocalReadPrincipal())

	// SecureProjectMutationScope configured
	scope, err := SecureProjectMutationScope(dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, "main", "save-query")
	assert.NoError(t, err)
	assert.Equal(t, "alice", scope.ActorID)
	assert.Equal(t, pID, scope.ProjectID)

	adapter := LocalProjectQueryAdapter{}

	// Capabilities:
	// - Bad store ID
	_, err = adapter.Capabilities(ctx, dto.ProjectRef{StoreID: "other", ProjectID: pID})
	assert.ErrorIs(t, err, ErrUnknownStoreID)

	// - Unknown project
	_, err = adapter.Capabilities(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "nonexistent"})
	assert.Error(t, err)

	// - Success
	caps, err := adapter.Capabilities(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.NoError(t, err)
	assert.True(t, caps.QueryRead)
	assert.True(t, caps.QuerySave)
	assert.True(t, caps.SemanticModel)

	// Capabilities unauthenticated
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, err = adapter.Capabilities(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.Error(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})

	_ = git
}

// Not parallel: mutates secure session, seams, and git state.
func TestCoverageFinish100_ProjectQueryAdapter_Branches(t *testing.T) {
	ctx := context.Background()
	const pID = "proj-adapter-branches"
	dir, _ := setupTestGitProject(t, pID)
	paths := map[string]string{pID: dir}

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	// 1. Unauthenticated
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.Error(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})

	// 2. Validate error
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{})
	assert.Error(t, err)

	// 3. Bad store ID
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: "bad", ProjectID: pID})
	assert.ErrorIs(t, err, ErrUnknownStoreID)

	// 4. Unknown project
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "unknown"})
	assert.Error(t, err)

	// 5. localGitState error (non-git dir)
	nonGitDir := t.TempDir()
	ConfigureSecureSession(session, map[string]string{"nongit": nonGitDir}, Capabilities{})
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "nongit"})
	assert.Error(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})

	// 6. Lock busy/timeout & errors
	origFlock := flockTryLockContextSeam
	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) { return false, nil }
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) {
		return false, errors.New("lock error")
	}
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.Error(t, err)
	flockTryLockContextSeam = origFlock

	// localGitState error after lock
	origGitState := localGitStateSeam
	callCount := 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		if callCount == 2 {
			return gitState{}, errors.New("state error after lock")
		}
		return origGitState(c, d)
	}
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.Error(t, err)
	localGitStateSeam = origGitState

	// 7. Success path with remote default branch
	origGitOutput := localGitOutputSeam
	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "symbolic-ref" {
			return "origin/main", nil
		}
		return origGitOutput(c, d, args...)
	}
	branches, err := LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.NoError(t, err)
	assert.NotEmpty(t, branches.Branches)
	assert.NotEmpty(t, branches.CurrentBranch)
	assert.Equal(t, "main", branches.DefaultBranch)
	localGitOutputSeam = origGitOutput

	// 8. Output parsing error (> 1000 branches or bad line)
	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "for-each-ref" {
			return "bad_format_no_null_char", nil
		}
		return origGitOutput(c, d, args...)
	}
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, dto.ErrUnsupportedCapability)
	localGitOutputSeam = origGitOutput

	// 9. for-each-ref command error
	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "for-each-ref" {
			return "", errors.New("git error")
		}
		return origGitOutput(c, d, args...)
	}
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.Error(t, err)
	localGitOutputSeam = origGitOutput

	// 10. afterRead conflict
	callCount = 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		st, err := origGitState(c, d)
		if callCount == 3 {
			st.head = "different_head"
		}
		return st, err
	}
	_, err = LocalProjectBranches(ctx, dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID})
	assert.ErrorIs(t, err, dto.ErrBranchHeadConflict)
	localGitStateSeam = origGitState
}

// Not parallel: mutates secure session, seams, and git state.
func TestCoverageFinish100_ProjectQueryAdapter_GetQuery(t *testing.T) {
	ctx := context.Background()
	const pID = "proj-adapter-getquery"
	dir, git := setupTestGitProject(t, pID)
	paths := map[string]string{pID: dir}

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	branch := git("symbolic-ref", "--short", "HEAD")
	var head string

	// Store setup
	filestore.SetProjectPath(pID, dir)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	store := filestore.NewProjectStore(pID, dir).(datatug.RevisionedQueriesStore)

	query := datatug.QueryDefWithFolderPath{}
	query.ID, query.Title, query.Type, query.Text = "q1", "Q1 Title", datatug.QueryTypeSQL, "SELECT 1"
	_, err = store.PutQuery(ctx, &query, datatug.QueryWriteCondition{IfNoneMatch: true})
	require.NoError(t, err)
	git("add", "-A")
	git("commit", "-q", "-m", "added q1")
	head = git("rev-parse", "HEAD")

	adapter := LocalProjectQueryAdapter{}

	// 1. Unauthenticated
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.Error(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})

	// 2. Validate error
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{})
	assert.Error(t, err)

	// 3. Bad store ID
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: "bad", ProjectID: pID}, ID: "q1", Branch: branch})
	assert.ErrorIs(t, err, ErrUnknownStoreID)

	// 4. Bad query path (e.g. "../escaped")
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "../q1", Branch: branch})
	assert.Error(t, err)

	// 5. Unknown project
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "unknown"}, ID: "q1", Branch: branch})
	assert.Error(t, err)

	// 6. Branch mismatch
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: "wrong-branch"})
	assert.ErrorIs(t, err, dto.ErrBranchHeadConflict)

	// 7. Lock conflict & errors
	origFlock := flockTryLockContextSeam
	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) { return false, nil }
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) {
		return false, errors.New("lock error")
	}
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.Error(t, err)
	flockTryLockContextSeam = origFlock

	// localGitState error before lock
	origGitState := localGitStateSeam
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		return gitState{}, errors.New("state error before lock")
	}
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.Error(t, err)
	localGitStateSeam = origGitState

	// localGitState error after lock
	callCount := 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		if callCount == 2 {
			return gitState{}, errors.New("state error after lock")
		}
		return origGitState(c, d)
	}
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.Error(t, err)
	localGitStateSeam = origGitState

	// projectStoreForID error
	storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, errors.New("store err") }
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.Error(t, err)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 8. Store not implementing RevisionedQueriesStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return mockNoRevStore{}, nil }
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.ErrorIs(t, err, dto.ErrUnsupportedCapability)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 9. Success path
	resp, err := adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.NoError(t, err)
	assert.Equal(t, "q1", resp.Query.ID)
	assert.Equal(t, datatug.RootSharedFolderName, resp.Query.FolderPath)
	assert.Equal(t, head, resp.BranchHead)

	// 10. LoadQueryRevision error
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "nonexistent_query", Branch: branch})
	assert.Error(t, err)

	// 11. afterRead conflict
	callCount = 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		st, err := origGitState(c, d)
		if callCount == 3 {
			st.head = "different_head"
		}
		return st, err
	}
	_, err = adapter.GetQuery(ctx, dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID}, ID: "q1", Branch: branch})
	assert.ErrorIs(t, err, dto.ErrBranchHeadConflict)
	localGitStateSeam = origGitState
}

// Not parallel: mutates secure session, seams, and git state.
func TestCoverageFinish100_ProjectQueryAdapter_SaveQuery(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	ctx := context.Background()
	const pID = "proj-adapter-savequery"
	dir, git := setupTestGitProject(t, pID)
	paths := map[string]string{pID: dir}

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	branch := git("symbolic-ref", "--short", "HEAD")
	head := git("rev-parse", "HEAD")

	filestore.SetProjectPath(pID, dir)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	adapter := LocalProjectQueryAdapter{}

	req := dto.SaveQueryRequest{
		OperationID:        "op-save-1",
		ProjectRef:         dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID},
		Branch:             branch,
		ExpectedBranchHead: head,
		IfNoneMatch:        true,
		Query: datatug.QueryDefWithFolderPath{
			FolderPath: datatug.RootSharedFolderName,
			QueryDef: datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{
						ID:    "save_q1",
						Title: "Save Q1",
					},
				},
				Type: datatug.QueryTypeSQL,
				Text: "SELECT 42",
			},
		},
	}
	scope, err := SecureProjectMutationScope(req.ProjectRef, req.Branch, "save-query")
	require.NoError(t, err)

	// 1. Bad request validation
	_, err = adapter.SaveQuery(ctx, scope, dto.SaveQueryRequest{})
	assert.Error(t, err)

	// 2. Bad store ID
	badStoreReq := req
	badStoreReq.StoreID = "bad"
	_, err = adapter.SaveQuery(ctx, scope, badStoreReq)
	assert.ErrorIs(t, err, ErrUnknownStoreID)

	// 3. Scope mismatch
	badScope := scope
	badScope.ActorID = "wrong_actor"
	_, err = adapter.SaveQuery(ctx, badScope, req)
	assert.Error(t, err)

	// 4. Credential reason found in query
	credReq := req
	credReq.Query.Text = "SELECT * FROM db WHERE password='s3cr3t;user=alice'"
	credReq.OperationID = "op-cred"
	credScope, _ := SecureProjectMutationScope(credReq.ProjectRef, credReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, credScope, credReq)
	assert.Error(t, err)

	// 5. Successful SaveQuery (IfNoneMatch: true)
	saved, err := adapter.SaveQuery(ctx, scope, req)
	assert.NoError(t, err)
	assert.NotEmpty(t, saved.Revision)

	// 6. Idempotent replay of completed journal
	replayed, err := adapter.SaveQuery(ctx, scope, req)
	assert.NoError(t, err)
	assert.Equal(t, saved.Revision, replayed.Revision)

	// 7. Staged query pair verified
	staged := git("diff", "--cached", "--name-only")
	assert.Contains(t, staged, "queries/save_q1.query.json")
	assert.Contains(t, staged, "queries/save_q1.query.sql")

	// Commit staged files to move head forward
	git("commit", "-q", "-m", "saved query 1")
	newHead := git("rev-parse", "HEAD")

	// 8. Update query (IfMatch)
	updateReq := req
	updateReq.OperationID = "op-save-2"
	updateReq.ExpectedBranchHead = newHead
	updateReq.IfNoneMatch = false
	updateReq.IfMatch = saved.Revision
	updateReq.Query.Text = "SELECT 43"
	updateScope, err := SecureProjectMutationScope(updateReq.ProjectRef, updateReq.Branch, "save-query")
	require.NoError(t, err)

	updated, err := adapter.SaveQuery(ctx, updateScope, updateReq)
	assert.NoError(t, err)
	assert.NotEqual(t, saved.Revision, updated.Revision)

	// 9. Revision conflict
	conflictReq := updateReq
	conflictReq.OperationID = "op-conflict"
	conflictReq.IfMatch = "wrong_rev"
	conflictScope, _ := SecureProjectMutationScope(conflictReq.ProjectRef, conflictReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, conflictScope, conflictReq)
	assert.ErrorIs(t, err, dto.ErrQueryRevisionConflict)

	// 10. Client capture refusal on IfNoneMatch
	capReq := req
	capReq.OperationID = "op-cap"
	capReq.Query.ID = "q_cap"
	capReq.Query.Capture = &datatug.QueryCapture{Author: "mallory"}
	capScope, _ := SecureProjectMutationScope(capReq.ProjectRef, capReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, capScope, capReq)
	assert.Error(t, err)

	// 11. Branch head conflict
	headConflictReq := updateReq
	headConflictReq.OperationID = "op-head-conflict"
	headConflictReq.ExpectedBranchHead = "0000000000000000000000000000000000000000"
	headScope, _ := SecureProjectMutationScope(headConflictReq.ProjectRef, headConflictReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, headScope, headConflictReq)
	assert.ErrorIs(t, err, dto.ErrBranchHeadConflict)
}

// Not parallel: tests internal helper functions of project_query_adapter.
func TestCoverageFinish100_ProjectQueryAdapter_Helpers(t *testing.T) {
	ctx := context.Background()
	const pID = "proj-adapter-helpers"
	dir, git := setupTestGitProject(t, pID)

	// 1. localGitOutput and localGitInput
	out, err := localGitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	assert.NoError(t, err)
	assert.NotEmpty(t, out)

	_, err = localGitOutput(ctx, dir, "invalid-git-command")
	assert.Error(t, err)

	oid, err := localGitInput(ctx, dir, []byte("hello git input"), "hash-object", "-w", "--stdin")
	assert.NoError(t, err)
	assert.NotEmpty(t, oid)

	_, err = localGitInput(ctx, dir, []byte("hello"), "invalid-git-command")
	assert.Error(t, err)

	// 2. localGitState
	st, err := localGitState(ctx, dir)
	assert.NoError(t, err)
	assert.NotEmpty(t, st.root)
	assert.NotEmpty(t, st.branch)
	assert.NotEmpty(t, st.head)

	origGitOut := localGitOutputSeam
	for i := 1; i <= 4; i++ {
		callIdx := 0
		targetIdx := i
		localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
			callIdx++
			if callIdx == targetIdx {
				return "", errors.New("git err")
			}
			return origGitOut(c, d, args...)
		}
		_, err := localGitState(ctx, dir)
		assert.Error(t, err)
	}
	localGitOutputSeam = origGitOut

	// 3. previewLocalQueryRevision
	query := datatug.QueryDefWithFolderPath{
		QueryDef: datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{
				ProjItemBrief: datatug.ProjItemBrief{
					ID:    "prev_q",
					Title: "Prev Q",
				},
			},
			Type: datatug.QueryTypeSQL,
			Text: "SELECT 1",
		},
	}
	preview, err := previewLocalQueryRevision(ctx, query)
	assert.NoError(t, err)
	assert.NotEmpty(t, preview.revision)
	assert.NotEmpty(t, preview.metadata)
	assert.NotEmpty(t, preview.body)

	// previewLocalQueryRevision errors
	origMkdirTemp := osMkdirTemp
	osMkdirTemp = func(string, string) (string, error) { return "", errors.New("mkdir err") }
	_, err = previewLocalQueryRevision(ctx, query)
	assert.Error(t, err)
	osMkdirTemp = origMkdirTemp

	badQuery := query
	badQuery.ID = "../bad"
	_, err = previewLocalQueryRevision(ctx, badQuery)
	assert.Error(t, err)

	origReadFile := osReadFile
	osReadFile = func(name string) ([]byte, error) {
		if strings.HasSuffix(name, ".json") {
			return nil, errors.New("json read error")
		}
		return origReadFile(name)
	}
	_, err = previewLocalQueryRevision(ctx, query)
	assert.Error(t, err)

	osReadFile = func(name string) ([]byte, error) {
		if strings.HasSuffix(name, ".sql") {
			return nil, errors.New("sql read error")
		}
		return origReadFile(name)
	}
	_, err = previewLocalQueryRevision(ctx, query)
	assert.Error(t, err)
	osReadFile = origReadFile

	// 4. localMutationReceiptPath
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	p, err := localMutationReceiptPath("actor1", "op1")
	assert.NoError(t, err)
	assert.NotEmpty(t, p)

	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", "")
	p2, err := localMutationReceiptPath("actor1", "op1")
	assert.NoError(t, err)
	assert.NotEmpty(t, p2)

	origUserConfigDir := osUserConfigDir
	osUserConfigDir = func() (string, error) { return "", errors.New("configdir err") }
	_, err = localMutationReceiptPath("actor1", "op1")
	assert.Error(t, err)
	osUserConfigDir = origUserConfigDir

	fileInsteadOfDir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(fileInsteadOfDir, []byte("x"), 0o600))
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", fileInsteadOfDir)
	_, err = localMutationReceiptPath("actor1", "op1")
	assert.Error(t, err)
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())

	// 5. writeLocalQueryJournal
	journalPath := filepath.Join(t.TempDir(), "journal.json")
	journal := localQueryJournal{
		OperationReceipt: dto.OperationReceipt{
			OperationID: "op1",
		},
		Complete: true,
	}
	assert.NoError(t, writeLocalQueryJournal(journalPath, journal))

	origMarshal := jsonMarshal
	jsonMarshal = func(any) ([]byte, error) { return nil, errors.New("marshal err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	jsonMarshal = origMarshal

	origCreateTemp := osCreateTemp
	osCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("create temp err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	osCreateTemp = origCreateTemp

	origChmod := fileChmod
	fileChmod = func(*os.File, os.FileMode) error { return errors.New("chmod err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	fileChmod = origChmod

	origWrite := fileWrite
	fileWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	fileWrite = origWrite

	origSync := fileSync
	fileSync = func(*os.File) error { return errors.New("sync err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	fileSync = origSync

	origClose := fileClose
	fileClose = func(*os.File) error { return errors.New("close err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	fileClose = origClose

	origRename := osRename
	osRename = func(string, string) error { return errors.New("rename err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	osRename = origRename

	origOpenDir := osOpenDir
	osOpenDir = func(string) (*os.File, error) { return nil, errors.New("open dir err") }
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	osOpenDir = origOpenDir

	fileSync = func(f *os.File) error {
		if f.Name() != "" && strings.Contains(f.Name(), ".receipt-") {
			return nil
		}
		return errors.New("dir sync err")
	}
	assert.Error(t, writeLocalQueryJournal(journalPath, journal))
	fileSync = origSync

	// localGitState errors
	origOutput := localGitOutputSeam
	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "symbolic-ref" {
			return "", errors.New("symbolic ref err")
		}
		return origOutput(c, d, args...)
	}
	_, err = localGitState(ctx, dir)
	assert.ErrorIs(t, err, dto.ErrUnsupportedCapability)

	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "", errors.New("no head")
		}
		return origOutput(c, d, args...)
	}
	_, err = localGitState(ctx, dir)
	assert.ErrorIs(t, err, dto.ErrInitializationRequired)
	localGitOutputSeam = origOutput

	// 6. stageLocalQueryPair
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.NoError(t, err)

	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeDTQL, datatug.QueryTypeSQL, preview.metadata, preview.body)
	assert.NoError(t, err)

	outsideDir := t.TempDir()
	err = stageLocalQueryPair(ctx, st.root, outsideDir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.Error(t, err)

	origEval := filepathEvalSymlinks
	filepathEvalSymlinks = func(p string) (string, error) {
		if p == st.root {
			return "", errors.New("eval root err")
		}
		return origEval(p)
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.Error(t, err)

	calledCount := 0
	filepathEvalSymlinks = func(p string) (string, error) {
		calledCount++
		if calledCount == 2 {
			return "", errors.New("eval dir err")
		}
		return origEval(p)
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.Error(t, err)
	filepathEvalSymlinks = origEval

	origInput := localGitInputSeam
	localGitInputSeam = func(context.Context, string, []byte, ...string) (string, error) {
		return "", errors.New("input err")
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.Error(t, err)
	localGitInputSeam = origInput

	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "update-index" && args[1] == "--add" {
			return "", errors.New("update index add err")
		}
		return origOutput(c, d, args...)
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.Error(t, err)

	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "ls-files" {
			return "file", nil
		}
		if len(args) > 0 && args[0] == "update-index" && args[1] == "--force-remove" {
			return "", errors.New("force remove err")
		}
		return origOutput(c, d, args...)
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeDTQL, datatug.QueryTypeSQL, preview.metadata, preview.body)
	assert.Error(t, err)

	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "ls-files" {
			return "file", nil
		}
		return origOutput(c, d, args...)
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeDTQL, datatug.QueryTypeSQL, preview.metadata, preview.body)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)

	localGitOutputSeam = func(c context.Context, d string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "rev-parse" && strings.HasPrefix(args[1], ":") {
			return "wrong_oid", nil
		}
		return origOutput(c, d, args...)
	}
	err = stageLocalQueryPair(ctx, st.root, dir, "prev_q", datatug.QueryTypeSQL, "", preview.metadata, preview.body)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	localGitOutputSeam = origOutput

	_ = git
}

// Not parallel: mutates secure session, seams, and git state.
func TestCoverageFinish100_ProjectQueryAdapter_SaveQueryEdgeCases(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	ctx := context.Background()
	const pID = "proj-adapter-savequery-edges"
	dir, git := setupTestGitProject(t, pID)
	paths := map[string]string{pID: dir}

	session, err := secureread.NewSession(secureread.SessionOptions{As: "alice", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})
	defer ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})

	branch := git("symbolic-ref", "--short", "HEAD")
	head := git("rev-parse", "HEAD")

	filestore.SetProjectPath(pID, dir)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	adapter := LocalProjectQueryAdapter{}

	req := dto.SaveQueryRequest{
		OperationID:        "op-edges-1",
		ProjectRef:         dto.ProjectRef{StoreID: LocalStoreID, ProjectID: pID},
		Branch:             branch,
		ExpectedBranchHead: head,
		IfNoneMatch:        true,
		Query: datatug.QueryDefWithFolderPath{
			FolderPath: datatug.RootSharedFolderName,
			QueryDef: datatug.QueryDef{
				ProjectItem: datatug.ProjectItem{
					ProjItemBrief: datatug.ProjItemBrief{
						ID:    "edge_q1",
						Title: "Edge Q1",
					},
				},
				Type: datatug.QueryTypeSQL,
				Text: "SELECT 100",
			},
		},
	}
	scope, err := SecureProjectMutationScope(req.ProjectRef, req.Branch, "save-query")
	require.NoError(t, err)

	// 0a. SecureProjectMutationScope error (unauthenticated principal)
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})

	// 0b. servedProjectDir error
	badProjReq := req
	badProjReq.ProjectID = "unserved-project"
	badProjScope, _ := SecureProjectMutationScope(badProjReq.ProjectRef, badProjReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, badProjScope, badProjReq)
	assert.Error(t, err)

	// 0c. querywriteQueryCredentialReason
	origCred := querywriteQueryCredentialReason
	querywriteQueryCredentialReason = func(*datatug.QueryDef) (string, string, bool) {
		return "credField", "has hardcoded password", true
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	querywriteQueryCredentialReason = origCred

	// 1. Invalid query ID (legacyQueryID error)
	badIDReq := req
	badIDReq.Query.ID = "../bad"
	badIDScope, _ := SecureProjectMutationScope(badIDReq.ProjectRef, badIDReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, badIDScope, badIDReq)
	assert.Error(t, err)

	// 2. Folder support disabled (requireFolderSupport error)
	origFolderSupport := legacyStoreResolvesFolders
	legacyStoreResolvesFolders = false
	folderReq := req
	folderReq.Query.FolderPath = "folder1"
	folderScope, _ := SecureProjectMutationScope(folderReq.ProjectRef, folderReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, folderScope, folderReq)
	assert.Error(t, err)
	legacyStoreResolvesFolders = origFolderSupport

	// 3. AuthorizeProjectQueryWrite error (AllowWrites: false)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: false})
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	ConfigureSecureSession(session, paths, Capabilities{AllowWrites: true})

	// 4. saveQueryPayloadDigestSeam error
	origDigest := saveQueryPayloadDigestSeam
	saveQueryPayloadDigestSeam = func(r dto.SaveQueryRequest) (string, error) { return "", errors.New("digest err") }
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	saveQueryPayloadDigestSeam = origDigest

	// 5. localMutationReceiptPath error (osUserConfigDir err)
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", "")
	origUserCfg := osUserConfigDir
	osUserConfigDir = func() (string, error) { return "", errors.New("user cfg err") }
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	osUserConfigDir = origUserCfg
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())

	// 6. opLock busy / error
	origFlock := flockTryLockContextSeam
	flockTryLockContextSeam = func(f *flock.Flock, c context.Context, d time.Duration) (bool, error) {
		return false, nil
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	flockTryLockContextSeam = func(f *flock.Flock, c context.Context, d time.Duration) (bool, error) {
		return false, errors.New("oplock err")
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	flockTryLockContextSeam = origFlock

	// 7. Corrupted receipt JSON
	corruptReq := req
	corruptReq.OperationID = "op-corrupt"
	cPath, err := localMutationReceiptPath(scope.ActorID, corruptReq.OperationID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cPath, []byte("{bad-json"), 0o600))
	cScope, _ := SecureProjectMutationScope(corruptReq.ProjectRef, corruptReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, cScope, corruptReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)

	// 8. MatchSaveRetry mismatch
	mismatchReq := req
	mismatchReq.OperationID = "op-mismatch"
	mPath, err := localMutationReceiptPath(scope.ActorID, mismatchReq.OperationID)
	require.NoError(t, err)
	mJournal := localQueryJournal{
		OperationReceipt: dto.OperationReceipt{
			Scope:         scope,
			OperationID:   mismatchReq.OperationID,
			PayloadDigest: "different_digest",
		},
	}
	mBytes, _ := json.Marshal(mJournal)
	require.NoError(t, os.WriteFile(mPath, mBytes, 0o600))
	mScope, _ := SecureProjectMutationScope(mismatchReq.ProjectRef, mismatchReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, mScope, mismatchReq)
	assert.Error(t, err)

	// 9. Receipt read error other than ErrNotExist (EISDIR)
	dirReq := req
	dirReq.OperationID = "op-dir"
	dPath, err := localMutationReceiptPath(scope.ActorID, dirReq.OperationID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dPath, 0o700))
	dScope, _ := SecureProjectMutationScope(dirReq.ProjectRef, dirReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, dScope, dirReq)
	assert.Error(t, err)

	// 10. localGitStateSeam error before project lock
	origGitState := localGitStateSeam
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		return gitState{}, errors.New("git state err 1")
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	localGitStateSeam = origGitState

	// 11. Project lock busy / error
	lockCall := 0
	flockTryLockContextSeam = func(f *flock.Flock, c context.Context, d time.Duration) (bool, error) {
		lockCall++
		if lockCall == 2 {
			return false, nil
		}
		return origFlock(f, c, d)
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	lockCall = 0
	flockTryLockContextSeam = func(f *flock.Flock, c context.Context, d time.Duration) (bool, error) {
		lockCall++
		if lockCall == 2 {
			return false, errors.New("proj lock err")
		}
		return origFlock(f, c, d)
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	flockTryLockContextSeam = origFlock

	// 12. localGitState error after project lock
	callCount := 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		if callCount == 2 {
			return gitState{}, errors.New("git state err after lock")
		}
		return origGitState(c, d)
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	localGitStateSeam = origGitState

	// 13. Store not implementing RevisionedQueriesStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return mockNoRevStore{}, nil }
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.ErrorIs(t, err, dto.ErrUnsupportedCapability)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 13b. projectStoreForID error in SaveQuery
	storage.NewDatatugStore = func(string) (storage.Store, error) { return nil, errors.New("store err in save") }
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 14. LoadQueryRevision unexpected error
	store := filestore.NewProjectStore(pID, dir).(datatug.RevisionedQueriesStore)
	fsStore, _ := filestore.NewStore("files", paths)
	pStore := store.(datatug.ProjectStore)
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockWrappedStore{Store: fsStore, ps: mockErrLoadRevisionStore{ProjectStore: pStore, RevisionedQueriesStore: store}}, nil
	}
	_, err = adapter.SaveQuery(ctx, scope, req)
	assert.Error(t, err)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 14b. IfNoneMatch with capture provenance refusal
	validCap := &datatug.QueryCapture{Author: "alice", Environment: "local", Source: "chinook", Collection: "Invoice"}
	captureReq := req
	captureReq.OperationID = "op-capture-refusal"
	captureReq.IfNoneMatch = true
	captureReq.Query.Capture = validCap
	captureScope, _ := SecureProjectMutationScope(captureReq.ProjectRef, captureReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, captureScope, captureReq)
	assert.Error(t, err)

	// 15. Pending branches
	// Create a query first to have a valid baseline
	firstSaved, err := adapter.SaveQuery(ctx, scope, req)
	require.NoError(t, err)
	git("add", "-A")
	git("commit", "-q", "-m", "commit edge_q1")
	head = git("rev-parse", "HEAD")

	// 14c. Alter capture provenance refusal
	alterCaptureReq := req
	alterCaptureReq.OperationID = "op-alter-capture"
	alterCaptureReq.IfNoneMatch = false
	alterCaptureReq.IfMatch = firstSaved.Revision
	alterCaptureReq.ExpectedBranchHead = head
	alterCaptureReq.Query.Capture = validCap
	alterScope, _ := SecureProjectMutationScope(alterCaptureReq.ProjectRef, alterCaptureReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, alterScope, alterCaptureReq)
	assert.Error(t, err)

	// 15a. Pending branch head mismatch -> ErrProjectMutationOutcomeUncertain
	pReq := req
	pReq.OperationID = "op-pending-mismatch"
	pReq.IfNoneMatch = false
	pReq.IfMatch = firstSaved.Revision
	pReq.ExpectedBranchHead = head
	pScope, _ := SecureProjectMutationScope(pReq.ProjectRef, pReq.Branch, "save-query")
	pDigest, _ := pReq.PayloadDigest()
	pReceiptPath, _ := localMutationReceiptPath(pScope.ActorID, pReq.OperationID)
	pJournal := localQueryJournal{
		OperationReceipt: dto.OperationReceipt{
			Scope:         pScope,
			OperationID:   pReq.OperationID,
			PayloadDigest: pDigest,
			Result:        dto.SaveQueryResponse{Query: pReq.Query, Revision: "some_rev", BranchHead: head},
		},
		Complete:         false,
		QueryID:          "edge_q1",
		PreviousRevision: firstSaved.Revision,
		MetadataBytes:    []byte("{}"),
		BodyBytes:        []byte("SELECT 100"),
	}
	_ = writeLocalQueryJournal(pReceiptPath, pJournal)

	origGitState = localGitStateSeam
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		st, err := origGitState(c, d)
		st.head = "head_moved_later"
		return st, err
	}
	_, err = adapter.SaveQuery(ctx, pScope, pReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	localGitStateSeam = origGitState

	// 15b. Pending invalid journal (QueryID mismatch)
	pJournalBadQuery := pJournal
	pJournalBadQuery.QueryID = "different_query"
	_ = writeLocalQueryJournal(pReceiptPath, pJournalBadQuery)
	_, err = adapter.SaveQuery(ctx, pScope, pReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)

	// 15c. Pending foreign bytes replaced pair (!previousUnchanged)
	pJournalForeign := pJournal
	pJournalForeign.PreviousRevision = "different_rev"
	_ = writeLocalQueryJournal(pReceiptPath, pJournalForeign)
	_, err = adapter.SaveQuery(ctx, pScope, pReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)

	// 15d. Pending shouldPut = false (current revision matches journal result revision)
	pJournalCommitted := pJournal
	pJournalCommitted.Result.Revision = firstSaved.Revision
	_ = writeLocalQueryJournal(pReceiptPath, pJournalCommitted)
	completedResp, err := adapter.SaveQuery(ctx, pScope, pReq)
	assert.NoError(t, err)
	assert.Equal(t, firstSaved.Revision, completedResp.Revision)

	// 16. Non-pending branches
	// 16a. Alter capture provenance
	alterCapReq := req
	alterCapReq.OperationID = "op-alter-cap"
	alterCapReq.IfNoneMatch = false
	alterCapReq.IfMatch = firstSaved.Revision
	alterCapReq.ExpectedBranchHead = head
	alterCapReq.Query.Capture = &datatug.QueryCapture{Author: "eve"}
	alterCapScope, _ := SecureProjectMutationScope(alterCapReq.ProjectRef, alterCapReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, alterCapScope, alterCapReq)
	assert.Error(t, err)

	// 16b. previewLocalQueryRevisionSeam error
	origPreview := previewLocalQueryRevisionSeam
	previewLocalQueryRevisionSeam = func(context.Context, datatug.QueryDefWithFolderPath) (localQueryPreview, error) {
		return localQueryPreview{}, errors.New("preview seam err")
	}
	nextReq := req
	nextReq.OperationID = "op-prev-err"
	nextReq.IfNoneMatch = false
	nextReq.IfMatch = firstSaved.Revision
	nextReq.ExpectedBranchHead = head
	nextScope, _ := SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.Error(t, err)
	previewLocalQueryRevisionSeam = origPreview

	// 16c. writeLocalQueryJournalSeam initial error
	origWriteJournal := writeLocalQueryJournalSeam
	writeLocalQueryJournalSeam = func(string, localQueryJournal) error { return errors.New("write journal err") }
	nextReq.OperationID = "op-write-err"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.Error(t, err)
	writeLocalQueryJournalSeam = origWriteJournal

	// 16d. PutQuery errors
	// Revision conflict
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockWrappedStore{Store: fsStore, ps: mockPutQueryErrStore{ProjectStore: pStore, RevisionedQueriesStore: store, putErr: &datatug.QueryRevisionConflictError{}}}, nil
	}
	nextReq.OperationID = "op-put-conflict"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, dto.ErrQueryRevisionConflict)

	// Bad record / invalid query location
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockWrappedStore{Store: fsStore, ps: mockPutQueryErrStore{ProjectStore: pStore, RevisionedQueriesStore: store, putErr: validation.NewErrBadRecordFieldValue("field", "reason")}}, nil
	}
	nextReq.OperationID = "op-put-bad-loc"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.Error(t, err)

	// Generic PutQuery error
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockWrappedStore{Store: fsStore, ps: mockPutQueryErrStore{ProjectStore: pStore, RevisionedQueriesStore: store, putErr: errors.New("generic put err")}}, nil
	}
	nextReq.OperationID = "op-put-generic"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)

	// PutQuery returns different revision
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockWrappedStore{Store: fsStore, ps: mockPutQueryErrStore{ProjectStore: pStore, RevisionedQueriesStore: store, putRev: "unexpected_rev"}}, nil
	}
	nextReq.OperationID = "op-put-diff-rev"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 16e. beforeStage mismatch
	callCount = 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		st, err := origGitState(c, d)
		if callCount == 3 {
			st.head = "head_changed_before_stage"
		}
		return st, err
	}
	nextReq.OperationID = "op-before-stage-mismatch"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	localGitStateSeam = origGitState

	// 16f. stageLocalQueryPair error
	origInput := localGitInputSeam
	localGitInputSeam = func(context.Context, string, []byte, ...string) (string, error) {
		return "", errors.New("stage input err")
	}
	nextReq.OperationID = "op-stage-err"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	localGitInputSeam = origInput

	// 16g. stagedPair revision mismatch
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockWrappedStore{Store: fsStore, ps: &mockSecondErrLoadRevisionStore{ProjectStore: pStore, RevisionedQueriesStore: store}}, nil
	}
	nextReq.OperationID = "op-staged-pair-err"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }

	// 16h. afterStage mismatch
	callCount = 0
	localGitStateSeam = func(c context.Context, d string) (gitState, error) {
		callCount++
		st, err := origGitState(c, d)
		if callCount == 4 {
			st.head = "head_changed_after_stage"
		}
		return st, err
	}
	nextReq.OperationID = "op-after-stage-mismatch"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	localGitStateSeam = origGitState

	// 16i. writeLocalQueryJournalSeam final error (completion receipt failed)
	wCall := 0
	writeLocalQueryJournalSeam = func(p string, j localQueryJournal) error {
		wCall++
		if wCall == 2 {
			return errors.New("final receipt write err")
		}
		return origWriteJournal(p, j)
	}
	nextReq.OperationID = "op-final-receipt-err"
	nextScope, _ = SecureProjectMutationScope(nextReq.ProjectRef, nextReq.Branch, "save-query")
	_, err = adapter.SaveQuery(ctx, nextScope, nextReq)
	assert.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	writeLocalQueryJournalSeam = origWriteJournal
}

type mockNoRevStore struct {
	storage.Store
}

func (mockNoRevStore) GetProjectStore(string) datatug.ProjectStore {
	return mockNoRevProjStore{}
}

type mockNoRevProjStore struct {
	datatug.ProjectStore
}

type mockWrappedStore struct {
	storage.Store
	ps datatug.ProjectStore
}

func (m mockWrappedStore) GetProjectStore(string) datatug.ProjectStore {
	return m.ps
}

type mockErrLoadRevisionStore struct {
	datatug.ProjectStore
	datatug.RevisionedQueriesStore
}

func (m mockErrLoadRevisionStore) LoadQueryRevision(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	return nil, errors.New("load revision error")
}

type mockSecondErrLoadRevisionStore struct {
	datatug.ProjectStore
	datatug.RevisionedQueriesStore
	callCount int
}

func (m *mockSecondErrLoadRevisionStore) LoadQueryRevision(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.StoredQuery, error) {
	m.callCount++
	if m.callCount == 2 {
		return nil, errors.New("second load revision err")
	}
	return m.RevisionedQueriesStore.LoadQueryRevision(ctx, id, o...)
}

type mockPutQueryErrStore struct {
	datatug.ProjectStore
	datatug.RevisionedQueriesStore
	putErr error
	putRev datatug.QueryRevision
}

func (m mockPutQueryErrStore) PutQuery(ctx context.Context, q *datatug.QueryDefWithFolderPath, c datatug.QueryWriteCondition) (*datatug.StoredQuery, error) {
	if m.putErr != nil {
		return nil, m.putErr
	}
	res, err := m.RevisionedQueriesStore.PutQuery(ctx, q, c)
	if err == nil && m.putRev != "" {
		res.Revision = m.putRev
	}
	return res, err
}
