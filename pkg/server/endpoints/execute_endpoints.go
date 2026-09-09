package endpoints

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// executeCommandsHandler handler for execute command endpoint
func executeCommandsHandler(w http.ResponseWriter, r *http.Request) {

	var executeRequest sqlexecute.Request

	executeRequest.Project = r.URL.Query().Get("project")

	switch r.Method {
	//case "GET":
	//	q := r.URL.ExecuteSingle()
	//	executeRequest.ID = q.Get("id")
	//	executeRequest.GetProjectStore = q.Get("p")
	//	env := q.Get("env")
	//	db := q.Get("db")
	//	executeRequest.Commands = append(executeRequest.Commands,
	//		execute.RequestCommand{
	//			Env:  env,
	//			DB:   db,
	//			Text: q.Get("q1"),
	//		},
	//	)
	case "POST":
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&executeRequest); err != nil {
			err = fmt.Errorf("%w: failed to decode request body", validation.NewBadRequestError(err))
			handleError(err, w, r)
			return
		}
	default:
		handleError(validation.NewBadRequestError(errors.New("only POST requests are supported for this endpoint")), w, r)
		return
	}

	ctx := r.Context()
	storeID := r.URL.Query().Get(urlParamStoreID)
	response, err := api.ExecuteCommands(ctx, storeID, executeRequest)
	returnJSON(w, r, http.StatusOK, err, response)
}

// executeSelectHandler executes a select through the policy-enforced
// secureread.Executor (REQ:server-acl-all-reads) — see api.ExecuteSelect.
func executeSelectHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var limit int64
	var err error
	if v := query.Get("limit"); v == "" {
		limit = -1
	} else if limit, err = strconv.ParseInt(v, 10, 0); err != nil {
		err = validation.NewErrBadRequestFieldValue("limit", "should be an integer number")
		handleError(err, w, r)
		return
	}
	cols := query.Get("cols")
	request := api.SelectRequest{
		Project:     query.Get("proj"),
		Environment: query.Get("env"),
		Database:    query.Get("db"),
		From:        query.Get("from"),
		SQL:         query.Get("sql"),
		Where:       query.Get("where"),
		Limit:       int(limit),
	}
	if request.Project == "" {
		request.Project = storage.SingleProjectID
	}
	if cols != "" {
		request.Columns = strings.Split(cols, ",")
	}
	ctx := r.Context()
	storeID := query.Get(urlParamStoreID)
	response, err := api.ExecuteSelect(ctx, storeID, request)
	returnJSON(w, r, http.StatusOK, err, response)
}

// runQueryHandler executes a saved or ad-hoc query through the
// policy-enforced secureread.Executor (REQ:server-acl-all-reads,
// REQ:dtql-query-type) — see api.RunQuery.
func runQueryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		handleError(validation.NewBadRequestError(errors.New("only POST requests are supported for this endpoint")), w, r)
		return
	}
	var request api.RunQueryRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		err = fmt.Errorf("%w: failed to decode request body", validation.NewBadRequestError(err))
		handleError(err, w, r)
		return
	}
	if request.StoreID == "" {
		request.StoreID = r.URL.Query().Get(urlParamStoreID)
	}
	response, err := api.RunQuery(r.Context(), request)
	returnJSON(w, r, http.StatusOK, err, response)
}
