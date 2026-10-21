package endpoints

import (
	"errors"
	"io"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/validation"
)

var localMeaningsCatalog = api.LocalProjectMeaningsCatalog
var localMeaningChange = api.LocalProjectMeaningChange

func projectMeaningsCatalogHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ref, err := newProjectRef(r.URL.Query())
	if err == nil {
		err = ref.Validate()
	}
	if err != nil {
		writeProjectMutationError(w, r, err)
		return
	}
	catalog, err := localMeaningsCatalog(r.Context(), ref, r.URL.Query().Get("branch"))
	if err != nil {
		writeMeaningError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, catalog)
}
func projectMeaningChangeHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		writeMeaningError(w, r, validation.NewBadRequestError(errors.New("meaning request too large")))
		return
	}
	var request api.LocalMeaningWrite
	if err = decodeContractBody(body, &request); err != nil {
		writeMeaningError(w, r, validation.NewBadRequestError(err))
		return
	}
	ack, err := localMeaningChange(r.Context(), request)
	if err != nil {
		writeMeaningError(w, r, err)
		return
	}
	writeProjectMutationJSON(w, r, http.StatusOK, ack)
}
func writeMeaningError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, datatug.ErrMeaningConflict), errors.Is(err, datatug.ErrMeaningReferenced), errors.Is(err, datatug.ErrMeaningIncomplete), errors.Is(err, datatug.ErrMeaningOperationConflict):
		code := "MEANING_REVISION_CONFLICT"
		if errors.Is(err, datatug.ErrMeaningReferenced) {
			code = "MEANING_REFERENCED"
		}
		if errors.Is(err, datatug.ErrMeaningIncomplete) {
			code = "REFERENCES_INCOMPLETE"
		}
		if errors.Is(err, datatug.ErrMeaningOperationConflict) {
			code = "OPERATION_CONFLICT"
		}
		writeProjectMutationJSON(w, r, http.StatusConflict, map[string]any{"error": map[string]string{"code": code, "message": "meaning change refused; reload and check references"}})
	default:
		writeProjectMutationError(w, r, err)
	}
}
