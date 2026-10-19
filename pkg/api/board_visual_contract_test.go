package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/require"
)

// The CLI's branch-qualified project reader must preserve the published board
// wire contract when a paired chart links to a held SQL result. This is a
// consumer read, not another set of Core widget-validation edge cases.
func TestLocalProjectBoardReadsPairedChartAndPresentationLink(t *testing.T) {
	const projectID = "visual-board-read"
	dir, git := setupTestGitProject(t, projectID)
	configureBranchReadTest(t, projectID, dir)
	boardPath := filepath.Join(dir, "boards", "showcase", "board.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(boardPath), 0o755))
	const boardJSON = `{
  "id":"showcase","title":"Customer countries",
  "parameters":[{"id":"top","type":"integer","defaultValue":10}],
  "rows":[{"cards":[
    {"id":"countries","title":"Shared country comparison","cols":6,
     "widget":{"name":"chart","data":{
       "sources":[{"id":"shared","queryId":"demodb/customer-country-comparison-server"}],
       "chart":{"type":"bar","categoryColumn":"country","unit":"customer records","selectionId":"customer-country",
         "series":[{"id":"northwind","title":"Northwind","valueColumn":"northwindCustomers"},
                   {"id":"chinook","title":"Chinook","valueColumn":"chinookCustomers"}]}}}},
    {"id":"orders","title":"Orders detail","cols":6,
     "widget":{"name":"SQL","data":{"sql":{"queryId":"demodb/northwind-orders-full-sqlite",
       "presentationFilter":{"selectionId":"customer-country","column":"ShipCountry"}}}}}
  ]}]
}`
	require.NoError(t, os.WriteFile(boardPath, []byte(boardJSON), 0o600))
	branch := git("symbolic-ref", "--short", "HEAD")
	ref := dto.ProjectItemRef{ProjectRef: dto.ProjectRef{StoreID: LocalStoreID, ProjectID: projectID}, ID: "showcase"}
	board, err := LocalProjectBoard(context.Background(), ref, branch)
	require.NoError(t, err)
	require.NoError(t, board.Validate())
	require.Equal(t, float64(10), board.Parameters[0].DefaultValue)

	// The reader returns a model to the CLI/API surface. Verify the fields
	// survive decoding and re-encoding rather than merely passing validation.
	wire, err := json.Marshal(board)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(wire, &decoded))
	rows := decoded["rows"].([]any)
	cards := rows[0].(map[string]any)["cards"].([]any)
	chart := cards[0].(map[string]any)["widget"].(map[string]any)["data"].(map[string]any)["chart"].(map[string]any)
	require.Equal(t, "customer-country", chart["selectionId"])
	require.Len(t, chart["series"].([]any), 2)
	orders := cards[1].(map[string]any)["widget"].(map[string]any)["data"].(map[string]any)["sql"].(map[string]any)
	require.Equal(t, map[string]any{"selectionId": "customer-country", "column": "ShipCountry"}, orders["presentationFilter"])
}
