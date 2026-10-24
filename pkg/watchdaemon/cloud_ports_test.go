package watchdaemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/watch"
)

func TestWatchCloudCredentialResolverIsExclusive(t *testing.T) {
	f := protocolFixture(t)
	for _, tc := range []struct {
		err    error
		status int
	}{{watch.ErrForbidden, 403}, {errors.New("private backend credential detail"), 503}, {watch.ErrCapacity, 429}} {
		f.server.ResolveCredential = func(*http.Request, string) (Credential, error) { return Credential{}, tc.err }
		status, body := f.request(t, "alice", "GET", "identity", nil)
		if status != tc.status || bytes.Contains(body, []byte("private backend")) || bytes.Contains(body, []byte("alice")) {
			t.Fatal("resolver failed open or leaked", status, string(body))
		}
	}
	f.server.ResolveCredential = func(_ *http.Request, kind string) (Credential, error) {
		return Credential{Kind: "runner", ID: "alice", Namespace: "northwind", Project: "northwind", Environment: "local"}, nil
	}
	if status, _ := f.request(t, "alice", "GET", "identity", nil); status != 403 {
		t.Fatal("wrong resolver kind", status)
	}
	f.server.Auth = nil
	f.server.ResolveCredential = func(_ *http.Request, kind string) (Credential, error) {
		return Credential{Kind: kind, ID: "alice", Namespace: "northwind", Project: "northwind", Environment: "local", Hosted: true, SpaceID: "space", CanManageRunners: true}, nil
	}
	status, body := f.request(t, "alice", "GET", "identity", nil)
	for _, expected := range []string{`"hosted":true`, `"spaceID":"space"`, `"canManageRunners":true`} {
		if status != 200 || !bytes.Contains(body, []byte(expected)) {
			t.Fatal(status, string(body))
		}
	}
}

type cloudTickStore struct {
	CoordinatorStore
	reconcileErr, claimErr, finishErr error
	deliveries                        []*watch.Delivery
	claimed                           int
	failures                          []string
}

func (s *cloudTickStore) Reconcile(context.Context) error { return s.reconcileErr }
func (s *cloudTickStore) ClaimNotifications(context.Context) (*watch.Delivery, error) {
	s.claimed++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if len(s.deliveries) == 0 {
		return nil, nil
	}
	d := s.deliveries[0]
	s.deliveries = s.deliveries[1:]
	return d, nil
}
func (s *cloudTickStore) FinishNotification(_ context.Context, _ string, _ int64, failure string) error {
	s.failures = append(s.failures, failure)
	return s.finishErr
}
func TestWatchCloudTickIsBoundedAndAcknowledgesProviderFailure(t *testing.T) {
	ctx := context.Background()
	if (&Server{}).RunCoordinatorTick(ctx, 1) != watch.ErrInvalid {
		t.Fatal("nil store")
	}
	for _, n := range []int{-1, 0, 51} {
		if (&Server{Store: &cloudTickStore{}}).RunCoordinatorTick(ctx, n) != watch.ErrInvalid {
			t.Fatal("unbounded tick", n)
		}
	}
	store := &cloudTickStore{deliveries: []*watch.Delivery{{Channel: "email"}, {Channel: "email"}}}
	server := &Server{Store: store}
	if e := server.RunCoordinatorTick(ctx, 1); e != nil || store.claimed != 1 || len(store.failures) != 1 || store.failures[0] != "DELIVERY_FAILED" || len(store.deliveries) != 1 {
		t.Fatal("bounded failed-provider acknowledgement", store, e)
	}
	store.deliveries = nil
	if e := server.RunCoordinatorTick(ctx, 50); e != nil {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e := server.RunCoordinatorTick(canceled, 1); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	problem := errors.New("storage failed")
	store.reconcileErr = problem
	if e := server.RunCoordinatorTick(ctx, 1); !errors.Is(e, problem) {
		t.Fatal(e)
	}
	store.reconcileErr = nil
	store.claimErr = problem
	if e := server.RunCoordinatorTick(ctx, 1); !errors.Is(e, problem) {
		t.Fatal(e)
	}
	store.claimErr = nil
	store.finishErr = problem
	store.deliveries = []*watch.Delivery{{Channel: "in_app"}}
	if e := server.RunCoordinatorTick(ctx, 1); !errors.Is(e, problem) || store.failures[len(store.failures)-1] != "" {
		t.Fatal("accepted send acknowledgement", e)
	}
}
func TestWatchCloudNotificationLinksUseEnvironmentAndNoStaticFallback(t *testing.T) {
	f := protocolFixture(t)
	n := watch.Notification{Scope: protocolScope, WatchID: "watch", Link: "/watch?project=northwind&environment=local&watch=watch&check=check"}
	f.server.ResolveCredential = func(*http.Request, string) (Credential, error) { return Credential{}, watch.ErrForbidden }
	text := f.server.notificationText(n)
	if !strings.Contains(text, "/watch/watch?env=local") || !strings.Contains(text, "/checks/check?env=local") {
		t.Fatal(text)
	}
	f.server.ProjectUIURLs = nil
	f.server.PublicUIURL = "https://datatug.app/private-project"
	if strings.Contains(f.server.notificationText(n), "https://") {
		t.Fatal("dynamic authority used static-token project fallback")
	}
	response := httptest.NewRecorder()
	fail(response, watch.ErrCapacity)
	if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "CAPACITY_REACHED") {
		t.Fatal(response.Code, response.Body.String())
	}
}
