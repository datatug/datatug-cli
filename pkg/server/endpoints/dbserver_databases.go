package endpoints

import (
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
	// The path only: the query string can hold a source string a client typed.
	log.Println(r.Method, r.URL.Path)
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
	databases, err := getServerDatabasesFunc(request)
	returnJSON(w, r, http.StatusOK, err, databases)
}

var getServerDatabasesFunc = api.GetServerDatabases

func newDbServerFromQueryParams(query url.Values) (dbServer datatug.ServerRef, err error) {
	dbServer.Driver = query.Get("driver")
	dbServer.Host = query.Get("host")
	if port := strings.TrimSpace(query.Get("port")); port != "" {
		if dbServer.Port, err = strconv.Atoi(port); err != nil {
			// The text of the client is not in the answer (nor is the parse error, which quotes it).
			err = validation.NewErrBadRequestFieldValue("port", "must be a number")
			return
		}
	}
	return
}
