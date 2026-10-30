package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dto"
)

// A project ID or a saved-query ID a client sent can be a whole source string. Every
// route that reports a project or a query that is not there names the ID only when it is
// a plain name (a query ID: when each of its folders and its name is one). For every
// generated source string no secret of four or more characters is in the error text, in
// the HTTP body or on stderr, and each route's message is the one the property is meant
// to read. TestProperty_RunQueryNeverEchoesAProjectOrQueryIDThatIsASourceString holds
// exec/run_query; this holds the compare routes (facts and key), the capture route and
// the two queries listings.
func TestProperty_RoutesNeverEchoAProjectOrQueryIDThatIsASourceString(t *testing.T) {
	ctx := context.Background()
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	logged := captureAgentLog(t)
	storeID, err := api.ResolveStoreID("", scope.Project)
	if err != nil {
		t.Fatal(err)
	}
	side := func(project string) apicontract.CompareSideSpec {
		return apicontract.CompareSideSpec{Kind: apicontract.CompareSideScope, Project: project, Environment: scope.Environment, StoreID: storeID}
	}
	capture := func(project string) error {
		req := validCaptureRequest(scope)
		req.Project = project
		_, _, err := computeCaptureQuery(ctx, req)
		return err
	}
	listing := func(project string) dto.ProjectRef { return dto.ProjectRef{StoreID: "s", ProjectID: project} }

	type call struct {
		name string
		run  func(source string) error
		// phrase is text of the message that the property must reach.
		phrase string
	}
	calls := []call{
		{"compare facts, project", func(s string) error {
			_, err := proveNativeFactsBinding(ctx, "q", side(s), "Customer", "ID")
			return err
		}, "unknown project"},
		{"compare facts, query", func(s string) error {
			_, err := proveNativeFactsBinding(ctx, s, side(scope.Project), "Customer", "ID")
			return err
		}, "not found"},
		{"compare key, project", func(s string) error { _, err := mappedCompareKey(ctx, "q", side(s)); return err }, "unknown project"},
		{"compare key, query", func(s string) error { _, err := mappedCompareKey(ctx, s, side(scope.Project)); return err }, "not found"},
		{"queries capture", capture, "unknown project"},
		{"all queries", func(s string) error { _, err := getAllQueries(ctx, listing(s)); return err }, "unknown project"},
		{"personal queries", func(s string) error { _, err := getPersonalQueries(ctx, listing(s)); return err }, "unknown project"},
	}

	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		var texts []string
		for _, call := range calls {
			err := call.run(c.Source)
			if err == nil || !strings.Contains(err.Error(), call.phrase) {
				t.Fatalf("%s: %s returned %v, want an error that says %q: the property does not reach the message it is meant to read", c.Name, call.name, err, call.phrase)
			}
			texts = append(texts, err.Error()) // before the sink that redacts it
			w := httptest.NewRecorder()
			stderr := captureStderr(t, func() { handleError(err, w, httptest.NewRequest(http.MethodGet, "/datatug/compare", nil)) })
			texts = append(texts, w.Body.String(), stderr)
		}
		texts = append(texts, logged.String())
		logged.Reset()
		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources leaked through a project or query ID", failed, len(cases))
	}

	// A plain name is still named, so the message stays useful.
	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{"compare facts, project", func() error {
			_, err := proveNativeFactsBinding(ctx, "q", side("no-such-project"), "Customer", "ID")
			return err
		}, `unknown project "no-such-project"`},
		{"compare facts, query", func() error {
			_, err := proveNativeFactsBinding(ctx, "folder/no-such-query", side(scope.Project), "Customer", "ID")
			return err
		}, `query "folder/no-such-query" not found`},
		{"compare key, project", func() error { _, err := mappedCompareKey(ctx, "q", side("no-such-project")); return err }, `unknown project "no-such-project"`},
		{"compare key, query", func() error {
			_, err := mappedCompareKey(ctx, "no-such-query", side(scope.Project))
			return err
		}, `query "no-such-query" not found`},
		{"queries capture", func() error { return capture("no-such-project") }, `unknown project "no-such-project"`},
		{"all queries", func() error { _, err := getAllQueries(ctx, listing("no-such-project")); return err }, `unknown project "no-such-project"`},
		{"personal queries", func() error { _, err := getPersonalQueries(ctx, listing("no-such-project")); return err }, `unknown project "no-such-project"`},
	} {
		if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: a plain ID should be named: got %v, want %q", tc.name, err, tc.want)
		}
	}
}
