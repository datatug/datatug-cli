package endpoints

import (
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	knowledge "github.com/datatug/datatug-core/pkg/detectiveknowledge"
)

func detectiveKnowledgeProposalsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	store, _, err := detectiveStore(query.Get("project"), query.Get("securityContextId"))
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	page, err := store.KnowledgePage(r.Context(), query.Get("after"))
	writeContractResponse(w, r, detectiveError(err), page)
}

func detectiveKnowledgeActionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var request knowledge.Action
	if err := readIncidentBody(w, r, &request); err != nil {
		writeContractError(w, r, err)
		return
	}
	query := r.URL.Query()
	if request.Project != query.Get("project") || request.Storage != "local" || request.SpaceID != "" {
		writeContractError(w, r, newInvalidRequest("project", "invalid local knowledge scope"))
		return
	}
	store, actor, err := detectiveStore(request.Project, query.Get("securityContextId"))
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	response, err := store.KnowledgeAction(r.Context(), request, actor)
	writeContractResponse(w, r, detectiveError(err), response)
}
