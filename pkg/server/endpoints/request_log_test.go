package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// A request line in the agent log names the method and the path. The query string
// is where a client can put a source string (an environment, a database, a source),
// and percent-encoding hides its punctuation, not its password, so it is never logged.
func TestDbServerHandlers_LogTheMethodAndThePathNeverTheQueryString(t *testing.T) {
	const secret = "s3cr3t-Zk39xq"
	origDatabases, origSummary, origDelete := getServerDatabasesFunc, getDbServerSummaryFunc, deleteDbServerFunc
	t.Cleanup(func() {
		getServerDatabasesFunc, getDbServerSummaryFunc, deleteDbServerFunc = origDatabases, origSummary, origDelete
	})
	getServerDatabasesFunc = func(context.Context, dto.GetServerDatabasesRequest) ([]*datatug.DbCatalog, error) { return nil, nil }
	getDbServerSummaryFunc = func(context.Context, dto.ProjectRef, datatug.ServerRef) (*datatug.ProjDbServer, error) {
		return &datatug.ProjDbServer{}, nil
	}
	deleteDbServerFunc = func(context.Context, dto.ProjectRef, datatug.ServerRef) error { return nil }

	for name, route := range map[string]struct {
		path    string
		handler http.HandlerFunc
	}{
		"dbserver-databases": {"/datatug/dbserver-databases", getServerDatabases},
		"dbserver-summary":   {"/datatug/dbserver-summary", getDbServerSummary},
		"dbserver-delete":    {"/datatug/dbserver-delete", deleteDbServer},
	} {
		logged := captureAgentLog(t)
		target := route.path + "?proj=demo&env=postgres%3A%2F%2Falice%3A" + secret + "%40db.example.com%2Fshop&driver=sqlite3&host=h&port=1"
		method := http.MethodGet
		if name == "dbserver-delete" {
			method = http.MethodDelete
		}
		w := httptest.NewRecorder()
		route.handler(w, httptest.NewRequest(method, target, nil))
		line := logged.String()
		if !strings.Contains(line, method+" "+route.path) {
			t.Errorf("%s: the log line %q does not name the method and the path", name, line)
		}
		for _, leaked := range []string{secret, "alice", "db.example.com", "?"} {
			if strings.Contains(line, leaked) {
				t.Errorf("%s: the log line %q holds %q from the query string", name, line, leaked)
			}
		}
	}
}
