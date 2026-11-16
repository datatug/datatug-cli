package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestQualifiedProjectConnectionCatalogReadsSelectedBranchAndFailsClosed(t *testing.T) {
	const projectID = "qualified-connection-catalog"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	path := filepath.Join(dir, "connections", "demo-db.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	valid := `{"format":"datatug-demo-connections/v1","connections":[{"id":"chinook-sqlite","dataset":"chinook","storage":"sqlite","readiness":"public-api","source":"https://demodb.dev/ovdb/v1/databases/chinook","fixtureSha256":"7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15","browserFixture":{"url":"https://chinook.demodb.dev/data/chinook.sqlite","bytes":1007616}}]}`
	require.NoError(t, os.WriteFile(path, []byte(valid), 0o600))
	got, err := LocalProjectConnectionCatalogForBranch(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, branch)
	require.NoError(t, err)
	require.Equal(t, "datatug-demo-connections/v1", got.Format)
	require.Len(t, got.Connections, 1)
	require.Equal(t, "chinook-sqlite", got.Connections[0].ID)

	require.NoError(t, os.Remove(path))
	_, err = LocalProjectConnectionCatalogForBranch(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, branch)
	require.Error(t, err)

	require.NoError(t, os.WriteFile(path, []byte(`{"format":"unknown"}`), 0o600))
	_, err = LocalProjectConnectionCatalogForBranch(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, branch)
	require.Error(t, err)
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
	originalGitState := localGitStateSeam
	localGitStateSeam = func(context.Context, string) (gitState, error) {
		return gitState{root: dir, gitDir: dir, branch: branch, head: "head"}, nil
	}
	lockAttempts := 0
	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) {
		lockAttempts++
		return false, nil
	}
	t.Cleanup(func() {
		flockTryLockContextSeam = originalLock
		localGitStateSeam = originalGitState
	})
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

func TestQualifiedReaderFileAndDirectoryBoundaries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "boards"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "valid.json"), []byte(`{"ok":true}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oversized.json"), []byte("12345"), 0o600))
	reader, err := openQualifiedProjectReader(dir)
	require.NoError(t, err)
	defer reader.close()

	if _, err := reader.readFileLimit("../outside", 10); err == nil {
		t.Fatal("parent traversal was accepted")
	}
	if _, err := reader.readFileLimit("missing.json", 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
	if _, err := reader.readFileLimit("boards", 10); err == nil {
		t.Fatal("directory was accepted as a document")
	}
	if _, err := reader.readFileLimit("oversized.json", 4); err == nil {
		t.Fatal("oversized file was accepted")
	}
	reader.bytes = maxQualifiedReadBytes - 5
	if _, err := reader.readFileLimit("valid.json", 20); err == nil {
		t.Fatal("read exceeded the remaining aggregate byte budget")
	}
	reader.bytes = maxQualifiedReadBytes + 1
	if _, err := reader.readFileLimit("valid.json", 20); err == nil {
		t.Fatal("aggregate byte limit underflow was accepted")
	}
	reader.bytes = 0
	if _, err := reader.readJSON("valid.json", new(map[string]any)); err != nil {
		t.Fatalf("readJSON valid document: %v", err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "invalid.json"), []byte("{"), 0o600))
	if _, err := reader.readJSON("invalid.json", new(map[string]any)); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	entries, err := reader.dirEntries("absent")
	require.NoError(t, err)
	require.Empty(t, entries)
	if _, err := reader.dirEntries("../outside"); err == nil {
		t.Fatal("directory traversal was accepted")
	}
	entries, err = reader.dirEntries(".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	if _, err := openQualifiedProjectReader(filepath.Join(dir, "not-created")); err == nil {
		t.Fatal("opening a missing project root succeeded")
	}
}

func TestQualifiedReaderEntityAndBoardLayouts(t *testing.T) {
	dir := t.TempDir()
	reader, err := openQualifiedProjectReader(dir)
	require.NoError(t, err)
	defer reader.close()

	entitiesDir := filepath.Join(dir, "entities")
	require.NoError(t, os.MkdirAll(filepath.Join(entitiesDir, "folder-only"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(entitiesDir, "customer.entity.json"), []byte(`{"id":"customer","title":"Customer"}`), 0o600))
	entity, err := reader.readQualifiedEntity("customer")
	require.NoError(t, err)
	require.Equal(t, "Customer", entity.Title)
	entities, err := readQualifiedEntities(reader)
	require.NoError(t, err)
	require.Len(t, entities, 1)
	require.True(t, isDirectoryEntry(mustQualifiedEntries(t, reader, storage.EntitiesFolder), "folder-only"))
	require.False(t, isDirectoryEntry(mustQualifiedEntries(t, reader, storage.EntitiesFolder), "customer.entity.json"))
	require.False(t, isDirectoryEntry(mustQualifiedEntries(t, reader, storage.EntitiesFolder), "not-present"))
	ids := itemIDs(mustQualifiedEntries(t, reader, storage.EntitiesFolder), storage.EntityFileSuffix)
	require.Equal(t, []string{"customer", "folder-only"}, sortedIDs(ids))
	if _, err := reader.readQualifiedEntity("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing entity error = %v", err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(entitiesDir, "broken.entity.json"), []byte(`{"fields":"wrong type"}`), 0o600))
	if _, err := reader.readQualifiedEntity("broken"); err == nil {
		t.Fatal("entity with an invalid model field type was accepted")
	}

	boardsDir := filepath.Join(dir, "boards")
	require.NoError(t, os.MkdirAll(boardsDir, 0o755))
	if _, err := reader.readQualifiedBoard("sales"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing board error = %v", err)
	}
	require.NoError(t, os.WriteFile(filepath.Join(boardsDir, "sales.board.json"), []byte("{"), 0o600))
	if _, err := reader.readQualifiedBoard("sales"); err == nil {
		t.Fatal("malformed board JSON was accepted")
	}
	require.NoError(t, os.WriteFile(filepath.Join(boardsDir, "sales.board.json"), []byte(`{"id":"sales","title":"Flat"}`), 0o600))
	nested := filepath.Join(boardsDir, "sales", "board.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o755))
	require.NoError(t, os.WriteFile(nested, []byte(`{"id":"sales","title":"Nested"}`), 0o600))
	if _, err := reader.readQualifiedBoard("sales"); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("conflicting board layouts error = %v", err)
	}
	if _, err := reader.readQualifiedBoard("../sales"); err == nil {
		t.Fatal("invalid board ID was accepted")
	}
	require.NoError(t, os.RemoveAll(filepath.Join(boardsDir, "sales")))
	require.NoError(t, os.WriteFile(filepath.Join(boardsDir, "sales.board.json"), []byte(`{"id":"sales","title":7}`), 0o600))
	if _, err := reader.readQualifiedBoard("sales"); err == nil {
		t.Fatal("board with an invalid model field type was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(boardsDir, "sales.board.json")))
	require.NoError(t, os.Mkdir(filepath.Join(boardsDir, "empty-folder"), 0o755))
	boards, err := readQualifiedBoards(reader)
	require.NoError(t, err)
	require.Empty(t, boards)
	nestedError := filepath.Join(boardsDir, "invalid", "board.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(nestedError), 0o755))
	require.NoError(t, os.WriteFile(nestedError, []byte(`{"id":"invalid","title":7}`), 0o600))
	if _, err := reader.readQualifiedBoard("invalid"); err == nil {
		t.Fatal("invalid nested board model was accepted")
	}
}

func mustQualifiedEntries(t *testing.T, reader *qualifiedProjectReader, dir string) []os.DirEntry {
	t.Helper()
	entries, err := reader.dirEntries(dir)
	require.NoError(t, err)
	return entries
}

func sortedIDs(ids []string) []string {
	// The helper is used only in assertions and keeps ordering explicit.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

func TestQualifiedProjectEntitiesAndSummary(t *testing.T) {
	const projectID = "qualified-read-summary"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"title":"Qualified project"}`), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "entities"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entities", "customer.entity.json"), []byte(`{"id":"customer","title":"Customer"}`), 0o600))
	git("add", "-A")
	git("commit", "-q", "-m", "project summary")
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}
	entities, err := LocalProjectEntities(context.Background(), ref, branch)
	require.NoError(t, err)
	require.Equal(t, []datatug.ProjEntityBrief{{ProjItemBrief: datatug.ProjItemBrief{ID: "customer", Title: "Customer"}}}, entities)
	summary, err := LocalProjectSummary(context.Background(), ref, branch)
	require.NoError(t, err)
	require.Equal(t, "Qualified project", summary.Title)
	require.Len(t, summary.Entities, 1)
	require.Equal(t, "customer", summary.Entities[0].ID)
}

