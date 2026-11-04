package server

import (
	"errors"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"github.com/datatug/datatug-cli/pkg/comparecache"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestServeHTTPConfigurationRaceRefusesBeforeListener(t *testing.T) {
	oldListen, oldCompare, oldStore, oldMonitor := listenAndServeFn, configureCompareCacheFn, storage.NewDatatugStore, apimonitor.Default
	t.Cleanup(func() {
		listenAndServeFn, configureCompareCacheFn, storage.NewDatatugStore, apimonitor.Default = oldListen, oldCompare, oldStore, oldMonitor
		if err := api.ConfigureSecureSessionChecked(secureread.Session{}, nil, api.Capabilities{}); err != nil {
			t.Error(err)
		}
	})
	apimonitor.Default = &apimonitor.Registry{}
	root := t.TempDir()
	project := "late-declaration"
	if err := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"late-declaration","title":"Owned fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	listened := 0
	listenAndServeFn = func(*http.Server) error { listened++; return http.ErrServerClosed }
	injected := 0
	configureCompareCacheFn = func(comparecache.Options) error {
		injected++
		if err := os.Mkdir(filepath.Join(root, ".datatug"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, ".datatug", "security.json"), []byte(`{}`), 0600)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true, As: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewHttpServer()
	err = server.ServeHTTP(map[string]string{project: root}, "127.0.0.1", 8989, session, api.Capabilities{EvidencePrivateDir: t.TempDir()})
	if !errors.Is(err, secureread.ErrSourceBinding) || injected != 1 || listened != 0 {
		t.Fatal("late declaration reached listener", err, injected, listened)
	}
	if api.SecureLocalProjectOwner() || api.SecureSessionUnrestricted() || api.SecurityContextID() != "" {
		t.Fatal("startup failure retained authority")
	}
}
