package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func nonGitDiscoveryFixture(t *testing.T) (dto.EnvironmentScope, Capabilities, string) {
	t.Helper()
	scope, caps := environmentFixture(t)
	root, err := servedProjectDir(scope.ProjectID)
	require.NoError(t, err)
	for _, id := range []string{"dev", "prod"} {
		dir := filepath.Join(root, "environments", id)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		body := `{"id":"` + id + `","title":"` + id + `","vars":{"secret":"private-value"},"editionConnections":["private-connection"],"presentation":{"label":"Readable","tone":"success"}}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, id+".env.json"), []byte(body), 0o600))
	}
	return scope, caps, root
}

func TestNonGitEnvironmentDiscoveryReaderAndOwnerVisibility(t *testing.T) {
	scope, caps, root := nonGitDiscoveryFixture(t)
	ctx := context.Background()
	paths := map[string]string{scope.ProjectID: root}
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}
	reader := secureread.Session{Principal: &access.Principal{ID: "governed-reader"}}
	ConfigureSecureSession(reader, paths, caps)
	state, err := LocalEnvironmentState(ctx, scope)
	require.NoError(t, err)
	require.Nil(t, state.Scope.ResolvedBranch)
	require.False(t, state.Capabilities.CanWriteOrder)
	active, err := LocalProjectEnvironments(ctx, ref, "")
	require.NoError(t, err)
	require.Len(t, active, 2)
	body, err := json.Marshal(active)
	require.NoError(t, err)
	require.NotContains(t, string(body), "private-value")
	require.NotContains(t, string(body), "private-connection")
	require.NotContains(t, string(body), "vars")
	require.NotContains(t, string(body), "editionConnections")
	require.Contains(t, string(body), `"presentation":{"label":"Readable","tone":"success"}`)
	selection := LocalLifecycleSelection{ProjectRef: ref}
	_, err = LocalProjectLifecycleCatalog(ctx, selection, false)
	require.ErrorIs(t, err, secureread.ErrAccessDenied)
	_, err = LocalCreateEnvironment(ctx, dto.CreateEnvironmentRequest{Name: "Forbidden", EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope}})
	require.Error(t, err)

	// The real owner changes visibility through the same lifecycle API. Its
	// sidecar must never delete or rewrite the definition used by direct loads.
	owner, err := secureread.NewSession(secureread.SessionOptions{As: "actor-A", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(owner, paths, caps)
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	definition := filepath.Join(root, "environments", "prod", "prod.env.json")
	original, err := os.ReadFile(definition)
	require.NoError(t, err)
	object := datatug.ProjectObjectRef{Kind: "environment", ID: "prod"}
	for i, action := range []string{"archive", "restore"} {
		review, err := LocalProjectLifecycleReview(ctx, selection, object)
		require.NoError(t, err)
		_, err = LocalProjectLifecycleChange(ctx, LocalLifecycleWrite{LocalLifecycleSelection: selection, ProjectLifecycleWrite: datatug.ProjectLifecycleWrite{
			OperationID: "environment-visibility-" + action, ExpectedRevision: review.Revision,
			Change: datatug.ProjectLifecycleChange{Object: object, Action: action},
		}})
		require.NoError(t, err)
		ConfigureSecureSession(reader, paths, caps)
		active, err = LocalProjectEnvironments(ctx, ref, "")
		require.NoError(t, err)
		require.Len(t, active, i+1)
		state, err = LocalEnvironmentState(ctx, scope)
		require.NoError(t, err)
		require.Len(t, state.Environments, 2)
		require.Len(t, state.Order, 2)
		direct, err := filestore.NewProjectStore(scope.ProjectID, root).LoadEnvironment(ctx, "prod")
		require.NoError(t, err)
		require.Equal(t, "prod", direct.ID)
		current, err := os.ReadFile(definition)
		require.NoError(t, err)
		require.Equal(t, original, current)
		_, err = LocalProjectLifecycleCatalog(ctx, selection, true)
		require.ErrorIs(t, err, secureread.ErrAccessDenied)
		ConfigureSecureSession(owner, paths, caps)
	}
	_, err = os.Stat(filepath.Join(root, ".git"))
	require.True(t, os.IsNotExist(err), "a discovery read must not initialize Git")
}

func TestNonGitEnvironmentDiscoveryRejectsChangedAuthority(t *testing.T) {
	for _, changed := range []string{"actor", "session", "registered-root", "physical-root", "physical-root-before-open"} {
		t.Run(changed, func(t *testing.T) {
			scope, caps, root := nonGitDiscoveryFixture(t)
			paths := map[string]string{scope.ProjectID: root}
			session := secureread.Session{Principal: &access.Principal{ID: "actor-A"}}
			ConfigureSecureSession(session, paths, caps)
			outside := t.TempDir()
			originalOpen := openQualifiedProjectReaderSeam
			t.Cleanup(func() { openQualifiedProjectReaderSeam = originalOpen })
			openQualifiedProjectReaderSeam = func(dir string) (*qualifiedProjectReader, error) {
				if changed == "physical-root-before-open" {
					moved := root + "-moved"
					require.NoError(t, os.Rename(root, moved))
					t.Cleanup(func() { _ = os.RemoveAll(moved) })
					dir := filepath.Join(root, "environments", "replacement")
					require.NoError(t, os.MkdirAll(dir, 0o700))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "replacement.env.json"), []byte(`{"id":"replacement","title":"Foreign root"}`), 0o600))
				}
				reader, err := originalOpen(dir)
				if err != nil {
					return nil, err
				}
				switch changed {
				case "actor":
					ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-B"}}, paths, caps)
				case "session":
					ConfigureSecureSession(session, paths, caps)
				case "registered-root":
					ConfigureSecureSession(session, map[string]string{scope.ProjectID: outside}, caps)
				case "physical-root":
					moved := root + "-moved"
					require.NoError(t, os.Rename(root, moved))
					t.Cleanup(func() { _ = os.RemoveAll(moved) })
					require.NoError(t, os.Symlink(outside, root))
				}
				return reader, nil
			}
			rows, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}, "")
			require.Error(t, err)
			require.Nil(t, rows, "a refused read must not publish even a partial presentation")
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestNonGitEnvironmentDiscoveryRefusesInvalidOrUnverifiableReads(t *testing.T) {
	scope, caps, root := nonGitDiscoveryFixture(t)
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}
	paths := map[string]string{scope.ProjectID: root}
	for _, invalid := range []dto.ProjectRef{{}, {StoreID: "foreign", ProjectID: scope.ProjectID}, {StoreID: LocalStoreID, ProjectID: "unserved"}} {
		rows, err := LocalProjectEnvironments(context.Background(), invalid, "")
		require.Error(t, err)
		require.Nil(t, rows)
	}
	caps.EnvironmentMetadataPolicies[scope.ProjectID] = EnvironmentMetadataPolicy{PrincipalID: "actor-A"}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
	rows, err := LocalProjectEnvironments(context.Background(), ref, "")
	require.Error(t, err)
	require.Nil(t, rows)
	caps.EnvironmentMetadataPolicies[scope.ProjectID] = EnvironmentMetadataPolicy{PrincipalID: "actor-A", CanReadOrder: true}
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, paths, caps)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte(`{"version":999}`), 0o600))
	rows, err = LocalProjectEnvironments(context.Background(), ref, "")
	require.Error(t, err)
	require.Nil(t, rows)

	gitRoot, _ := setupTestGitProject(t, "git-needs-branch")
	configureBranchReadTest(t, "git-needs-branch", gitRoot)
	rows, err = LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: "git-needs-branch"}, "")
	require.ErrorContains(t, err, "branch is required")
	require.Nil(t, rows)
}

func TestNonGitEnvironmentDiscoveryOpenFailure(t *testing.T) {
	scope, _, _ := nonGitDiscoveryFixture(t)
	original := openQualifiedProjectReaderSeam
	t.Cleanup(func() { openQualifiedProjectReaderSeam = original })
	failure := errors.New("registered root became unavailable")
	openQualifiedProjectReaderSeam = func(string) (*qualifiedProjectReader, error) { return nil, failure }
	rows, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}, "")
	require.ErrorIs(t, err, failure)
	require.Nil(t, rows)
}

func TestNonGitEnvironmentDiscoveryRejectsNewGitState(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unborn-repository", true: "malformed-repository"}[malformed], func(t *testing.T) {
			scope, _, root := nonGitDiscoveryFixture(t)
			original := openQualifiedProjectReaderSeam
			t.Cleanup(func() { openQualifiedProjectReaderSeam = original })
			openQualifiedProjectReaderSeam = func(dir string) (*qualifiedProjectReader, error) {
				reader, err := original(dir)
				if err != nil {
					return nil, err
				}
				if malformed {
					require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("not a git directory"), 0o600))
				} else {
					out, err := exec.Command("git", "-C", root, "init", "-q", "-b", "main").CombinedOutput()
					require.NoError(t, err, string(out))
				}
				return reader, nil
			}
			rows, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}, "")
			if malformed {
				var refusal *EnvironmentError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, "TEMPORARILY_UNAVAILABLE", refusal.Code)
			} else {
				require.ErrorContains(t, err, "branch is required")
			}
			require.Nil(t, rows, "new or unverifiable Git state needs branch admission")
		})
	}
}

func TestNonGitEnvironmentDiscoveryMissingRootAfterLock(t *testing.T) {
	scope, _, root := nonGitDiscoveryFixture(t)
	original := environmentReadFilesSeam
	t.Cleanup(func() { environmentReadFilesSeam = original })
	environmentReadFilesSeam = func(ctx context.Context, dir string, fn func(*filestore.EnvironmentFiles) error) error {
		return original(ctx, dir, func(files *filestore.EnvironmentFiles) error {
			moved := root + "-moved"
			require.NoError(t, os.Rename(root, moved))
			t.Cleanup(func() { _ = os.RemoveAll(moved) })
			return fn(files)
		})
	}
	rows, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}, "")
	require.Error(t, err)
	require.Nil(t, rows)
}

func TestNonGitEnvironmentDiscoveryRootReplacementAfterLock(t *testing.T) {
	scope, _, root := nonGitDiscoveryFixture(t)
	original := environmentReadFilesSeam
	t.Cleanup(func() { environmentReadFilesSeam = original })
	environmentReadFilesSeam = func(ctx context.Context, dir string, fn func(*filestore.EnvironmentFiles) error) error {
		return original(ctx, dir, func(files *filestore.EnvironmentFiles) error {
			moved := root + "-moved"
			require.NoError(t, os.Rename(root, moved))
			t.Cleanup(func() { _ = os.RemoveAll(moved) })
			replacement := filepath.Join(root, "environments", "replacement")
			require.NoError(t, os.MkdirAll(replacement, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(replacement, "replacement.env.json"), []byte(`{"id":"replacement","title":"Other root"}`), 0o600))
			return fn(files)
		})
	}
	rows, err := LocalProjectEnvironments(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}, "")
	var refusal *EnvironmentError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "ACCESS_DENIED", refusal.Code)
	require.Nil(t, rows)
}

func TestNonGitEnvironmentDiscoveryInitialRootAndPrincipalRefusal(t *testing.T) {
	scope, caps, root := nonGitDiscoveryFixture(t)
	ref := dto.ProjectRef{StoreID: LocalStoreID, ProjectID: scope.ProjectID}
	ConfigureSecureSession(secureread.Session{}, map[string]string{scope.ProjectID: root}, caps)
	rows, err := LocalProjectEnvironments(context.Background(), ref, "")
	var refusal *EnvironmentError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "ACCESS_DENIED", refusal.Code)
	require.Nil(t, rows)
	ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "actor-A"}}, map[string]string{scope.ProjectID: filepath.Join(root, "missing")}, caps)
	rows, err = LocalProjectEnvironments(context.Background(), ref, "")
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "PROJECT_NOT_FOUND", refusal.Code)
	require.NotContains(t, err.Error(), root)
	require.Nil(t, rows)
}
