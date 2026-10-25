package watchdaemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

// Client.call closes the response after decoding its acknowledged result. This
// body cancels the real context at that boundary, before Runner starts waiting.
type cancelContextOnBodyClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelContextOnBodyClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func TestRunnerCancellationAfterSuccessfulClaim(t *testing.T) {
	for _, caller := range []string{"claim", "runner-empty", "runner-completed"} {
		t.Run(caller, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var registrations, claims, executions, completions int
			client := Client{
				Endpoint: "http://127.0.0.1",
				Token:    strings.Repeat("runner-token", 3),
				HTTP: &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					if err := req.Context().Err(); err != nil {
						t.Errorf("request made after cancellation: %s: %v", req.URL.Path, err)
						return nil, err
					}
					var body io.ReadCloser
					switch req.URL.Path {
					case "/v1/watch/runner/register":
						registrations++
						body = io.NopCloser(strings.NewReader(`{"ok":true}`))
					case "/v1/watch/runner/claim":
						claims++
						if caller == "runner-completed" {
							body = io.NopCloser(strings.NewReader(`{"claim":{"claimId":"claim-1","runId":"run-1","fence":1}}`))
						} else {
							body = cancelContextOnBodyClose{ReadCloser: io.NopCloser(strings.NewReader(`{"claim":null}`)), cancel: cancel}
						}
					case "/v1/watch/runner/complete":
						completions++
						body = cancelContextOnBodyClose{ReadCloser: io.NopCloser(strings.NewReader(`{"ok":true}`)), cancel: cancel}
					default:
						t.Errorf("unexpected coordinator request: %s", req.URL.Path)
						cancel()
						return nil, errors.New("unexpected coordinator request")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: req}, nil
				})},
			}
			if caller == "claim" {
				claim, err := client.Claim(ctx)
				if err != nil || claim != nil {
					t.Fatalf("successfully decoded empty claim = %v, %v", claim, err)
				}
			} else {
				runner := Runner{
					Client: client, PollInterval: time.Hour, RegistrationInterval: time.Hour, RenewInterval: time.Hour,
					Executor: ExecutorFunc(func(context.Context, watch.Claim) (watch.Completion, error) {
						executions++
						return watch.Completion{ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "v1"}, nil
					}),
				}
				if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
					t.Fatalf("Runner.Run after acknowledged claim = %v; want context cancellation", err)
				}
			}
			wantRegistrations, wantExecutions := 0, 0
			if caller != "claim" {
				wantRegistrations = 1
			}
			if caller == "runner-completed" {
				wantExecutions = 1
			}
			if registrations != wantRegistrations || claims != 1 || executions != wantExecutions || completions != wantExecutions || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("registrations=%d claims=%d executions=%d completions=%d context=%v; want %d/1/%d/%d/canceled", registrations, claims, executions, completions, ctx.Err(), wantRegistrations, wantExecutions, wantExecutions)
			}
		})
	}
}
