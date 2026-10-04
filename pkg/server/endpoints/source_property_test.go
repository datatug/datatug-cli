package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// TestProperty_ServeNeverEchoesASourceSecret is the DT-0C acceptance property at
// the serve layer. A source string reaches a response in two ways: a source URL
// the server opens (resolveSQLSourceURL builds the error), and a source a client
// sent where an ID belongs (the unknown-source and unauthorized-target messages).
// For every generated source string no secret of four or more characters is in
// the error text, in the HTTP body, in stderr or in the log.
//
// The client-sent source is read through every message that echoes one: the
// unknown-source message (ad-hoc DTQL), the not-an-authorized-target message (a
// saved query, with the eligible targets stubbed so that branch is reached) and
// the not-a-catalog-source message of a query capture (checkCaptureSource).
//
// The text is read before it reaches any sink (the error's own Error() and the
// contract error's Message field), and again in the response, so a redactor in
// the sink cannot be what keeps it out.
func TestProperty_ServeNeverEchoesASourceSecret(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	logged := captureAgentLog(t)

	// A saved query with two eligible targets: a source that is neither is "not an
	// authorized target". A capture store with no catalogs: nothing is a catalog source.
	origEligible, origStore := eligibleTargetsHook, captureProjectStoreFor
	t.Cleanup(func() { eligibleTargetsHook, captureProjectStoreFor = origEligible, origStore })
	eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return []api.ResolvedSource{{ID: "chinook", Label: "Chinook"}, {ID: "orders", Label: "Orders"}}, nil
	}
	captureProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return mockStoreWithCatalogs{err: errors.New("no catalogs")}, nil
	}
	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		var texts []string
		note := func(err error) {
			if err != nil {
				texts = append(texts, err.Error())
			}
		}
		respond := func(err error) {
			w := httptest.NewRecorder()
			stderr := captureStderr(t, func() { handleError(err, w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select", nil)) })
			texts = append(texts, w.Body.String(), stderr)
		}

		// A source URL the server opens.
		_, err := resolveSQLSourceURL(ctx, c.Source, "customers")
		note(err)
		respond(err)

		// A source a client sent as an ID, through the resolver and the contract.
		_, err = apiResolveSource(ctx, nil, projectDir, "", c.Source)
		note(err)
		// Each request reaches a different message: the ad-hoc DTQL one says the
		// source is unknown, the saved-query one that it is not an authorized target.
		for _, request := range []struct {
			name       string
			req        apicontract.ExecutionRequest
			wantPhrase string
		}{
			{"ad-hoc DTQL", apicontract.ExecutionRequest{DTQL: "from: {name: t}", Source: c.Source}, "unknown source"},
			{"saved query", apicontract.ExecutionRequest{QueryID: "q", Source: c.Source}, "is not an authorized target"},
		} {
			_, err = resolveExecutionSource(ctx, nil, projectDir, request.req, &datatug.QueryDef{ID: "q"})
			var contract *contractError
			if !errors.As(err, &contract) {
				t.Fatalf("%s: resolveExecutionSource (%s) returned %v, want a contract error", c.Name, request.name, err)
			}
			if !strings.Contains(contract.Message, request.wantPhrase) {
				t.Fatalf("%s: the %s message %q does not say %q: the property does not reach the message it is meant to read", c.Name, request.name, contract.Message, request.wantPhrase)
			}
			texts = append(texts, contract.Message) // before the sink that redacts it
			w := httptest.NewRecorder()
			writeContractResponse(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil), contract, nil)
			texts = append(texts, w.Body.String())
		}

		// A source a client sent to capture a query.
		err = checkCaptureSource(ctx, "demo", projectDir, "local", c.Source)
		var captureErr *contractError
		if !errors.As(err, &captureErr) || !strings.Contains(captureErr.Message, "is not a catalog source") {
			t.Fatalf("%s: checkCaptureSource returned %v, want the not-a-catalog-source contract error", c.Name, err)
		}
		texts = append(texts, captureErr.Message)
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
		t.Errorf("%d of %d generated sources leaked a secret through a serve response", failed, len(cases))
	}
}