func TestQualifiedEnvironmentPresentationRejectsInvalidMetadata(t *testing.T) {
	for _, body := range []string{
		"[1]",
		`{"presentation":[]}`,
		`{"presentation":{"label":7}}`,
		`{"presentation":{"unknown":"value"}}`,
		`{"presentation":{"label":"` + strings.Repeat("x", 257) + `"}}`,
		`{"presentation":{"icon":"unsupported"}}`,
		`{"presentation":{"tone":"unsupported"}}`,
	} {
		if _, err := parseEnvironmentPresentation([]byte(body)); err == nil {
			t.Errorf("accepted invalid environment metadata %q", body)
		}
	}
	if _, err := parseEnvironmentPresentation([]byte("{")); err == nil {
		t.Fatal("accepted malformed environment JSON")
	}
	if got, err := parseEnvironmentPresentation(nil); err != nil || got != nil {
		t.Fatalf("empty presentation = %#v, %v", got, err)
	}
	for _, icon := range []string{"globe-outline", "desktop-outline", "flask-outline", "cloud-outline", "shield-checkmark-outline", "warning-outline"} {
		if !allowedEnvironmentIcon(icon) {
			t.Errorf("supported icon %q was rejected", icon)
		}
	}
	if allowedEnvironmentIcon("unsupported") {
		t.Fatal("unsupported icon was accepted")
	}
}

