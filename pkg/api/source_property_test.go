package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
)

// A source a client sent where an ID belongs is named in the error only when it
// is a plain name (dbcopy.SourceIDDisplay): never as a whole source string.
func TestProperty_ResolveSourceNeverEchoesASourceString(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range sourcecases.All() {
		_, err := ResolveSource(context.Background(), mockProjectStore{}, projectDir, "", c.Source)
		if err == nil {
			t.Fatalf("%s: a source that no registry holds must not resolve", c.Name)
		}
		if leaked := sourcecases.Leaks(c, err.Error()); len(leaked) > 0 {
			t.Errorf("%s\n  leaked %q in %q", c.Name, leaked, err.Error())
		}
		if !strings.Contains(err.Error(), "unknown source") {
			t.Errorf("%s: error %q should still say the source is unknown", c.Name, err.Error())
		}
	}
	// A plain name is still named, so the message stays useful.
	_, err := ResolveSource(context.Background(), mockProjectStore{}, projectDir, "", "chinook")
	if err == nil || !strings.Contains(err.Error(), `unknown source "chinook"`) {
		t.Fatalf("a plain source ID should be named: %v", err)
	}
}
