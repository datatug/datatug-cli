package endpoints

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

// getServerDatabases returns databases hosted at server. The web client
// (db-server.service.ts's getServerDatabases) sends "proj"; paramAlias also
// accepts the contract's "project"/"environment" names.
func getServerDatabases(w http.ResponseWriter, r *http.Request) {
	log.Println(r.Method, r.RequestURI)
	q := r.URL.Query()
	request := dto.GetServerDatabasesRequest{
		Project:     paramAlias(q, "proj", urlParamProjectID),
		Environment: paramAlias(q, "env", "environment"),
	}
	var err error
	if request.ServerRef, err = newDbServerFromQueryParams(q); err != nil {
		handleError(err, w, r)
		return
	}
	databases, err := api.GetServerDatabases(request)
	returnJSON(w, r, http.StatusOK, err, databases)
}

func newDbServerFromQueryParams(query url.Values) (dbServer datatug.ServerRef, err error) {
	dbServer.Driver = query.Get("driver")
	dbServer.Host = query.Get("host")
	if port := strings.TrimSpace(query.Get("port")); port != "" {
		if dbServer.Port, err = strconv.Atoi(port); err != nil {
			err = validation.NewBadRequestError(fmt.Errorf("port parameter is not a number: %w", err))
			return
		}
	}
	return
}