func TestQualifiedReaderDualLayoutsAndLegacyEnvironment(t *testing.T) {
	dir := t.TempDir()
	reader, err := openQualifiedProjectReader(dir)
	require.NoError(t, err)
	defer reader.close()

	entityDir := filepath.Join(dir, "entities")
	require.NoError(t, os.MkdirAll(filepath.Join(entityDir, "customer"), 0o755))
	nested := filepath.Join(entityDir, "customer", "customer.entity.json")
	flat := filepath.Join(entityDir, "customer.entity.json")
	require.NoError(t, os.WriteFile(nested, []byte(`{"id":"customer","title":"Customer"}`), 0o600))
	require.NoError(t, os.WriteFile(flat, []byte("["), 0o600))
	if _, err := readQualifiedEntityDocument(reader, "customer"); err == nil {
		t.Fatal("malformed dual-layout entity was accepted")
	}
	require.NoError(t, os.WriteFile(flat, []byte(`{"id":"customer","title":"Customer"}`), 0o600))
	require.NoError(t, os.WriteFile(nested, []byte(`{"id":"another-entity","title":"Customer"}`), 0o600))
	if _, err := readQualifiedEntityDocument(reader, "customer"); err == nil {
		t.Fatal("entity document with a mismatched ID was accepted")
	}
	require.NoError(t, os.WriteFile(nested, []byte(`{"id":"customer","title":"Customer"}`), 0o600))
	require.NoError(t, os.WriteFile(flat, []byte(`{"id":"other","title":"Customer"}`), 0o600))
	if _, err := readQualifiedEntityDocument(reader, "customer"); err == nil {
		t.Fatal("mismatched flat entity ID was accepted")
	}
	require.NoError(t, os.WriteFile(flat, []byte(`{"id":"customer","title":"Customer"}`), 0o600))
	if _, err := readQualifiedEntityDocument(reader, "customer"); err != nil {
		t.Fatalf("matching entity layouts: %v", err)
	}
	require.NoError(t, os.WriteFile(nested, []byte(`{"title":"Customer"}`), 0o600))
	require.NoError(t, os.WriteFile(flat, []byte(`{"title":"Customer"}`), 0o600))
	if _, err := readQualifiedEntityDocument(reader, "customer"); err != nil {
		t.Fatalf("matching layouts with path-derived IDs: %v", err)
	}
	if sameJSONFields(map[string]json.RawMessage{"x": []byte("{")}, map[string]json.RawMessage{}) {
		t.Fatal("invalid raw JSON fields compared equal")
	}

	envDir := filepath.Join(dir, "environments", "legacy")
	require.NoError(t, os.MkdirAll(envDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "environment-summary.json"), []byte(`{"id":"legacy","title":"Legacy","dbServers":[{"driver":"postgres"}]}`), 0o600))
	environments, err := readQualifiedEnvironments(reader)
	require.NoError(t, err)
	require.Len(t, environments, 1)
	require.Equal(t, "Legacy", environments[0].env.Title)
	require.Len(t, environments[0].env.DbServers, 1)
	require.Equal(t, "postgres", environments[0].env.DbServers[0].Driver)
	// Legacy layout is supported; incomplete server definitions are not.
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "environment-summary.json"), []byte(`{"id":"legacy","dbServers":[{"id":"server"}]}`), 0o600))
	if _, err := readQualifiedEnvironments(reader); err == nil {
		t.Fatal("legacy summary without an owning driver was accepted")
	}
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "environment-summary.json"), []byte(`{"id":"legacy","title":"Legacy","dbServers":[{"driver":"postgres"}]}`), 0o600))

	invalidEnvDir := filepath.Join(dir, "environments", "broken")
	require.NoError(t, os.MkdirAll(invalidEnvDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(invalidEnvDir, "broken.env.json"), []byte("{"), 0o600))
	if _, err := readQualifiedEnvironments(reader); err == nil {
		t.Fatal("malformed environment document was accepted")
	}
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "environments", "broken")))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "legacy.env.json"), []byte(`{"id":"legacy","title":7}`), 0o600))
	if _, _, err := readQualifiedEnvironment(reader, "legacy"); err == nil {
		t.Fatal("environment with an invalid model field type was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(envDir, "legacy.env.json")))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "environment-summary.json"), []byte(`{"id":"legacy","title":"Legacy","dbServers":"bad"}`), 0o600))
	if _, _, err := readQualifiedEnvironment(reader, "legacy"); err == nil {
		t.Fatal("legacy summary with invalid server data was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(envDir, "environment-summary.json")))
	require.NoError(t, os.WriteFile(filepath.Join(envDir, "environment-summary.json"), []byte(`{"id":"legacy","title":"`+strings.Repeat("x", maxEnvironmentDocumentBytes)+`"}`), 0o600))
	if _, _, err := readQualifiedEnvironment(reader, "legacy"); err == nil {
		t.Fatal("oversized legacy environment was accepted")
	}

	for _, item := range []struct{ path, link string }{
		{path: filepath.Join(dir, "entities", "escape"), link: filepath.Join(dir, "entities", "escape", "escape.entity.json")},
		{path: filepath.Join(dir, "boards", "escape"), link: filepath.Join(dir, "boards", "escape", "board.json")},
	} {
		outside := filepath.Join(t.TempDir(), "outside.json")
		require.NoError(t, os.WriteFile(outside, []byte(`{"id":"escape"}`), 0o600))
		require.NoError(t, os.MkdirAll(item.path, 0o755))
		if err := os.Symlink(outside, item.link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if strings.Contains(item.path, "entities") {
			if _, err := readQualifiedEntityDocument(reader, "escape"); err == nil {
				t.Fatal("entity symlink escape was accepted")
			}
		} else if _, err := reader.readQualifiedBoard("escape"); err == nil {
			t.Fatal("board symlink escape was accepted")
		}
	}
	flatOutside := filepath.Join(t.TempDir(), "outside.entity.json")
	require.NoError(t, os.WriteFile(flatOutside, []byte(`{"id":"escape"}`), 0o600))
	if err := os.Symlink(flatOutside, filepath.Join(dir, "entities", "flat-escape.entity.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readQualifiedEntityDocument(reader, "flat-escape"); err == nil {
		t.Fatal("flat entity symlink escape was accepted")
	}

	oversizedReaderDir := t.TempDir()
	oversizedReader, err := openQualifiedProjectReader(oversizedReaderDir)
	require.NoError(t, err)
	defer oversizedReader.close()
	prodDir := filepath.Join(oversizedReaderDir, "environments", "prod")
	require.NoError(t, os.MkdirAll(prodDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(prodDir, "prod.env.json"), []byte(`{"id":"prod","title":"`+strings.Repeat("x", maxEnvironmentDocumentBytes)+`"}`), 0o600))
	if _, _, err := readQualifiedEnvironment(oversizedReader, "prod"); err == nil {
		t.Fatal("oversized current environment document was accepted")
	}
	require.NoError(t, os.WriteFile(filepath.Join(prodDir, "prod.env.json"), []byte(`{"id":"prod","presentation":{"icon":"unsupported"}}`), 0o600))
	if _, err := readQualifiedEnvironments(oversizedReader); err == nil {
		t.Fatal("invalid environment presentation was accepted by the list reader")
	}
}

func TestQualifiedProjectDirectoryAndAssetCountLimits(t *testing.T) {
	dir := t.TempDir()
	for _, folder := range []string{"boards", "entities", "environments"} {
		base := filepath.Join(dir, folder)
		require.NoError(t, os.MkdirAll(base, 0o755))
		for i := 0; i <= maxQualifiedProjectItems; i++ {
			require.NoError(t, os.Mkdir(filepath.Join(base, fmt.Sprintf("item-%04d", i)), 0o755))
		}
	}
	reader, err := openQualifiedProjectReader(dir)
	require.NoError(t, err)
	defer reader.close()
	if _, err := readQualifiedBoards(reader); err == nil || !strings.Contains(err.Error(), "board list exceeds") {
		t.Fatalf("board count limit error = %v", err)
	}
	if _, err := readQualifiedEntities(reader); err == nil || !strings.Contains(err.Error(), "entity list exceeds") {
		t.Fatalf("entity count limit error = %v", err)
	}
	if _, err := readQualifiedEnvironments(reader); err == nil || !strings.Contains(err.Error(), "environment list exceeds") {
		t.Fatalf("environment count limit error = %v", err)
	}
	tooMany := filepath.Join(dir, "too-many")
	require.NoError(t, os.Mkdir(tooMany, 0o755))
	for i := 0; i <= maxQualifiedDirEntries; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(tooMany, fmt.Sprintf("item-%05d", i)), nil, 0o600))
	}
	if _, err := reader.dirEntries("too-many"); err == nil || !strings.Contains(err.Error(), "directory exceeds") {
		t.Fatalf("directory entry limit error = %v", err)
	}
	if _, err := readQualifiedLimited(bytes.NewReader([]byte("too large")), 2); err == nil {
		t.Fatal("limited reader accepted too many bytes")
	}
	if _, err := readQualifiedLimited(errorReader{}, 4); err == nil {
		t.Fatal("limited reader ignored a source read error")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failure") }

func TestQualifiedBranchReadGuardFailures(t *testing.T) {
	const projectID = "qualified-read-guard-errors"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}
	if _, err := withCurrentBranchRead(context.Background(), dto.ProjectRef{}, branch, func(*qualifiedProjectReader) (any, error) { return nil, nil }); err == nil {
		t.Fatal("invalid project ref was accepted")
	}
	if _, err := withCurrentBranchRead(context.Background(), dto.ProjectRef{StoreID: "remote", ProjectID: projectID}, branch, func(*qualifiedProjectReader) (any, error) { return nil, nil }); err == nil {
		t.Fatal("non-local store was accepted")
	}
	if _, err := withCurrentBranchRead(context.Background(), ref, "", func(*qualifiedProjectReader) (any, error) { return nil, nil }); err == nil {
		t.Fatal("missing branch was accepted")
	}
	if _, err := withCurrentBranchRead(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "unknown-qualified-project"}, branch, func(*qualifiedProjectReader) (any, error) { return nil, nil }); err == nil {
		t.Fatal("unserved project was accepted")
	}

	previousGitState := localGitStateSeam
	localGitStateSeam = func(context.Context, string) (gitState, error) {
		return gitState{}, errors.New("git state unavailable")
	}
	_, err := withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) { return nil, nil })
	require.ErrorContains(t, err, "git state unavailable")

	localGitStateSeam = previousGitState
	previousLock := flockTryLockContextSeam
	flockTryLockContextSeam = func(*flock.Flock, context.Context, time.Duration) (bool, error) {
		return false, errors.New("lock backend unavailable")
	}
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) { return nil, nil })
	require.ErrorContains(t, err, "lock backend unavailable")

	flockTryLockContextSeam = previousLock
	gitCallsBeforeLockError := 0
	localGitStateSeam = func(ctx context.Context, projectDir string) (gitState, error) {
		gitCallsBeforeLockError++
		if gitCallsBeforeLockError == 2 {
			return gitState{}, errors.New("locked git state unavailable")
		}
		return previousGitState(ctx, projectDir)
	}
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) { return nil, nil })
	require.ErrorContains(t, err, "locked git state unavailable")
	require.Equal(t, 2, gitCallsBeforeLockError)
	localGitStateSeam = previousGitState
	gitCalls := 0
	localGitStateSeam = func(ctx context.Context, projectDir string) (gitState, error) {
		gitCalls++
		if gitCalls == 3 {
			return gitState{}, errors.New("post-read git state unavailable")
		}
		return previousGitState(ctx, projectDir)
	}
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) { return "read", nil })
	require.ErrorIs(t, err, dto.ErrBranchHeadConflict)
	require.Equal(t, 3, gitCalls)

	localGitStateSeam = previousGitState
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) { return nil, errors.New("document read failed") })
	require.ErrorContains(t, err, "document read failed")
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) { return make(chan int), nil })
	require.ErrorContains(t, err, "unsupported type")
	previousOpenReader := openQualifiedProjectReaderSeam
	openQualifiedProjectReaderSeam = func(string) (*qualifiedProjectReader, error) { return nil, errors.New("root open failed") }
	_, err = withCurrentBranchRead(context.Background(), ref, branch, func(*qualifiedProjectReader) (any, error) {
		t.Fatal("callback ran after root open failure")
		return nil, nil
	})
	require.ErrorContains(t, err, "root open failed")
	t.Cleanup(func() {
		localGitStateSeam = previousGitState
		flockTryLockContextSeam = previousLock
		openQualifiedProjectReaderSeam = previousOpenReader
	})
}

