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
