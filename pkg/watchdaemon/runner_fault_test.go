package watchdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
)

func TestWatchRunnerCoordinatorFailuresAndRegistrationRefresh(t *testing.T) {
	executor := ExecutorFunc(func(context.Context, watch.Claim) (watch.Completion, error) {
		return watch.Completion{ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "v1"}, nil
	})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (Runner{Executor: executor}).Run(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, mode := range []string{"registration-down", "registration-recover", "registration-wait-cancel", "claim-forbidden", "claim-down", "refresh", "stale-result"} {
		t.Run(mode, func(t *testing.T) {
			var register, claims atomic.Int32
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/watch/runner/register":
					n := register.Add(1)
					if mode == "registration-wait-cancel" {
						cancel()
						w.WriteHeader(503)
						return
					}
					if mode == "registration-down" || mode == "registration-recover" && n == 1 {
						w.WriteHeader(503)
						return
					}
					_, _ = w.Write([]byte(`{"ok":true}`))
				case "/v1/watch/runner/claim":
					n := claims.Add(1)
					if mode == "claim-forbidden" {
						w.WriteHeader(403)
						return
					}
					if mode == "claim-down" || mode == "registration-recover" {
						w.WriteHeader(503)
						return
					}
					if mode == "stale-result" && n == 1 {
						_ = json.NewEncoder(w).Encode(map[string]any{"claim": watch.Claim{ClaimID: "run", RunID: "run", Fence: 1}})
					} else {
						_, _ = w.Write([]byte(`{"claim":null}`))
					}
				case "/v1/watch/runner/complete":
					w.WriteHeader(409)
				default:
					_, _ = w.Write([]byte(`{"ok":true}`))
				}
			}))
			defer provider.Close()
			r := Runner{Client: Client{Endpoint: provider.URL, Token: strings.Repeat("token", 9)}, Executor: executor, PollInterval: 5 * time.Millisecond, RegistrationInterval: 2 * time.Millisecond}
			err := r.Run(ctx)
			if mode == "claim-forbidden" {
				if !errors.Is(err, watch.ErrForbidden) {
					t.Fatal(err)
				}
			} else if mode == "registration-wait-cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if mode == "refresh" && register.Load() < 2 {
				t.Fatal("registration was not refreshed")
			}
			if mode == "registration-recover" && register.Load() < 2 {
				t.Fatal("registration failure was not retried")
			}
		})
	}
}

func TestWatchCancellationAtCompletionBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := Runner{Executor: ExecutorFunc(func(context.Context, watch.Claim) (watch.Completion, error) {
		return watch.Completion{ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "v1"}, nil
	}), beforeSubmit: cancel}
	if err := runner.executeClaim(ctx, watch.Claim{ClaimID: "run", RunID: "run", Fence: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled result submitted", err)
	}
	// Missing contexts are intentional negative input, never ordinary calls.
	for _, missingContext := range []context.Context{nil} {
		if err := (Client{Endpoint: "https://coordinator.example", Token: strings.Repeat("token", 9)}).Register(missingContext); !errors.Is(err, watch.ErrInvalid) {
			t.Fatal("nil context submitted", err)
		}
	}
}

func TestWatchRunnerCompletionRetriesPreserveIdentity(t *testing.T) {
	for _, mode := range []string{"accepted", "unavailable", "cancelled", "heartbeat-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var submissions atomic.Int32
			var prior watch.Completion
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/heartbeat") {
					cancel()
					w.WriteHeader(409)
					return
				}
				var c watch.Completion
				_ = json.NewDecoder(r.Body).Decode(&c)
				n := submissions.Add(1)
				if n == 1 {
					prior = c
				} else if c != prior {
					t.Error("completion retry changed identity or payload")
				}
				if mode == "accepted" {
					_, _ = w.Write([]byte(`{"ok":true}`))
					return
				}
				if mode == "cancelled" {
					go func() { time.Sleep(5 * time.Millisecond); cancel() }()
				}
				w.WriteHeader(503)
			}))
			defer provider.Close()
			exec := ExecutorFunc(func(ctx context.Context, claim watch.Claim) (watch.Completion, error) {
				if mode == "heartbeat-cancel" {
					<-ctx.Done()
					return watch.Completion{}, ctx.Err()
				}
				return watch.Completion{ExecutionStatus: "completed", Outcome: "pass", SourceFingerprint: "v1"}, nil
			})
			r := Runner{Client: Client{Endpoint: provider.URL, Token: strings.Repeat("token", 9)}, Executor: exec}
			if mode == "heartbeat-cancel" {
				r.RenewInterval = time.Millisecond
			}
			err := r.executeClaim(ctx, watch.Claim{ClaimID: "run", RunID: "run", Fence: 7})
			if mode == "accepted" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unaccepted completion succeeded")
			}
			if mode == "unavailable" && submissions.Load() != 3 {
				t.Fatal("completion retry budget", submissions.Load())
			}
		})
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer provider.Close()
	ctx, cancel := context.WithCancel(context.Background())
	r := Runner{Client: Client{Endpoint: provider.URL, Token: strings.Repeat("token", 9)}, Executor: ExecutorFunc(func(ctx context.Context, c watch.Claim) (watch.Completion, error) {
		cancel()
		<-ctx.Done()
		time.Sleep(time.Millisecond)
		return watch.Completion{}, ctx.Err()
	})}
	if r.executeClaim(ctx, watch.Claim{ClaimID: "run", RunID: "run", Fence: 1}) == nil {
		t.Fatal("cancelled source submitted result")
	}
}
