package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	"github.com/stretchr/testify/require"
)

func configureBranchReadTest(t *testing.T, projectID, dir string) {
	t.Helper()
	paths := map[string]string{projectID: dir}
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, paths, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
}

func TestQualifiedProjectReadsUseOnlySelectedCurrentBranch(t *testing.T) {
	const projectID = "qualified-read-branch"
	dir, git := setupTestGitProject(t, projectID)
	store := filestore.NewProjectStore(projectID, dir)
	ctx := context.Background()
	board := &datatug.Board{ProjectItem: datatug.ProjectItem{ID: "sales", Title: "Branch A"}}
	require.NoError(t, store.SaveBoard(ctx, board))
	git("add", "-A")
	git("commit", "-q", "-m", "branch A board")
	branchA := git("symbolic-ref", "--short", "HEAD")
	git("switch", "-q", "-c", "branch-b")
	board.Title = "Branch B"
	require.NoError(t, store.SaveBoard(ctx, board))
	git("add", "-A")
	git("commit", "-q", "-m", "branch B board")
	branchB := git("symbolic-ref", "--short", "HEAD")
	git("switch", "-q", branchA)
	configureBranchReadTest(t, projectID, dir)

	ref := dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "sales"}
	got, err := LocalProjectBoard(ctx, ref, branchA)
	require.NoError(t, err)
	require.Equal(t, "Branch A", got.Title)
	briefs, err := LocalProjectBoards(ctx, ref.ProjectRef, branchA)
	require.NoError(t, err)
	require.Equal(t, []datatug.ProjBoardBrief{{ProjItemBrief: datatug.ProjItemBrief{ID: "sales", Title: "Branch A"}}}, briefs)
	_, err = LocalProjectBoard(ctx, ref, branchB)
	require.ErrorIs(t, err, dto.ErrBranchHeadConflict)
	require.NotContains(t, err.Error(), "Branch B")

	// Legacy unqualified reads continue to resolve the current checkout.
	legacy, err := GetBoard(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, "Branch A", legacy.Title)
}

func TestQualifiedProjectReadRejectsHeadChangeAndBusyLock(t *testing.T) {
	const projectID = "qualified-read-head-change"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}
	branch := git("symbolic-ref", "--short", "HEAD")

	_, err := withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) {
		git("commit", "--allow-empty", "-q", "-m", "move HEAD while reading")
		return []datatug.ProjItemBrief{}, nil
	})
	require.ErrorIs(t, err, dto.ErrBranchHeadConflict)

	originalLock := flockTryLockContextSeam
	lockAttempts := 0
	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) {
		lockAttempts++
		return false, nil
	}
	t.Cleanup(func() { flockTryLockContextSeam = originalLock })
	lockCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = withCurrentBranchRead(lockCtx, ref, branch, func(*qualifiedProjectReader) (any, error) {
		t.Fatal("read callback ran despite a busy project lock")
		return nil, nil
	})
	require.True(t, errors.Is(err, context.DeadlineExceeded), "busy lock error = %v", err)
	require.Equal(t, 1, lockAttempts)
}

func TestQualifiedEntityReadPreservesBoundedWebFieldsAcrossLayouts(t *testing.T) {
	const projectID = "qualified-read-entity-wire"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	entity := `{"id":"customer","title":"Customer","fields":[{"id":"Email","type":"string","checks":[]}],"extends":{"def":"https://example.test/customer.json"},"sourceOfRecord":{"db":"local","objType":"table","objName":"Customer"},"options":{"sources":[]}}`
	nested := filepath.Join(dir, "entities", "customer", "customer.entity.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o755))
	require.NoError(t, os.WriteFile(nested, []byte(entity), 0o600))
	git("add", "-A")
	git("commit", "-q", "-m", "entity nested layout")
	branch := git("symbolic-ref", "--short", "HEAD")
	got, err := LocalProjectEntity(context.Background(), dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "customer"}, branch)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(got, &decoded))
	require.Equal(t, "https://example.test/customer.json", decoded["extends"].(map[string]any)["def"])
	require.Equal(t, "Customer", decoded["sourceOfRecord"].(map[string]any)["objName"])
	require.Contains(t, decoded, "options")
	require.Equal(t, []any{}, decoded["fields"].([]any)[0].(map[string]any)["checks"])
	flat := filepath.Join(dir, "entities", "customer.entity.json")
	require.NoError(t, os.WriteFile(flat, []byte(`{"id":"customer","title":"Customer","fields":[{"id":"Email","type":"string","checks":[]}],"extends":{"def":"https://other.test/customer.json"},"sourceOfRecord":{"db":"local","objType":"table","objName":"Customer"},"options":{"sources":[]}}`), 0o600))
	git("add", "-A")
	git("commit", "-q", "-m", "conflicting entity flat layout")
	branch = git("symbolic-ref", "--short", "HEAD")
	_, err = LocalProjectEntity(context.Background(), dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "customer"}, branch)
	require.ErrorContains(t, err, "different content")
}

func TestQualifiedProjectReadRequiresPrincipalAndBoundsResponses(t *testing.T) {
	const projectID = "qualified-read-auth"
	dir, git := setupTestGitProject(t, projectID)
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}
	ConfigureSecureSession(secureread.Session{}, nil, Capabilities{})
	_, err := LocalProjectBoards(context.Background(), ref, branch)
	require.ErrorIs(t, err, secureread.ErrAccessDenied)

	configureBranchReadTest(t, projectID, dir)
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) {
		return map[string]string{"payload": string(make([]byte, maxQualifiedReadBytes+1))}, nil
	})
	require.ErrorContains(t, err, "exceeds")
}

