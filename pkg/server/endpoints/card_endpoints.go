package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// getCard handles the get card endpoint. With a branch it reads that checked-out
// branch, like getBoard does.
func getCard(w http.ResponseWriter, r *http.Request) {
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
		card, err := api.LocalProjectCard(r.Context(), ref, r.URL.Query().Get("branch"))
		if err != nil {
			writeProjectMutationError(w, r, err)
			return
		}
		writeProjectMutationJSON(w, r, http.StatusOK, card)
		return
	}
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return api.GetCard(ctx, ref)
	})
}

// getCards handles the all cards endpoint: the briefs of the project's cards.
func getCards(w http.ResponseWriter, r *http.Request) {
	if err := api.RequireLocalReadPrincipal(); err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	var briefs []datatug.CardBrief
	if r.URL.Query().Has("branch") {
		briefs, err = api.LocalProjectCards(r.Context(), ref, r.URL.Query().Get("branch"))
	} else {
		briefs, err = api.ListCards(r.Context(), ref)
	}
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, briefs)
}

// createCard handles the card creation endpoint; the card carries its own ID.
func createCard(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectRef
	var card datatug.Card
	createProjectItem(w, r, &ref, &card, func(ctx context.Context) (apicore.ResponseDTO, error) {
		return api.CreateCard(ctx, ref, card)
	})
}

// saveCard handles the save card endpoint.
func saveCard(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	var card datatug.Card
	saveProjectItem(w, r, &ref, &card, func(ctx context.Context) (apicore.ResponseDTO, error) {
		return api.SaveCard(ctx, ref.ProjectRef, card)
	})
}

// deleteCard handles the delete card endpoint.
var deleteCard = deleteProjItem(api.DeleteCard)
