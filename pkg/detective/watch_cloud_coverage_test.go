package detective

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestCloudWatchAcceptedQueriesOwnDefinitionsAndRevisions(t *testing.T) {
	body := json.RawMessage(`{"id":"query","text":"SELECT 1"}`)
	pins := map[string]sessioncontext.DetectiveAcceptedQueryPin{"query": {QueryID: "query", Revision: "saved-revision", DefinitionJSON: body}}
	ctx := WithWatchAcceptedQueries(context.Background(), pins)
	body[0] = '!'
	pins["query"] = sessioncontext.DetectiveAcceptedQueryPin{Revision: "changed"}
	got, ok := NativeAcceptedQuery(ctx, "query")
	if !ok || got.QueryID != "query" || got.Revision != "saved-revision" || string(got.DefinitionJSON) != `{"id":"query","text":"SELECT 1"}` {
		t.Fatal("accepted snapshot changed after caller mutation", got, ok)
	}
	got.DefinitionJSON[0] = '!'
	again, _ := NativeAcceptedQuery(ctx, "query")
	if again.DefinitionJSON[0] != '{' {
		t.Fatal("a query executor mutated the accepted snapshot")
	}
	if revision, ok := AdmittedQueryRevision(ctx, "query"); !ok || revision != "saved-revision" {
		t.Fatal(revision, ok)
	}
	if _, ok := NativeAcceptedQuery(WithWatchAcceptedQueries(context.Background(), nil), "query"); ok {
		t.Fatal("empty admission inherited another request's queries")
	}
}
