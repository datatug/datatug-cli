package demogit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken  = "gho_test_credential"
	userJSON   = `{"login":"alice","id":42}`
	forkPath   = "/repos/alice/demo"
	createPath = "/repos/team/demo/forks"
	refPath    = forkPath + "/git/ref/heads/main"
	forkJSON   = `{"full_name":"alice/demo","fork":true,"parent":{"full_name":"TeAm/DeMo"}}`
	branchJSON = `{"default_branch":"main"}`
	refJSON    = `{"ref":"refs/heads/main","object":{"sha":"0123456789012345678901234567890123456789"}}`
)

var (
	canonical = Repo{"team", "demo"}
	aliceFork = Repo{"alice", "demo"}
	testTime  = time.Date(2026, 11, 26, 4, 5, 6, 0, time.FixedZone("test", 2*60*60))
)

type apiReply struct {
	method  string
	path    string
	query   string
	status  int
	body    string
	headers map[string]string
}

func reply(method, path string, status int, body string) apiReply {
	return apiReply{method: method, path: path, status: status, body: body}
}

func userReply() apiReply { return reply("GET", "/user", 200, userJSON) }

func ownedReposReply(page, status int, body string) apiReply {
	r := reply("GET", "/user/repos", status, body)
	r.query = fmt.Sprintf("page=%d&per_page=100&type=owner", page)
	return r
}

func testAPI(t *testing.T, cfg Config, replies ...apiReply) *Client {
	t.Helper()
	next := 0
	var mu sync.Mutex
	srv := testHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if next == len(replies) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		want := replies[next]
		next++
		if r.Method != want.method || r.URL.Path != want.path {
			t.Errorf("request %s %s, want %s %s", r.Method, r.URL.Path, want.method, want.path)
		}
		if want.query != "" && r.URL.RawQuery != want.query {
			t.Errorf("query = %q, want %q", r.URL.RawQuery, want.query)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("unexpected authorization header %q", got)
		}
		if r.Method == "POST" {
			var body map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, map[string]bool{"default_branch_only": true}) {
				t.Errorf("fork request body = %v, error = %v", body, err)
			}
		}
		for key, value := range want.headers {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(want.status)
		fmt.Fprint(w, want.body)
	}))
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if next != len(replies) {
			t.Errorf("received %d requests, wanted %d", next, len(replies))
		}
	})
	cfg.Canonical = canonical
	cfg.APIBaseURL = srv.URL
	cfg.HTTPClient = srv.Client()
	cfg.Now = func() time.Time { return testTime }
	return New(cfg, testToken)
}

func TestDefaults(t *testing.T) {
	c := New(Config{}, "")
	if c.cfg.Branch != "datatug-demo" || c.cfg.APIBaseURL != "https://api.github.com/" || c.cfg.GitBaseURL != "https://github.com/" ||
		c.cfg.ForkWait != time.Minute || c.cfg.Limits != (CloneLimits{200 * 1024 * 1024, 20_000, 1}) ||
		c.cfg.Now().IsZero() || c.cfg.HTTPClient.Timeout <= 0 || c.cfg.HTTPClient.Transport.(*http.Transport).Proxy == nil {
		t.Fatalf("unexpected defaults: %+v", c.cfg)
	}
	if _, err := c.apiClient(); err != nil {
		t.Fatal(err)
	}
}

