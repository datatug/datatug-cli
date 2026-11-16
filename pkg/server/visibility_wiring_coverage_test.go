package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestServeHTTPRefusesInvalidCreationRootBeforeListening(t *testing.T) {
	previous := listenAndServeFn
	t.Cleanup(func() { listenAndServeFn = previous })
	listened := false
	listenAndServeFn = func(*http.Server) error { listened = true; return nil }
	root := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(root, []byte("operator-owned-file"), 0600); err != nil {
		t.Fatal(err)
	}
	server := NewHttpServer()
	err := server.ServeHTTP(nil, "", 0, unrestrictedTestSession(t), api.Capabilities{ProjectsRoot: root})
	if err == nil || !strings.Contains(err.Error(), "configure local project registry") || listened {
		t.Fatalf("invalid projects root reached listener: listened=%v error=%v", listened, err)
	}
}

func TestServeHTTPDynamicStoreFactoryPropagatesConstructionFailure(t *testing.T) {
	previousListen, previousFactory, previousStore := listenAndServeFn, filestoreNewStore, storage.NewDatatugStore
	t.Cleanup(func() {
		listenAndServeFn, filestoreNewStore, storage.NewDatatugStore = previousListen, previousFactory, previousStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	want := errors.New("injected native store construction failure")
	filestoreNewStore = func(string, map[string]string) (storage.Store, error) { return nil, want }
	listenAndServeFn = func(*http.Server) error {
		_, err := storage.NewDatatugStore("files")
		if !errors.Is(err, want) || !strings.Contains(err.Error(), "configured local store") {
			t.Fatalf("dynamic constructor lost its failure: %v", err)
		}
		return err
	}
	server := NewHttpServer()
	err := server.ServeHTTP(nil, "", 0, unrestrictedTestSession(t), api.Capabilities{ProjectsRoot: t.TempDir(), EvidencePrivateDir: t.TempDir()})
	if !errors.Is(err, want) {
		t.Fatalf("server hid failed store initialization: %v", err)
	}
}
