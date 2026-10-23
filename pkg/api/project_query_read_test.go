package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func TestLocalGetQueryRefusesBranchSwitchDuringStoreRead(t *testing.T) {
	dir := t.TempDir()
	const projectID = "branch-read-test"
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"),
		[]byte(`{"id":"branch-read-test","title":"Branch read test","access":"private","created":{"at":"2026-01-01T00:00:00Z"}}`), 0o600))
	query := datatug.QueryDefWithFolderPath{}
	query.ID, query.Title, query.Type, query.Text = "customer", "Customers", datatug.QueryTypeTugQL, "SELECT CustomerId FROM Customer"
	data, err := os.ReadFile("testdata/query_semantic_model.json")
	require.NoError(t, err)
	var semantic datatug.QuerySemanticModel
	require.NoError(t, json.Unmarshal(data, &semantic))
	query.SemanticModel = &semantic
	store := filestore.NewProjectStore(projectID, dir).(datatug.RevisionedQueriesStore)
	_, err = store.PutQuery(context.Background(), &query, datatug.QueryWriteCondition{IfNoneMatch: true})
	require.NoError(t, err)
	git("add", "-A")
	git("commit", "-q", "-m", "branch A")
	branchA := git("symbolic-ref", "--short", "HEAD")
	git("switch", "-q", "-c", "branch-b")
	query.Text = "SELECT FirstName FROM Customer"
	current, err := store.LoadQueryRevision(context.Background(), "customer")
	require.NoError(t, err)
	_, err = store.PutQuery(context.Background(), &query, datatug.QueryWriteCondition{IfMatch: current.Revision})
	require.NoError(t, err)
	git("add", "-A")
	git("commit", "-q", "-m", "branch B")
	git("switch", "-q", branchA)

	pathsByID := map[string]string{projectID: dir}
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", pathsByID) }
	session, err := secureread.NewSession(secureread.SessionOptions{As: "admin", NoPolicies: true})
	require.NoError(t, err)
	ConfigureSecureSession(session, pathsByID, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	capabilities, err := (LocalProjectQueryAdapter{}).Capabilities(context.Background(), dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID})
	require.NoError(t, err)
	require.True(t, capabilities.SemanticModel)
	preserved, err := (LocalProjectQueryAdapter{}).GetQuery(context.Background(), dto.GetQueryRequest{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "customer", Branch: branchA})
	require.NoError(t, err)
	require.Equal(t, &semantic, preserved.Query.SemanticModel)
	previous := loadLocalQueryRevision
	loadLocalQueryRevision = func(ctx context.Context, revisioned datatug.RevisionedQueriesStore, id string) (*datatug.StoredQuery, error) {
		git("switch", "-q", "branch-b")
		return previous(ctx, revisioned, id)
	}
	t.Cleanup(func() { loadLocalQueryRevision = previous })
	_, err = (LocalProjectQueryAdapter{}).GetQuery(context.Background(), dto.GetQueryRequest{
		ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "customer", Branch: branchA,
	})
	require.ErrorIs(t, err, dto.ErrBranchHeadConflict)
	require.True(t, errors.Is(err, dto.ErrBranchHeadConflict))
}