func TestUser(t *testing.T) {
	ctx := context.Background()
	t.Run("ok", func(t *testing.T) {
		c := testAPI(t, Config{}, userReply())
		if login, err := c.User(ctx); login != "alice" || err != nil {
			t.Fatalf("User() = %q, %v", login, err)
		}
	})
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		wantErr error
		reset   time.Time
	}{
		{"unauthorized", 401, nil, ErrAuth, time.Time{}},
		{"forbidden", 403, nil, ErrAuth, time.Time{}},
		{"primary-rate-limit", 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "2000000000"}, ErrRateLimited, time.Unix(2000000000, 0)},
		{"retry-seconds", 429, map[string]string{"Retry-After": "11"}, ErrRateLimited, testTime.Add(11 * time.Second)},
		{"retry-date", 403, map[string]string{"Retry-After": "Thu, 26 Nov 2026 02:06:00 GMT"}, ErrRateLimited, time.Date(2026, 11, 26, 2, 6, 0, 0, time.UTC)},
		{"no-reset", 429, map[string]string{"X-RateLimit-Remaining": "0"}, ErrRateLimited, testTime.Add(time.Minute)},
		{"echoed-token", 500, nil, nil, time.Time{}},
		{"missing-login", 200, nil, nil, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"message":"echo ` + testToken + `"}`
			r := reply("GET", "/user", tc.status, body)
			r.headers = tc.headers
			c := testAPI(t, Config{}, r)
			login, err := c.User(ctx)
			if login != "" || err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("User() = %q, %v, want %v", login, err, tc.wantErr)
			}
			assertNoToken(t, err.Error())
			var limit *RateLimitError
			if !tc.reset.IsZero() && (!errors.As(err, &limit) || !limit.Reset.Equal(tc.reset) || !strings.Contains(err.Error(), tc.reset.UTC().Format(time.RFC3339))) {
				t.Fatalf("rate limit response = %v, want %v", err, tc.reset)
			}
		})
	}
}

func TestFindFork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   Repo
		err    error
	}{
		{"parent", 200, forkJSON, aliceFork, nil},
		{"source", 200, `{"name":"demo","owner":{"login":"alice"},"fork":true,"parent":{"full_name":"bob/demo"},"source":{"full_name":"team/demo"}}`, aliceFork, nil},
		{"not-a-fork", 200, `{"fork":false,"parent":{"full_name":"team/demo"}}`, Repo{}, ErrNoFork},
		{"other-parent", 200, `{"fork":true,"parent":{"full_name":"other/demo"}}`, Repo{}, ErrNoFork},
		{"not-found", 404, `{}`, Repo{}, ErrNoFork},
		{"forbidden", 403, `{}`, Repo{}, ErrAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replies := []apiReply{userReply(), reply("GET", forkPath, tc.status, tc.body)}
			if errors.Is(tc.err, ErrNoFork) {
				replies = append(replies, ownedReposReply(1, 200, "[]"))
			}
			c := testAPI(t, Config{}, replies...)
			if fork, err := c.FindFork(context.Background()); fork != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("FindFork() = %+v, %v", fork, err)
			}
		})
	}
}

func TestFindForkRenamed(t *testing.T) {
	for _, direct := range []apiReply{
		reply("GET", forkPath, 404, "{}"),
		reply("GET", forkPath, 200, `{"full_name":"alice/demo","fork":false}`),
		reply("GET", forkPath, 200, `{"full_name":"bob/demo","fork":true,"parent":{"full_name":"team/demo"}}`),
	} {
		t.Run(fmt.Sprintf("%d/%s", direct.status, direct.body), func(t *testing.T) {
			page1 := ownedReposReply(1, 200, `[
				{"full_name":"alice/not-a-fork","fork":false},
				{"full_name":"bob/foreign","fork":true},
				{"full_name":"alice/conflicting","owner":{"login":"bob"},"fork":true},
				{"full_name":"alice/deleted","fork":true}
			]`)
			page1.headers = map[string]string{"Link": `</user/repos?page=2>; rel="next"`}
			page2 := ownedReposReply(2, 200, `[
				{"full_name":"alice/moved","fork":true},
				{"full_name":"alice/other","fork":true},
				{"full_name":"ALICE/renamed","fork":true},
				{"full_name":"alice/unused","fork":true}
			]`)
			page2.headers = map[string]string{"Link": `</user/repos?page=3>; rel="next"`}
			c := testAPI(t, Config{}, userReply(), direct, page1,
				reply("GET", "/repos/alice/deleted", 404, "{}"), page2,
				reply("GET", "/repos/alice/moved", 200, `{"full_name":"bob/moved","fork":true,"parent":{"full_name":"team/demo"}}`),
				reply("GET", "/repos/alice/other", 200, `{"full_name":"alice/other","fork":true,"parent":{"full_name":"team/other"}}`),
				reply("GET", "/repos/ALICE/renamed", 200, `{"full_name":"Alice/renamed","fork":true,"source":{"full_name":"TEAM/DEMO"}}`))
			if fork, err := c.FindFork(context.Background()); err != nil || fork != (Repo{"Alice", "renamed"}) {
				t.Fatalf("FindFork() = %+v, %v", fork, err)
			}
		})
	}
}

func TestFindForkDiscoveryLimitsAndErrors(t *testing.T) {
	missing := reply("GET", forkPath, 404, "{}")
	t.Run("at most ten pages", func(t *testing.T) {
		replies := []apiReply{userReply(), missing}
		for page := 1; page <= 10; page++ {
			r := ownedReposReply(page, 200, "[]")
			r.headers = map[string]string{"Link": fmt.Sprintf(`</user/repos?page=%d>; rel="next"`, page+1)}
			replies = append(replies, r)
		}
		c := testAPI(t, Config{}, replies...)
		if _, err := c.FindFork(context.Background()); !errors.Is(err, ErrNoFork) {
			t.Fatalf("FindFork() = %v", err)
		}
	})
	t.Run("list forbidden", func(t *testing.T) {
		c := testAPI(t, Config{}, userReply(), missing, ownedReposReply(1, 403, "{}"))
		if _, err := c.FindFork(context.Background()); !errors.Is(err, ErrAuth) {
			t.Fatalf("FindFork() = %v", err)
		}
	})
	t.Run("candidate forbidden", func(t *testing.T) {
		c := testAPI(t, Config{}, userReply(), missing,
			ownedReposReply(1, 200, `[{"full_name":"alice/renamed","fork":true}]`),
			reply("GET", "/repos/alice/renamed", 403, "{}"))
		if _, err := c.FindFork(context.Background()); !errors.Is(err, ErrAuth) {
			t.Fatalf("FindFork() = %v", err)
		}
	})
}

func TestEnsureFork(t *testing.T) {
	ctx := context.Background()
	missing := reply("GET", forkPath, 404, `{}`)
	empty := ownedReposReply(1, 200, "[]")
	accepted := reply("POST", createPath, 202, `{"full_name":"alice/demo"}`)
	t.Run("existing", func(t *testing.T) {
		c := testAPI(t, Config{}, userReply(), reply("GET", forkPath, 200, forkJSON))
		if fork, created, err := c.EnsureFork(ctx); fork != aliceFork || created || err != nil {
			t.Fatalf("EnsureFork() = %+v, %t, %v", fork, created, err)
		}
	})
	t.Run("new-with-wait", func(t *testing.T) {
		ready := reply("GET", forkPath, 200, branchJSON)
		c := testAPI(t, Config{}, userReply(), missing, empty, accepted, missing, ready, reply("GET", refPath, 404, `{}`), ready, reply("GET", refPath, 200, refJSON))
		var delays []time.Duration
		c.sleep = func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }
		if fork, created, err := c.EnsureFork(ctx); fork != aliceFork || !created || err != nil {
			t.Fatalf("EnsureFork() = %+v, %t, %v", fork, created, err)
		}
		if !reflect.DeepEqual(delays, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}) {
			t.Fatalf("backoff = %v", delays)
		}
	})
	t.Run("renamed", func(t *testing.T) {
		renamePath := "/repos/alice/other-name"
		c := testAPI(t, Config{}, userReply(), missing, empty,
			reply("POST", createPath, 202, `{"owner":{"login":"alice"},"name":"other-name","created_at":"2000-01-01T00:00:00Z"}`),
			reply("GET", renamePath, 200, branchJSON), reply("GET", renamePath+"/git/ref/heads/main", 200, refJSON))
		if fork, created, err := c.EnsureFork(ctx); fork != (Repo{"alice", "other-name"}) || created || err != nil {
			t.Fatalf("EnsureFork() = %+v, %t, %v", fork, created, err)
		}
	})
	t.Run("never-ready", func(t *testing.T) {
		c := testAPI(t, Config{ForkWait: 100 * time.Millisecond}, userReply(), missing, empty, accepted, reply("GET", forkPath, 200, `{}`))
		c.sleep = func(ctx context.Context, _ time.Duration) error { return waitForPoll(ctx, time.Hour) }
		if _, _, err := c.EnsureFork(ctx); !errors.Is(err, ErrForkNotReady) {
			t.Fatalf("EnsureFork() = %v", err)
		}
	})
	t.Run("canceled-during-wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		c := testAPI(t, Config{}, userReply(), missing, empty, accepted, missing)
		c.sleep = func(ctx context.Context, delay time.Duration) error { cancel(); return waitForPoll(ctx, delay) }
		if _, _, err := c.EnsureFork(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("EnsureFork() = %v", err)
		}
	})
	for _, tc := range []struct {
		name    string
		replies []apiReply
		message string
	}{
		{"canonical-missing", []apiReply{reply("POST", createPath, 404, `{}`)}, "demo repository not found"},
		{"create-forbidden", []apiReply{reply("POST", createPath, 403, `{}`)}, ErrAuth.Error()},
		{"create-malformed", []apiReply{reply("POST", createPath, 202, `{}`)}, "no repository name"},
		{"poll-forbidden", []apiReply{accepted, reply("GET", forkPath, 403, `{}`)}, ErrAuth.Error()},
		{"ref-forbidden", []apiReply{accepted, reply("GET", forkPath, 200, branchJSON), reply("GET", refPath, 403, `{}`)}, ErrAuth.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testAPI(t, Config{}, append([]apiReply{userReply(), missing, empty}, tc.replies...)...)
			if _, _, err := c.EnsureFork(ctx); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("EnsureFork() = %v, want %q", err, tc.message)
			}
		})
	}
}

func TestAPIContextAndConfiguration(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		want := errors.New("connection lost")
		c := New(Config{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, want })}}, testToken)
		if _, err := c.User(context.Background()); !errors.Is(err, want) || !strings.Contains(err.Error(), "get GitHub user") {
			t.Fatalf("User() error = %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := New(Config{}, testToken)
		if _, err := c.FindFork(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("FindFork() error = %v", err)
		}
		if _, _, err := c.EnsureFork(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("EnsureFork() error = %v", err)
		}
	})
	t.Run("invalid-url", func(t *testing.T) {
		c := New(Config{APIBaseURL: "%"}, testToken)
		if _, err := c.User(context.Background()); err == nil || !strings.Contains(err.Error(), "configure GitHub API") {
			t.Fatalf("User() error = %v", err)
		}
		if _, err := c.FindFork(context.Background()); err == nil || !strings.Contains(err.Error(), "configure GitHub API") {
			t.Fatalf("FindFork() error = %v", err)
		}
		if _, _, err := c.EnsureFork(context.Background()); err == nil || !strings.Contains(err.Error(), "configure GitHub API") {
			t.Fatalf("EnsureFork() error = %v", err)
		}
	})
	if err := waitForPoll(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func assertNoToken(t *testing.T, value string) {
	t.Helper()
	if strings.Contains(value, testToken) {
		t.Fatalf("value contains the GitHub token: %q", value)
	}
}

// Serve real HTTP messages over in-memory connections, which also works in
// sandboxes where even binding a loopback TCP socket is prohibited.
func testHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener := &pipeListener{pending: make(chan net.Conn), closed: make(chan struct{})}
	srv := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	srv.Start()
	srv.Client().Transport.(*http.Transport).DialContext = listener.dial
	t.Cleanup(srv.Close)
	return srv
}

type pipeListener struct {
	pending chan net.Conn
	closed  chan struct{}
	once    sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.pending:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	caller, server := net.Pipe()
	select {
	case l.pending <- server:
		return caller, nil
	case <-ctx.Done():
		caller.Close()
		server.Close()
		return nil, ctx.Err()
	case <-l.closed:
		caller.Close()
		server.Close()
		return nil, net.ErrClosed
	}
}
