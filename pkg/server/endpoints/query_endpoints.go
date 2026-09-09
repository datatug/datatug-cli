package endpoints

import (
	"context"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// var getQueries = api.GetQueries
var getQuery = api.GetQuery

//// GetQueries returns list of project queries
//func GetQueries(w http.ResponseWriter, r *http.Request) {
//	q := r.URL.Query()
//	folder := q.Get(urlQueryParamFolder)
//	ref := newProjectRef(r.URL.Query())
//	ctx, err := getContextFromRequest(r)
//	if err != nil {
//		handleError(err, w, r)
//	}
//	v, err := getQueries(ctx, ref, folder)
//	returnJSON(w, r, http.StatusOK, err, v)
//}

// getQueryHandler returns query definition. The web client
// (project-item-service.ts's getProjItem, instantiated for queries with
// itemPath="query") sends the query id as `?query=`, not `?id=` — widen the
// item-id lookup to accept both.
func getQueryHandler(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return getQuery(ctx, ref)
	}, urlParamID, "query")
}

// createQuery handles create query endpoint
var createQuery = func(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectRef
	var request dto.CreateQuery
	saveFunc := func(ctx context.Context) (apicore.ResponseDTO, error) {
		return api.CreateQuery(ctx, request)
	}
	createProjectItem(w, r, &ref, &request, saveFunc)
}

// updateQuery handles update query endpoint
func updateQuery(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	var request dto.UpdateQuery
	saveFunc := func(ctx context.Context) (apicore.ResponseDTO, error) {
		return api.UpdateQuery(ctx, request)
	}
	saveProjectItem(w, r, &ref, &request, saveFunc)
}

// deleteQuery handles delete query endpoint
var deleteQuery = deleteProjItem(api.DeleteQuery)
