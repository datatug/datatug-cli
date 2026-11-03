package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func contextualFixture(t *testing.T) (string, secureread.Session, ContextualEnvironmentScope) {
	t.Helper()
	root, _, session := apiBoundFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"binding","title":"Bound"}`), 0600))
	ConfigureSecureSession(session, map[string]string{"binding": root}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	return root, session, ContextualEnvironmentScope{ProjectID: "binding", EnvironmentID: "local", CatalogID: "selected"}
}
func TestBoundContextualParentChildArchiveRestore(t *testing.T) {
	root, _, scope := contextualFixture(t)
	registry, err := localprojects.Open("", map[string]string{"binding": root})
	require.NoError(t, err)
	for _, parentArchived := range []bool{false, true, false} {
		if parentArchived {
			_, err = registry.SetArchived(context.Background(), localprojects.Archive{Storage: "local", Project: "binding", Archived: true, OperationID: "archive"})
		} else if _, err = os.Stat(filepath.Join(root, ".datatug-project-state.json")); err == nil {
			_, err = registry.SetArchived(context.Background(), localprojects.Archive{Storage: "local", Project: "binding", OperationID: "restore"})
		} else {
			err = nil
		}
		require.NoError(t, err)
		for _, childArchived := range []bool{false, true, false} {
			visibility := `{"version":1,"archived":[]}`
			if childArchived {
				visibility = `{"version":1,"archived":[{"kind":"environment","id":"local"}]}`
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte(visibility), 0600))
			view, err := BoundContextualEnvironments(context.Background(), scope)
			require.NoError(t, err)
			require.Equal(t, "available", view.Status)
			require.Equal(t, scope, view.Scope)
			require.Equal(t, parentArchived, view.Parent.Archived)
			require.True(t, view.Parent.ReadOnly)
			require.NotNil(t, view.Items)
			if childArchived {
				require.Empty(t, *view.Items)
			} else {
				require.Len(t, *view.Items, 1)
				require.Equal(t, "local", (*view.Items)[0].ID)
			}
			wire, err := json.Marshal(view)
			require.NoError(t, err)
			require.NotContains(t, string(wire), "sqlite")
			require.NotContains(t, string(wire), "dbServers")
		}
	}
}
func TestBoundContextualMetadataUnavailableIsNotEmpty(t *testing.T) {
	for _, metadata := range []string{"parent-malformed", "parent-unsupported", "parent-directory", "child-malformed", "parent-deleted", "open-error", "lock-error"} {
		t.Run(metadata, func(t *testing.T) {
			root, _, scope := contextualFixture(t)
			switch metadata {
			case "parent-malformed":
				require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-project-state.json"), []byte("{"), 0600))
			case "parent-unsupported":
				require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-project-state.json"), []byte(`{"version":2,"archived":false}`), 0600))
			case "parent-directory":
				require.NoError(t, os.Mkdir(filepath.Join(root, ".datatug-project-state.json"), 0700))
			case "child-malformed":
				require.NoError(t, os.WriteFile(filepath.Join(root, ".datatug-visibility.json"), []byte("{"), 0600))
			case "parent-deleted":
				require.NoError(t, os.Remove(filepath.Join(root, "datatug-project.json")))
			case "open-error":
				old := openQualifiedProjectReaderSeam
				t.Cleanup(func() { openQualifiedProjectReaderSeam = old })
				openQualifiedProjectReaderSeam = func(string) (*qualifiedProjectReader, error) { return nil, errors.New("private path detail") }
			case "lock-error":
				old := environmentReadFilesSeam
				t.Cleanup(func() { environmentReadFilesSeam = old })
				environmentReadFilesSeam = func(context.Context, string, func(*filestore.EnvironmentFiles) error) error {
					return errors.New("private lock detail")
				}
			}
			view, err := BoundContextualEnvironments(context.Background(), scope)
			require.NoError(t, err)
			require.Equal(t, "unavailable", view.Status)
			require.Equal(t, "TEMPORARILY_UNAVAILABLE", view.ReasonCode)
			require.Nil(t, view.Items)
			require.Nil(t, view.Parent)
		})
	}
}
func TestBoundContextualRejectsForeignAndLateAuthority(t *testing.T) {
	for _, change := range []string{"foreign-project", "foreign-env", "foreign-catalog", "canceled", "session", "registry", "physical-root", "policy"} {
		t.Run(change, func(t *testing.T) {
			root, session, scope := contextualFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch change {
			case "foreign-project":
				scope.ProjectID = "foreign"
			case "foreign-env":
				scope.EnvironmentID = "foreign"
			case "foreign-catalog":
				scope.CatalogID = "foreign"
			case "canceled":
				cancel()
			default:
				old := openQualifiedProjectReaderSeam
				t.Cleanup(func() { openQualifiedProjectReaderSeam = old })
				openQualifiedProjectReaderSeam = func(dir string) (*qualifiedProjectReader, error) {
					reader, err := old(dir)
					if err != nil {
						return nil, err
					}
					switch change {
					case "session":
						ConfigureSecureSession(session, map[string]string{"binding": root}, Capabilities{})
					case "registry":
						ConfigureSecureSession(session, map[string]string{"binding": t.TempDir()}, Capabilities{})
					case "physical-root":
						moved := root + "-moved"
						require.NoError(t, os.Rename(root, moved))
						t.Cleanup(func() { _ = os.RemoveAll(moved) })
						require.NoError(t, os.Mkdir(root, 0700))
					case "policy":
						require.NoError(t, os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("revoked"), 0600))
					}
					return reader, nil
				}
			}
			view, err := BoundContextualEnvironments(ctx, scope)
			require.Error(t, err)
			require.NotEqual(t, "available", view.Status)
			require.Nil(t, view.Items)
		})
	}
}
func TestBoundContextualRefusesLateBranchChange(t *testing.T) {
	_, _, scope := contextualFixture(t)
	old := localGitStateSeam
	t.Cleanup(func() { localGitStateSeam = old })
	calls := 0
	localGitStateSeam = func(context.Context, string) (gitState, error) {
		calls++
		if calls == 1 {
			return gitState{branch: "main", head: "before"}, nil
		}
		return gitState{branch: "other", head: "after"}, nil
	}
	view, err := BoundContextualEnvironments(context.Background(), scope)
	require.NoError(t, err)
	require.Equal(t, "unavailable", view.Status)
	require.Nil(t, view.Items)
}

func TestBoundContextualGitProbeRechecksAuthorityOnMetadataFailure(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata-only", true: "source-revoked"}[revoked], func(t *testing.T) {
			root, _, scope := contextualFixture(t)
			old := localGitStateSeam
			t.Cleanup(func() { localGitStateSeam = old })
			localGitStateSeam = func(context.Context, string) (gitState, error) {
				if revoked {
					require.NoError(t, os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("revoked"), 0600))
				}
				return gitState{}, environmentError("TEMPORARILY_UNAVAILABLE", 503)
			}
			view, err := BoundContextualEnvironments(context.Background(), scope)
			if revoked {
				require.ErrorIs(t, err, secureread.ErrAccessDenied)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, "unavailable", view.Status)
			require.Nil(t, view.Items)
		})
	}
}

func TestBoundContextualUnavailableSourceDocuments(t *testing.T) {
	for _, kind := range []string{"null-parent", "invalid-environment", "invalid-presentation"} {
		t.Run(kind, func(t *testing.T) {
			root, _, scope := contextualFixture(t)
			if kind == "null-parent" {
				require.NoError(t, os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte("null"), 0600))
			} else {
				file := filepath.Join(root, "environments", "local", "local.env.json")
				body, err := os.ReadFile(file)
				require.NoError(t, err)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(body, &fields))
				if kind == "invalid-environment" {
					fields["id"] = "foreign"
				} else {
					fields["presentation"] = map[string]any{"label": 42}
				}
				body, err = json.Marshal(fields)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(file, body, 0600))
				session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
				require.NoError(t, err)
				require.True(t, declared)
				ConfigureSecureSession(session, map[string]string{"binding": root}, Capabilities{})
			}
			view, err := BoundContextualEnvironments(context.Background(), scope)
			require.NoError(t, err)
			require.Equal(t, "unavailable", view.Status)
			require.Nil(t, view.Items)
		})
	}
}
func TestBoundContextualFilesystemAndAuthorityBoundaries(t *testing.T) {
	for _, kind := range []string{"unregistered", "stat-denied", "not-directory", "wrong-registered-inode", "waiting-read-revoked", "read-denied", "after-git-revoked", "after-git-error-revoked"} {
		t.Run(kind, func(t *testing.T) {
			root, _, scope := contextualFixture(t)
			switch kind {
			case "unregistered":
				secureMu.Lock()
				delete(projectDirs, "binding")
				secureMu.Unlock()
			case "stat-denied", "not-directory", "wrong-registered-inode":
				old := nonGitDiscoveryRootStat
				t.Cleanup(func() { nonGitDiscoveryRootStat = old })
				nonGitDiscoveryRootStat = func(string) (os.FileInfo, error) {
					if kind == "stat-denied" {
						return nil, os.ErrPermission
					}
					if kind == "not-directory" {
						return os.Stat(filepath.Join(root, "datatug-project.json"))
					}
					return os.Stat(t.TempDir())
				}
			case "waiting-read-revoked", "read-denied":
				old := environmentReadFilesSeam
				t.Cleanup(func() { environmentReadFilesSeam = old })
				environmentReadFilesSeam = func(ctx context.Context, dir string, work func(*filestore.EnvironmentFiles) error) error {
					if kind == "read-denied" {
						return secureread.ErrAccessDenied
					}
					return old(ctx, dir, func(f *filestore.EnvironmentFiles) error {
						require.NoError(t, os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("revoked"), 0600))
						return work(f)
					})
				}
			case "after-git-revoked", "after-git-error-revoked":
				oldRead, oldGit := environmentReadFilesSeam, localGitStateSeam
				t.Cleanup(func() { environmentReadFilesSeam, localGitStateSeam = oldRead, oldGit })
				readDone := false
				environmentReadFilesSeam = func(ctx context.Context, dir string, work func(*filestore.EnvironmentFiles) error) error {
					err := oldRead(ctx, dir, work)
					readDone = true
					return err
				}
				localGitStateSeam = func(context.Context, string) (gitState, error) {
					if readDone {
						require.NoError(t, os.WriteFile(filepath.Join(root, "policies", "read.yaml"), []byte("revoked"), 0600))
					}
					if readDone && kind == "after-git-error-revoked" {
						return gitState{}, environmentError("TEMPORARILY_UNAVAILABLE", 503)
					}
					return gitState{}, errors.New("not a Git checkout")
				}
			}
			view, err := BoundContextualEnvironments(context.Background(), scope)
			require.ErrorIs(t, err, secureread.ErrAccessDenied)
			require.NotEqual(t, "available", view.Status)
			require.Nil(t, view.Items)
		})
	}
}
