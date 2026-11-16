package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/strongo/random"
	"github.com/strongo/validation"
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

var generateBoardID = func() string { return random.ID(9) }

// Discovery and creation accept the same unambiguous native target. Reject
// malformed escaping rather than silently dropping a selector via URL.Query.
func guardedBoardCreateSelector(raw string) (url.Values, error) {
	query, err := url.ParseQuery(raw)
	if err == nil {
		for key, values := range query {
			if len(values) != 1 || (key != "storage" && key != "project" && key != "branch") {
				err = errors.New("ambiguous or unknown selector")
				break
			}
		}
	}
	if err != nil {
		return nil, validation.NewBadRequestError(errors.New("invalid Board creation selector"))
	}
	return query, nil
}

// createBoard handles board creation endpoint
func createBoard(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectRef
	// Full Board validation runs during decode, before the worker is called.
	// Seed its server-owned identity now and reassert it after decoding, so a
	// client-supplied ID can never select the storage target.
	id := generateBoardID()
	board := datatug.Board{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}}}
	createProjectItem(w, r, &ref, &board, func(ctx context.Context) (apicore.ResponseDTO, error) {
		query, err := guardedBoardCreateSelector(r.URL.RawQuery)
		if err != nil {
			return nil, err
		}
		board.ID = id
		return api.CreateLocalProjectBoard(ctx, ref, query.Get("branch"), board)
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
