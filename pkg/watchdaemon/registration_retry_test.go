package watchdaemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

// Cancellation happens after the failed registration response has been handled,
// before Run enters its retry wait. No deadline races the HTTP request or poll.
type cancelAfterRegistrationResponse struct {
	io.Reader
	cancel context.CancelFunc
}

func (body cancelAfterRegistrationResponse) Close() error {
	body.cancel()
	return nil
}

func TestWatchRunnerStopsRegistrationRetryOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var paths []string
	executed := false
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       cancelAfterRegistrationResponse{Reader: strings.NewReader(`{"error":"coordinator unavailable"}`), cancel: cancel},
		}, nil
	})
	runner := Runner{
		Client:       Client{Endpoint: "https://coordinator.example", Token: strings.Repeat("token", 9), HTTP: &http.Client{Transport: transport}},
		PollInterval: time.Hour,
		Executor: ExecutorFunc(func(context.Context, watch.Claim) (watch.Completion, error) {
			executed = true
			return watch.Completion{}, nil
		}),
	}
	finished := make(chan error, 1)
	go func() { finished <- runner.Run(ctx) }()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("registration retry lost cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop when registration retry was cancelled")
	}
	if !reflect.DeepEqual(paths, []string{"/v1/watch/runner/register"}) {
		t.Fatalf("runner made requests after rejected registration: %v", paths)
	}
	if executed {
		t.Fatal("runner executed a check without registering")
	}
}
