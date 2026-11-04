package commands

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type athenaExecutorCancelOutcome struct {
	candidate []byte
	err       error
}

func TestAthenaExecutorLaterPageCallerAndDeadline(t *testing.T) {
	for _, test := range []struct {
		name     string
		deadline bool
		want     error
	}{
		{name: "caller_cancel", want: context.Canceled},
		{name: "caller_deadline", deadline: true, want: context.DeadlineExceeded},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			cfg := athenaSessionLifecycleConfig(t)
			dispatched, disconnected, escape := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var dispatchedOnce, disconnectedOnce, escapeOnce sync.Once
			f := athenaSessionNewFixture(t, func(_ *athenaSessionFixture, w http.ResponseWriter, r *http.Request, event athenaSessionEvent) bool {
				if event.op != "GetQueryResults" {
					return false
				}
				if !athenaSessionResultRequest(t, event.body) {
					w.WriteHeader(400)
					return true
				}
				token, hasToken := event.body["NextToken"].(string)
				if !hasToken {
					if _, present := event.body["NextToken"]; present || len(event.body) != 3 {
						t.Error("first executor result request changed closed shape")
						w.WriteHeader(400)
						return true
					}
					athenaSessionWrite(t, w, 200, map[string]any{
						"ResultSet": map[string]any{
							"ResultSetMetadata": map[string]any{"ColumnInfo": athenaSessionColumns()},
							"Rows": []any{
								athenaSessionRow("id", athenaSessionPointer("amount")),
								athenaSessionRow("a", athenaSessionPointer("native1")),
							},
						},
						"NextToken": "owned-executor-hold",
					})
					return true
				}
				if token != "owned-executor-hold" || len(event.body) != 4 {
					t.Error("later executor result request changed token or closed shape")
					w.WriteHeader(400)
					return true
				}
				dispatchedOnce.Do(func() { close(dispatched) })
				select {
				case <-r.Context().Done():
					disconnectedOnce.Do(func() { close(disconnected) })
				case <-escape:
					t.Error("executor escape won before actual cancellation")
				}
				return true
			})
			t.Cleanup(func() { escapeOnce.Do(func() { close(escape) }) })
			client, closeTransport := athenaSessionClient(t, f, cfg)
			var closes atomic.Int32
			closed := func() { closes.Add(1); closeTransport() }
			var parent context.Context
			var cancel context.CancelFunc
			var causeErr error
			if test.deadline {
				causeErr = errors.New("PRIVATE_EXECUTOR_DEADLINE_CAUSE")
				timeoutCtx, timeoutCancel := context.WithTimeoutCause(context.Background(), 2*time.Second, causeErr)
				parent, cancel = timeoutCtx, timeoutCancel
			} else {
				causeErr = errors.New("PRIVATE_EXECUTOR_CALLER_CAUSE")
				causeCtx, causeCancel := context.WithCancelCause(context.Background())
				parent, cancel = causeCtx, func() { causeCancel(causeErr) }
			}
			request := athenaSessionRequest(t, cfg)
			result, joined := make(chan athenaExecutorCancelOutcome, 1), make(chan struct{})
			t.Cleanup(func() {
				cancel()
				escapeOnce.Do(func() { close(escape) })
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-joined:
				case <-timer.C:
					t.Error("executor goroutine did not join during cleanup")
				}
			})
			go func() {
				defer close(joined)
				candidate, err := executeAthenaRead(parent, client, closed, request)
				result <- athenaExecutorCancelOutcome{candidate: candidate, err: err}
			}()
			athenaSessionLifecycleWait(t, dispatched, "second result page dispatch")
			if !test.deadline {
				cancel()
			}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			var outcome athenaExecutorCancelOutcome
			select {
			case outcome = <-result:
			case <-timer.C:
				t.Fatal("executeAthenaRead did not return")
			}
			select {
			case <-joined:
			case <-timer.C:
				t.Fatal("executeAthenaRead goroutine did not join")
			}
			athenaSessionLifecycleWait(t, disconnected, "actual server request disconnect")
			if outcome.candidate != nil {
				t.Fatal("cancellation returned a private candidate buffer")
			}
			if !errors.Is(outcome.err, test.want) {
				t.Fatalf("executeAthenaRead error = %v, want %v", outcome.err, test.want)
			}
			rendered := outcome.err.Error()
			for _, secret := range []string{
				causeErr.Error(), athenaSessionCredential.AccessKeyID,
				athenaSessionCredential.SecretAccessKey, athenaSessionCredential.SessionToken,
			} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("safe error leaked private %q", secret)
				}
			}
			if got := closes.Load(); got != 1 {
				t.Fatalf("original transport close callback ran %d times, want 1", got)
			}
			if start, status, results, stop := f.count("StartQueryExecution"), f.count("GetQueryExecution"),
				f.count("GetQueryResults"), f.count("StopQueryExecution"); start != 1 || status != 1 || results != 2 || stop != 0 {
				t.Fatalf("unexpected RPC counts: start=%d status=%d results=%d stop=%d", start, status, results, stop)
			}
		})
	}
}
