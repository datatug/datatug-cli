package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/strongo/random"
)

// getBoard handles get board endpoint
func getBoard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("branch") {
		if err := api.RequireLocalReadPrincipal(); err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		ref, err := newProjectItemRef(r.URL.Query())
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		board, err := api.LocalProjectBoard(r.Context(), ref, r.URL.Query().Get("branch"))
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		writeProjectMutationJSON(w, r, http.StatusOK, board)
		return
	}
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetBoard(ctx, ref)
	})
}

func getBoards(w http.ResponseWriter, r *http.Request) {
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	briefs, err := api.LocalProjectBoards(r.Context(), ref, r.URL.Query().Get("branch"))
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, briefs)
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
