package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// No test of this package may fetch the live public demo sites: they rate limit (429) and
// go down, and a test that quietly depends on one leaves branches uncovered on the runner
// while passing on a developer machine. For the whole test binary the default transport
// refuses any *.demodb.dev request, loudly; a test that needs the sample stands in with a
// transport of its own (sessionFixtureTransport, copyHTTPTransport).
type refuseDemodbTransport struct{ next http.RoundTripper }

func (transport refuseDemodbTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if host := r.URL.Hostname(); host == "demodb.dev" || strings.HasSuffix(host, ".demodb.dev") {
		panic("a test requested " + r.URL.Redacted() + " for real: it must stand in with a fake transport and never touch the live site")
	}
	return transport.next.RoundTrip(r)
}

func init() {
	http.DefaultTransport = refuseDemodbTransport{next: http.DefaultTransport}
}

func TestDefaultTransportOfThisTestBinaryRefusesDemodb(t *testing.T) {
	for _, target := range []string{"https://chinook.demodb.dev/data/chinook.sqlite", "https://demodb.dev/ovdb"} {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		assert.NoError(t, err)
		assert.Panics(t, func() { _, _ = http.DefaultTransport.RoundTrip(req) })
	}
	ok, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/", nil)
	assert.NoError(t, err)
	assert.NotPanics(t, func() { _, _ = http.DefaultTransport.RoundTrip(ok) })
}
