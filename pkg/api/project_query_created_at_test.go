package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/stretchr/testify/require"
)

func TestLocalQueryCreationSurvivesLostReceiptAndColdEdit(t *testing.T) {
	t.Setenv("DATATUG_OPERATION_RECEIPTS_DIR", t.TempDir())
	ctx := context.Background()
	const id = "creation-retry"
	dir, git := setupTestGitProject(t, id)
	paths := map[string]string{id: dir}
	configureWriteSession(t, secureread.SessionOptions{As: "alice", NoPolicies: true}, paths, Capabilities{AllowWrites: true})
	originalStore := storage.NewDatatugStore
	originalWrite := writeLocalQueryJournalSeam
	t.Cleanup(func() { storage.NewDatatugStore = originalStore; writeLocalQueryJournalSeam = originalWrite })
	filestore.SetProjectPath(id, dir)
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	request := dto.SaveQueryRequest{OperationID: "create", ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: id}, Branch: git("symbolic-ref", "--short", "HEAD"), ExpectedBranchHead: git("rev-parse", "HEAD"), IfNoneMatch: true, Query: datatug.QueryDefWithFolderPath{FolderPath: "~", QueryDef: datatug.QueryDef{CreatedAt: "2099-01-01T00:00:00Z", ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q", Title: "Saved query"}}, Type: datatug.QueryTypeSQL, Text: "SELECT 1"}}}
	scope, err := SecureProjectMutationScope(request.ProjectRef, request.Branch, "save-query")
	require.NoError(t, err)
	var intended dto.SaveQueryResponse
	writes := 0
	writeLocalQueryJournalSeam = func(path string, j localQueryJournal) error {
		writes++
		intended = j.Result
		if writes == 2 {
			return errors.New("lost completion receipt")
		}
		return originalWrite(path, j)
	}
	_, err = (LocalProjectQueryAdapter{}).SaveQuery(ctx, scope, request)
	require.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
	require.NotEqual(t, request.Query.CreatedAt, intended.Query.CreatedAt)
	require.NoError(t, intended.Query.ValidateCreationMetadata())
	writeLocalQueryJournalSeam = originalWrite
	replay, err := (LocalProjectQueryAdapter{}).SaveQuery(ctx, scope, request)
	require.NoError(t, err)
	require.Equal(t, intended, *replay)
	reopened, err := (LocalProjectQueryAdapter{}).GetQuery(ctx, dto.GetQueryRequest{ProjectRef: request.ProjectRef, ID: "q", Branch: request.Branch})
	require.NoError(t, err)
	require.Equal(t, replay.Query.CreatedAt, reopened.Query.CreatedAt)
	git("commit", "-q", "-m", "created query")
	request.OperationID = "edit"
	request.ExpectedBranchHead = git("rev-parse", "HEAD")
	request.IfNoneMatch = false
	request.IfMatch = replay.Revision
	request.Query.Text = "SELECT 2"
	edited, err := (LocalProjectQueryAdapter{}).SaveQuery(ctx, scope, request)
	require.NoError(t, err)
	require.Equal(t, replay.Query.CreatedAt, edited.Query.CreatedAt)
	require.NotEqual(t, replay.Revision, edited.Revision)
	// A corrupted pending clock cannot recreate files with fabricated metadata.
	request.OperationID = "corrupt-pending-clock"
	request.IfNoneMatch, request.IfMatch = true, ""
	request.Query.ID = "other"
	_, err = (LocalProjectQueryAdapter{}).SaveQuery(ctx, scope, request)
	require.NoError(t, err)
	receiptPath, err := localMutationReceiptPath(scope.ActorID, request.OperationID)
	require.NoError(t, err)
	raw, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	var journal localQueryJournal
	require.NoError(t, json.Unmarshal(raw, &journal))
	journal.Complete, journal.Result.Query.CreatedAt = false, "invalid"
	require.NoError(t, originalWrite(receiptPath, journal))
	for _, extension := range []string{"json", "sql"} {
		require.NoError(t, os.Remove(filepath.Join(dir, "queries", "other.query."+extension)))
	}
	_, err = (LocalProjectQueryAdapter{}).SaveQuery(ctx, scope, request)
	require.ErrorIs(t, err, ErrProjectMutationOutcomeUncertain)
}
