package endpoints

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsSupportedOrigin(t *testing.T) {
	assert.True(t, IsSupportedOrigin("http://localhost:8100"))
	assert.True(t, IsSupportedOrigin("https://datatug.app"))
	assert.True(t, IsSupportedOrigin("https://app.incidentius.com"))
	assert.True(t, IsSupportedOrigin("https://test.datatug.app"))
	assert.True(t, IsSupportedOrigin("http://127.0.0.1:8971"))
	assert.True(t, IsSupportedOrigin("https://127.0.0.1:8971"))
	assert.False(t, IsSupportedOrigin("https://www.example.com"))
	assert.False(t, IsSupportedOrigin("http://www.example.com"))
	assert.False(t, IsSupportedOrigin("http://app.incidentius.com"))
	assert.False(t, IsSupportedOrigin("https://app.incidentius.com:443"))
	assert.False(t, IsSupportedOrigin("https://test.app.incidentius.com"))
	assert.False(t, IsSupportedOrigin("https://app.incidentius.com/path"))
}

// An origin is a scheme, a host and maybe a port: a text that holds more is not on the list,
// whatever it starts or ends with.
func TestIsSupportedOrigin_OnlyTheShapeOfAnOrigin(t *testing.T) {
	for _, origin := range []string{
		"https://evil.example.com/.datatug.app",
		"https://evil.example.com/x.datatug.app",
		"http://localhost:80@evil.example.com",
		"https://user@datatug.app",
		"http://localhost:8100/",
		"http://localhost:8100/path",
		"https://datatug.app/",
		"https://datatug.app?x=1",
		"https://datatug.app#fragment",
		"https://datatug.app?",
		"http://127.0.0.1:8971/path",
		"HTTP://localhost:8100",
		"ftp://localhost:8100",
		"localhost:8100",
		"null",
		"",
		"https://",
		"http://localhost:8989.evil.example.com",
		"https://%zz.datatug.app",
	} {
		assert.False(t, IsSupportedOrigin(origin), "%q", origin)
	}
	assert.True(t, IsSupportedOrigin("http://localhost:8100"))
	assert.True(t, IsSupportedOrigin("https://localhost:8100"))
}
