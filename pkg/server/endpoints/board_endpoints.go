package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/datatug-core/datatug"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/strongo/random"
)

// getBoard handles get board endpoint
func getBoard(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetBoard(ctx, ref)
	})
}

// createBoard handles board creation endpoint
func createBoard(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectRef
	var board datatug.Board
	//board.ID = datatug.AutoID
	createProjectItem(w, r, &ref, &board, func(ctx context.Context) (apicore.ResponseDTO, error) {
		board.ID = random.ID(9)
		return api.CreateBoard(ctx, ref, board)
	})
}

// saveBoard handles save board endpoint
func saveBoard(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	var board datatug.Board
	saveProjectItem(w, r, &ref, &board, func(ctx context.Context) (apicore.ResponseDTO, error) {
		return api.SaveBoard(ctx, ref.ProjectRef, board)
	})
}

// deleteBoard handles delete board endpoint
var deleteBoard = deleteProjItem(api.DeleteBoard)