func TestQualifiedAssetListErrorsAreClosed(t *testing.T) {
	const projectID = "qualified-read-list-errors"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}
	write := func(relative, body string) {
		t.Helper()
		file := filepath.Join(dir, relative)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(body), 0o600))
	}
	write("boards/bad name.board.json", `{}`)
	if _, err := LocalProjectBoards(context.Background(), ref, branch); err == nil {
		t.Fatal("invalid board ID in list was accepted")
	}
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "boards")))
	write("boards", "not a directory")
	if _, err := LocalProjectBoards(context.Background(), ref, branch); err == nil {
		t.Fatal("non-directory boards path was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "boards")))
	write("entities/bad name.entity.json", `{}`)
	if _, err := LocalProjectEntities(context.Background(), ref, branch); err == nil {
		t.Fatal("invalid entity ID in list was accepted")
	}
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "entities")))
	write("entities", "not a directory")
	if _, err := LocalProjectEntities(context.Background(), ref, branch); err == nil {
		t.Fatal("non-directory entities path was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "entities")))
	write("entities/customer.entity.json", "{")
	if _, err := LocalProjectEntities(context.Background(), ref, branch); err == nil {
		t.Fatal("malformed entity document was accepted")
	}
	write("datatug-project.json", "{")
	if _, err := LocalProjectSummary(context.Background(), ref, branch); err == nil {
		t.Fatal("malformed project summary document was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "datatug-project.json")))
	if _, err := LocalProjectSummary(context.Background(), ref, branch); err == nil {
		t.Fatal("missing project summary document was accepted")
	}
	write("datatug-project.json", `{"title":"ok"}`)
	write("environments", "not a directory")
	if _, err := LocalProjectEnvironments(context.Background(), ref, branch); err == nil {
		t.Fatal("non-directory environments path was accepted")
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "environments")))
	write("environments/not valid/environment-summary.json", `{}`)
	if _, err := LocalProjectEnvironments(context.Background(), ref, branch); err == nil {
		t.Fatal("invalid environment ID was accepted")
	}

	// An invalid entity also makes the overview fail closed rather than
	// returning a partial summary.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, "environments")))
	write("datatug-project.json", `{"title":"ok"}`)
	write("entities/broken.entity.json", "{")
	if _, err := LocalProjectSummary(context.Background(), ref, branch); err == nil {
		t.Fatal("summary with an unreadable entity was accepted")
	}
	if _, err := LocalProjectBoard(context.Background(), dto.ProjectItemRef{ProjectRef: ref, ID: "bad id"}, branch); err == nil {
		t.Fatal("invalid board detail ID was accepted")
	}
	if _, err := LocalProjectEntity(context.Background(), dto.ProjectItemRef{ProjectRef: ref, ID: "bad id"}, branch); err == nil {
		t.Fatal("invalid entity detail ID was accepted")
	}
}