func TestQualifiedProjectReadsConfineAndBoundActualFiles(t *testing.T) {
	const projectID = "qualified-read-confined-files"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "sales"}
	boardsDir := filepath.Join(dir, "boards")
	require.NoError(t, os.MkdirAll(boardsDir, 0o755))
	outside := filepath.Join(t.TempDir(), "outside.board.json")
	secret := `{"id":"sales","title":"outside-secret"}`
	require.NoError(t, os.WriteFile(outside, []byte(secret), 0o600))
	link := filepath.Join(boardsDir, "sales.board.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := LocalProjectBoard(context.Background(), ref, branch)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "outside-secret")
	require.NoError(t, os.Remove(link))

	oversized := `{"id":"sales","title":"` + strings.Repeat("x", maxQualifiedReadBytes+1) + `"}`
	require.NoError(t, os.WriteFile(link, []byte(oversized), 0o600))
	reader, err := openQualifiedProjectReader(dir)
	require.NoError(t, err)
	defer reader.close()
	_, err = reader.readQualifiedBoard("sales")
	require.ErrorContains(t, err, "too large")
}

func TestQualifiedBoardStoredIDMatchesPathOrIsAbsent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "matching id", body: `{"id":"sales","title":"Sales"}`},
		{name: "missing id is supplied from path", body: `{"title":"Sales"}`},
		{name: "mismatched id is rejected", body: `{"id":"other","title":"Sales"}`, wantErr: true},
		{name: "non-string id is rejected", body: `{"id":7,"title":"Sales"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const projectID = "qualified-board-id"
			dir, git := setupTestGitProject(t, projectID)
			configureBranchReadTest(t, projectID, dir)
			file := filepath.Join(dir, "boards", "sales.board.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
			require.NoError(t, os.WriteFile(file, []byte(tc.body), 0o600))
			branch := git("symbolic-ref", "--short", "HEAD")
			got, err := LocalProjectBoard(context.Background(), dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "sales"}, branch)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "sales", got.ID)
		})
	}
}

func TestQualifiedEnvironmentProjectionOmitsConnectionFields(t *testing.T) {
	const projectID = "qualified-read-environments"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	store := filestore.NewProjectStore(projectID, dir)
	env := &datatug.Environment{ProjectItem: datatug.ProjectItem{ID: "local", Title: "Local"}, EditionConnections: []string{"private-connection"}}
	require.NoError(t, store.SaveEnvironment(context.Background(), env))
	envPath := filepath.Join(dir, "environments", "local", "local.env.json")
	require.NoError(t, os.WriteFile(envPath, []byte(`{"id":"local","title":"Local","dbServers":[{"id":"secret-server","password":"private-value"}],"editionConnections":["private-connection"],"vars":{"token":"private-value"},"presentation":{"label":"Production","icon":"shield-checkmark-outline","tone":"danger"}}`), 0o600))
	git("add", "-A")
	git("commit", "-q", "-m", "environment")
	branch := git("symbolic-ref", "--short", "HEAD")
	briefs, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, branch)
	require.NoError(t, err)
	body, err := json.Marshal(briefs)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"local","title":"Local","presentation":{"label":"Production","icon":"shield-checkmark-outline","tone":"danger"}}]`, string(body))
	require.NotContains(t, string(body), "dbServers")
	require.NotContains(t, string(body), "editionConnections")
	require.NotContains(t, string(body), "vars")
	require.NotContains(t, string(body), "private-value")
}

func TestQualifiedEnvironmentDefaultsAndCurrentFilePrecedence(t *testing.T) {
	const projectID = "qualified-read-environment-default"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	defaultDir := filepath.Join(dir, "environments", "empty")
	require.NoError(t, os.MkdirAll(defaultDir, 0o755))
	precedenceDir := filepath.Join(dir, "environments", "prod")
	require.NoError(t, os.MkdirAll(precedenceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(precedenceDir, "prod.env.json"), []byte(`{"id":"prod","title":"Current title","presentation":{"label":"Current","tone":"success"}}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(precedenceDir, "environment-summary.json"), []byte(`{"id":"prod","title":"Legacy title","presentation":{"label":"Legacy","tone":"danger"}}`), 0o600))
	git("add", "-A")
	git("commit", "-q", "-m", "environment directory defaults and precedence")
	branch := git("symbolic-ref", "--short", "HEAD")
	got, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, branch)
	require.NoError(t, err)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"empty","title":"empty"},{"id":"prod","title":"Current title","presentation":{"label":"Current","tone":"success"}}]`, string(encoded))
}

func TestQualifiedEnvironmentStoredIDMatchesPathOrIsAbsent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		file    string
		body    string
		wantErr bool
	}{
		{name: "matching current id", file: "env.env.json", body: `{"id":"env","title":"Env"}`},
		{name: "missing current id is supplied from path", file: "env.env.json", body: `{"title":"Env"}`},
		{name: "mismatched current id is rejected", file: "env.env.json", body: `{"id":"other","title":"Env"}`, wantErr: true},
		{name: "mismatched legacy id is rejected", file: "environment-summary.json", body: `{"id":"other","title":"Env"}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const projectID = "qualified-environment-id"
			dir, git := setupTestGitProject(t, projectID)
			configureBranchReadTest(t, projectID, dir)
			envDir := filepath.Join(dir, "environments", "env")
			require.NoError(t, os.MkdirAll(envDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(envDir, tc.file), []byte(tc.body), 0o600))
			branch := git("symbolic-ref", "--short", "HEAD")
			got, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, branch)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, "env", got[0].ID)
		})
	}
}
