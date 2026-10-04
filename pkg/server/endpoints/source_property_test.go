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
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// TestProperty_ServeNeverEchoesASourceSecret is the DT-0C acceptance property at
// the serve layer. A source string reaches a response in two ways: a source URL
// the server opens (resolveSQLSourceURL builds the error), and a source a client
// sent where an ID belongs (the unknown-source and unauthorized-target messages).
// For every generated source string no secret of four or more characters is in
// the error text, in the HTTP body, in stderr or in the log.
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
		_, err = resolveExecutionSource(ctx, nil, projectDir, apicontract.ExecutionRequest{DTQL: "from: {name: t}", Source: c.Source}, nil)
		var contract *contractError
		if errors.As(err, &contract) {
			texts = append(texts, contract.Message) // before the sink that redacts it
			w := httptest.NewRecorder()
			writeContractResponse(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil), contract, nil)
			texts = append(texts, w.Body.String())
		} else {
			t.Fatalf("%s: resolveExecutionSource returned %v, want a contract error", c.Name, err)
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
		t.Errorf("%d of %d generated sources leaked a secret through a serve response", failed, len(cases))
	}
}
