package api

import (
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A control character is not a URL's: dbcopy.Parse, which every reader of a project uses,
// refuses a URL that holds one, so a SQLite file with a tab or a newline in its path is
// written into its source URL percent-encoded, as "%", "#" and "?" are.
func TestLocalSQLiteSourceURL_EscapesAControlCharacter(t *testing.T) {
	for path, want := range map[string]string{
		"/data/a\tb.db":     "sqlite:///data/a%09b.db",
		"/data/a\nb.db":     "sqlite:///data/a%0Ab.db",
		"/data/a\r\nb.db":   "sqlite:///data/a%0D%0Ab.db",
		"/data/a\x7fb.db":   "sqlite:///data/a%7Fb.db",
		"/data/a\x00b.db":   "sqlite:///data/a%00b.db",
		"data/a\tb.db":      "sqlite://./data/a%09b.db",
		"/data/a\t#?%b.db":  "sqlite:///data/a%09%23%3F%25b.db",
		"/data/plain.db":    "sqlite:///data/plain.db",
		"/data/données.db":  "sqlite:///data/données.db",
		"/data/with space/": "sqlite:///data/with space/",
	} {
		assert.Equal(t, want, LocalSQLiteSourceURL(path), "%q", path)
	}
}

// The URL reads back as the path it was made from.
func TestLocalSQLiteSourceURL_ReadsBackAsThePath(t *testing.T) {
	for _, path := range []string{
		"/data/a\tb.db", "/data/a\nb.db", "/data/a\r\nb.db", "/data/a\x01b.db", "/data/a\x7fb.db",
		"/data/a\t#?%b.db", "data/a\tb.db", "./a\nb.db", "/data/with space.db", "/data/a#b.db",
	} {
		ref, err := dbcopy.Parse(LocalSQLiteSourceURL(path))
		require.NoError(t, err, "%q", path)
		// A relative path is written with a "./" in front of it, which names the same file.
		assert.Equal(t, filepath.Clean(path), filepath.Clean(ref.Path), "%q", path)
	}
}
