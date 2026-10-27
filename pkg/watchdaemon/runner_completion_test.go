package watchdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

// Cancel after the successful completion response is consumed. The coordinator
// acknowledgement therefore completes before shutdown; no deadline can race
// the successful-job path as it can in a timer-driven protocol test.
type cancelAfterCompletionBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b cancelAfterCompletionBody) Close() error { b.cancel(); return nil }

func TestWatchRunnerAcknowledgesSuccessfulCompletionBeforeShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var paths []string
	var completed watch.Completion
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		body := io.NopCloser(strings.NewReader(`{"ok":true}`))
		if strings.HasSuffix(r.URL.Path, "/claim") {
			body = io.NopCloser(strings.NewReader(`{"claim":{"claimId":"claim-1","runId":"run-1","fence":7}}`))
		}
		if strings.HasSuffix(r.URL.Path, "/complete") {
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			body = cancelAfterCompletionBody{Reader: strings.NewReader(`{"ok":true}`), cancel: cancel}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})
	runner := Runner{
		Client:        Client{Endpoint: "https://coordinator.example", Token: strings.Repeat("token", 9), HTTP: &http.Client{Transport: transport}},
		PollInterval:  time.Hour,
		RenewInterval: time.Hour,
		Executor: ExecutorFunc(func(_ context.Context, claim watch.Claim) (watch.Completion, error) {
			if claim.ClaimID != "claim-1" || claim.RunID != "run-1" || claim.Fence != 7 {
				t.Fatalf("wrong claimed job: %+v", claim)
			}
			return watch.Completion{ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "v1"}, nil
		}),
	}
	if err := runner.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown must preserve cancellation after acknowledgement: %v", err)
	}
	want := []string{"/v1/watch/runner/register", "/v1/watch/runner/claim", "/v1/watch/runner/complete"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("unexpected coordinator requests: %v", paths)
	}
	if completed.RunID != "run-1" || completed.ClaimID != "claim-1" || completed.Fence != 7 || completed.Outcome != "pass" {
		t.Fatalf("acknowledged result lost claim or outcome: %+v", completed)
	}
}
