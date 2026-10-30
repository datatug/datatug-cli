package endpoints

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The two origins of the agent (founder decision 2026-10-15): the hosted UI and the local UI
// development host. Everything else is refused, by exact match.
func TestIsSupportedOrigin(t *testing.T) {
	for _, origin := range []string{
		"https://datatug.app",
		"http://datatug.localhost", "https://datatug.localhost",
		"http://datatug.localhost:4200", "https://datatug.localhost:4200", "https://datatug.localhost:1", "http://datatug.localhost:65535",
	} {
		assert.True(t, IsSupportedOrigin(origin), "%q", origin)
	}
	for _, origin := range []string{
		"http://localhost:4200", "https://localhost:4200", "http://127.0.0.1:9", "https://127.0.0.1:8971",
		"https://x.datatug.app", "https://app.datatug.app", "https://test.datatug.app",
		"https://app.incidentius.com", "http://app.incidentius.com",
		"http://datatug.app", "https://datatug.app:443", "https://datatug.app:8443", "https://datatug.app.evil.com",
		"https://evil.datatug.localhost", "https://datatug.localhost.evil.com", "https://xdatatug.localhost",
		"https://datatug.localhost@evil.com", "http://datatug.localhost:0", "http://datatug.localhost:65536",
		"http://datatug.localhost:04200", "http://datatug.localhost:",
		"https://www.example.com", "http://www.example.com",
	} {
		assert.False(t, IsSupportedOrigin(origin), "%q", origin)
	}
}

// An origin is a scheme, a host and maybe a port: a text that holds more is not on the list,
// whatever it starts or ends with.
func TestIsSupportedOrigin_OnlyTheShapeOfAnOrigin(t *testing.T) {
	for _, origin := range []string{
		"https://evil.example.com/.datatug.app",
		"https://evil.example.com/x.datatug.app",
		"http://datatug.localhost:80@evil.example.com",
		"https://user@datatug.app",
		"http://datatug.localhost:8100/",
		"http://datatug.localhost:8100/path",
		"https://datatug.app/",
		"https://datatug.app?x=1",
		"https://datatug.app#fragment",
		"https://datatug.app?",
		"HTTP://datatug.localhost:8100",
		"ftp://datatug.localhost:8100",
		"datatug.localhost:8100",
		"null",
		"",
		"https://",
		"https://%zz.datatug.app",
	} {
		assert.False(t, IsSupportedOrigin(origin), "%q", origin)
	}
}
