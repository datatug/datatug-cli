package endpoints

import (
	"encoding/json"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/julienschmidt/httprouter"
)

// openQueryResults opens the result store; a seam for tests. The daemon and
// `serve` use the same fixed root as `query run --result-id`.
var openQueryResults = queryresults.OpenDefault

// resultsRoutes registers GET /datatug/results/:id, a read of a stored query
// result. It opens no database and runs no query.
func resultsRoutes(path string, router router, wrap wrapper, writeOnly bool) {
	if writeOnly {
		return
	}
	route(router, wrap, http.MethodGet, path+"/results/:id", resultHandler)
}

// resultResponse is the answer of GET /datatug/results/:id: meta.json as it
// was written and the recordset of data.json.
type resultResponse struct {
	Meta      json.RawMessage       `json:"meta"`
	Recordset apicontract.Recordset `json:"recordset"`
	// Query and Prompt repeat meta.query and the text of meta.prompt for
	// readers that want them without parsing meta. Both are additive: Prompt
	// is plain text and absent until `query results annotate` recorded one.
	Query  queryresults.Query `json:"query"`
	Prompt string             `json:"prompt,omitempty"`
}

// The messages of a refused result: reason codes, never an id or a path.
const (
	resultNotFound           = "result-not-found"
	resultIncomplete         = "result-incomplete"
	resultVersionUnsupported = "result-version-unsupported"
	resultRefused            = "result-refused"
	resultStoreUnavailable   = "results-store-unavailable"
)

// resultHandler is GET /datatug/results/:id. It refuses an id outside the
// grammar, a result directory that is a link or not the user's (nothing is
// read through it), and answers result-not-found for an absent or expired
// result and result-incomplete for one whose files are not complete.
func resultHandler(w http.ResponseWriter, r *http.Request) {
	id := httprouter.ParamsFromContext(r.Context()).ByName("id")
	if !queryresults.ValidID(id) {
		writeContractError(w, r, newInvalidRequest("id", queryresults.ReasonBadID))
		return
	}
	store, err := openQueryResults()
	if err != nil {
		writeContractError(w, r, newContractError(apicontract.ErrCodeSourceUnavailable, resultStoreUnavailable, "results"))
		return
	}
	loaded, err := store.Load(id)
	switch queryresults.ReasonOf(err) {
	case "":
		writeContractResponse(w, r, nil, resultResponseOf(loaded))
	case queryresults.ReasonNotFound, queryresults.ReasonExpired:
		writeContractError(w, r, newContractError(apicontract.ErrCodeNotFound, resultNotFound, "id"))
	case queryresults.ReasonIncomplete:
		writeContractError(w, r, newContractError(apicontract.ErrCodeNotFound, resultIncomplete, "id"))
	case queryresults.ReasonVersion:
		writeContractError(w, r, newInvalidRequest("id", resultVersionUnsupported))
	case queryresults.ReasonSymlink, queryresults.ReasonOwner:
		writeContractError(w, r, newContractError(apicontract.ErrCodeAccessDenied, resultRefused, "id"))
	default:
		writeContractError(w, r, newContractError(apicontract.ErrCodeSourceUnavailable, resultStoreUnavailable, "results"))
	}
}

func resultResponseOf(loaded queryresults.Loaded) resultResponse {
	response := resultResponse{Meta: loaded.MetaJSON, Recordset: loaded.Recordset, Query: loaded.Meta.Query}
	if loaded.Meta.Prompt != nil {
		response.Prompt = loaded.Meta.Prompt.Text
	}
	return response
}
