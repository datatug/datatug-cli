package dtproject

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/go-github/v92/github"
	"golang.org/x/oauth2"
)

// fakeGitHub is a local stand-in for api.github.com. A route is "METHOD /path"
// and answers with a JSON body; a route listed in failing answers with the given
// status instead; anything else is a 404.
type fakeGitHub struct {
	t       *testing.T
	mu      sync.Mutex
	routes  map[string]string
	failing map[string]int
	custom  map[string]http.HandlerFunc
	calls   []string
}

// newFakeGitHub starts the server and makes the screens use a client that talks
// to it, signed in with a token.
func newFakeGitHub(t *testing.T, routes map[string]string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, routes: routes, failing: map[string]int{}, custom: map[string]http.HandlerFunc{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	stub(t, &getToken, func() (*oauth2.Token, error) { return &oauth2.Token{AccessToken: "t"}, nil })
	stub(t, &newGitHubClient, func(context.Context, *oauth2.Token) (*github.Client, error) { return f.client(srv.URL), nil })
	return f
}

func (f *fakeGitHub) client(base string) *github.Client {
	u := base + "/"
	c, err := github.NewClient(github.WithURLs(&u, nil))
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.calls = append(f.calls, key)
	status, fails := f.failing[key]
	body, ok := f.routes[key]
	custom := f.custom[key]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case custom != nil:
		custom(w, r)
	case fails:
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"failed"}`))
	case !ok:
		f.t.Logf("fake GitHub: no route for %s", key)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	default:
		_, _ = w.Write([]byte(body))
	}
}

// fail makes a route answer with status.
func (f *fakeGitHub) fail(route string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing[route] = status
}

// called reports whether a route was requested.
func (f *fakeGitHub) called(route string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == route {
			return true
		}
	}
	return false
}

// handle makes a route answer with fn, for answers that change between calls.
func (f *fakeGitHub) handle(route string, fn http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.custom[route] = fn
}
