package demofixtures

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/datatug/datatug-core/pkg/meaninglabels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func restore[T any](t *testing.T, target *T) {
	original := *target
	t.Cleanup(func() { *target = original })
}

func TestFiles(t *testing.T) {
	files, err := Files()
	require.NoError(t, err)
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	chinook := byPath[ChinookPath]
	assert.True(t, chinook.Fixture)
	assert.Equal(t, ChinookSHA256, chinook.SHA256())
	assert.Contains(t, string(byPath[PolicyPath].Content), "name: agent")
	assert.Contains(t, byPath, NoticePath)
	assert.Contains(t, byPath, SupportPath+"/.ingitdb/root-collections.yaml")
	notes := 0
	for path := range byPath {
		if strings.HasPrefix(path, SupportPath+"/support-notes/$records/") {
			notes++
		}
	}
	assert.Equal(t, 8, notes)
	doc, err := meaninglabels.Decode(byPath[LabelsPath].Content)
	require.NoError(t, err)
	email := meaninglabels.Effective(doc, ChinookSource, "Customer", []meaninglabels.Column{{Name: "Email"}, {Name: "FirstName"}})
	assert.Equal(t, meaninglabels.ClassPII, email[0].Class)
	assert.False(t, email[1].Protected())
	again, err := Files()
	require.NoError(t, err)
	assert.Equal(t, files, again, "the same bytes on every run")
}

func TestFilesFailures(t *testing.T) {
	t.Run("walk", func(t *testing.T) {
		restore(t, &embeddedFS)
		embeddedFS = fstest.MapFS{}
		_, err := Files()
		assert.ErrorContains(t, err, "demo fixtures")
	})
	t.Run("read", func(t *testing.T) {
		restore(t, &embeddedFS)
		embeddedFS = unreadableFS{fstest.MapFS{"data/x": {Data: []byte("x")}}}
		_, err := Files()
		assert.ErrorContains(t, err, "unreadable")
	})
	t.Run("tampered chinook", func(t *testing.T) {
		restore(t, &embeddedFS)
		embeddedFS = fstest.MapFS{ChinookPath: {Data: []byte("not chinook")}}
		_, err := Files()
		assert.ErrorContains(t, err, "pinned sha256")
	})
	t.Run("labels", func(t *testing.T) {
		restore(t, &applyLabel)
		applyLabel = func(meaninglabels.Document, meaninglabels.Change, meaninglabels.Actor, time.Time) (meaninglabels.Document, meaninglabels.Event, error) {
			return meaninglabels.Document{}, meaninglabels.Event{}, errors.New("bad label")
		}
		_, err := Files()
		assert.ErrorContains(t, err, "bad label")
	})
	t.Run("encode", func(t *testing.T) {
		restore(t, &encodeLabels)
		encodeLabels = func(meaninglabels.Document) ([]byte, error) { return nil, errors.New("bad encode") }
		_, err := Files()
		assert.ErrorContains(t, err, "bad encode")
	})
}

type unreadableFS struct{ fstest.MapFS }

func (unreadableFS) ReadFile(string) ([]byte, error) { return nil, errors.New("unreadable") }

var _ fs.ReadFileFS = unreadableFS{}
