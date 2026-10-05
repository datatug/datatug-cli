package endpoints

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// A saved SQL or DTQL query is its "<id>.query.json" and a document beside it (the text of the
// query). When the document is not there, exec/run_query answers one sentence built from the ID
// of the query, a 400; the text of the read, which quotes the path of the file in the served
// project, is in the log of the server. When a folder is in the place of the document, the
// query is not one of the project's queries and the answer is the 404 for a query that is not
// there. Neither answer holds a path of the server.
func TestExecRunQuery_ASavedQueryWhoseDocumentCannotBeReadAnswersOneBuiltSentence(t *testing.T) {
	for _, c := range []struct {
		name, queryID, document string
	}{
		{"DTQL", "customers/customer-invoices", "customer-invoices.query.dtql"},
		{"SQL", "customers/customer-count", "customer-count.query.sql"},
	} {
		for _, breakage := range []string{"missing", "a folder"} {
			t.Run(c.name+" "+breakage, func(t *testing.T) {
				projectDir, projectID := writeRunQueryTestProject(t)
				queriesDir := filepath.Join(projectDir, "queries", "customers")
				mustWriteFile(t, filepath.Join(queriesDir, "customer-count.query.json"), `{"id": "customer-count", "title": "Customer count", "type": "SQL"}`)
				document := filepath.Join(queriesDir, c.document)
				if err := os.RemoveAll(document); err != nil {
					t.Fatal(err)
				}
				if breakage == "a folder" {
					mustMkdirAll(t, filepath.Join(document, "in-it"))
				}
				scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
				logged := captureAgentLog(t)

				status, env := postRunQuery(t, apicontract.ExecutionRequest{
					Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
					QueryID: c.queryID, Mode: apicontract.ProvenanceModeLive,
					Parameters:     map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(apicontract.NewIntegerValue("5"))},
					BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "CustomerId", Origin: apicontract.BindingOriginSelection, FactID: "fact-customer"}},
				})

				wantStatus, wantCode, want := http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, `query "`+c.queryID+`" has no readable document`
				if breakage == "a folder" {
					wantStatus, wantCode, want = http.StatusNotFound, apicontract.ErrCodeNotFound, `query "`+c.queryID+`" not found`
				}
				if status != wantStatus || env.Error.Code != string(wantCode) || env.Error.Message != want {
					t.Fatalf("%d %s %q, want a %d %s whose message is exactly %q", status, env.Error.Code, env.Error.Message, wantStatus, wantCode, want)
				}
				for _, leak := range []string{projectDir, filepath.Base(projectDir), "open ", "read ", "no such file", "is a directory"} {
					if strings.Contains(env.Error.Message, leak) {
						t.Errorf("the answer holds %q: %q", leak, env.Error.Message)
					}
				}
				if breakage == "missing" && !strings.Contains(logged.String(), document) {
					t.Errorf("what the read said is not in the log: %q", logged.String())
				}
			})
		}
	}
}
