package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/stretchr/testify/require"
)

// Exercise the registered request/session/DTO/storage pipeline used by serve,
// with an owned repository and actual filestore rather than a fake handle.
func nativeBoardHTTP(t *testing.T) (*httprouter.Router, string, string) {
	t.Helper()
	withApicoreHandle(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"board-project","title":"Boards","created":{"at":"2026-10-29T00:00:00Z"}}`), 0o600))
	gitInit(t, dir)
	paths := map[string]string{"board-project": dir}
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() {
		storage.NewDatatugStore = previous
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "board-operator"}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true})
	secret := bytes.Repeat([]byte{0x71}, 32)
	router := httprouter.New()
	RegisterDatatugHandlersWithCapabilities("", router, RegisterAllHandlers, nil, func(r *http.Request) (context.Context, error) { return r.Context(), nil }, apicore.Execute, Capabilities{AllowWrites: true, Access: agentaccess.New(secret)})
	return router, dir, agentaccess.SessionToken(secret)
}

func nativeBoardRequest(t *testing.T, router http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://localhost"+path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://datatug.localhost:4200")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestNativeBoardCreateRegisteredHTTPPersistsServerIdentity(t *testing.T) {
	router, dir, token := nativeBoardHTTP(t)
	response := nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?project=board-project", `{"title":"New native board","folder":"~"}`, token)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created datatug.Board
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	require.NoError(t, created.Validate())
	require.Equal(t, "New native board", created.Title)
	require.FileExists(t, filepath.Join(dir, "boards", created.ID, "board.json"))
	// A fresh registered read hydrates the exact persisted definition.
	loaded := nativeBoardRequest(t, router, http.MethodGet, "/datatug/boards/board?project=board-project&id="+created.ID, "", token)
	require.Equal(t, http.StatusCreated, loaded.Code, loaded.Body.String())
	var board datatug.Board
	require.NoError(t, json.Unmarshal(loaded.Body.Bytes(), &board))
	require.Equal(t, created, board)
}

func TestNativeBoardCreateUsesTheSameExplicitBranchAsCapabilityDiscovery(t *testing.T) {
	router, dir, token := nativeBoardHTTP(t)
	runGit(t, dir, "switch", "-q", "-c", "feature/boards")
	branch := runGit(t, dir, "symbolic-ref", "--short", "HEAD")
	selector := "storage=local&project=board-project&branch=" + url.QueryEscape(branch)
	capability := nativeBoardRequest(t, router, http.MethodGet, "/datatug/projects/capabilities?"+selector, "", token)
	require.Equal(t, http.StatusOK, capability.Code, capability.Body.String())
	var caps map[string]any
	require.NoError(t, json.Unmarshal(capability.Body.Bytes(), &caps))
	require.Equal(t, true, caps["guardedBoardCreate"])
	response := nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?"+selector, `{"title":"Explicit branch Board","folder":"~"}`, token)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var created datatug.Board
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &created))
	loaded := nativeBoardRequest(t, router, http.MethodGet, "/datatug/boards/board?"+selector+"&id="+created.ID, "", token)
	require.Equal(t, http.StatusOK, loaded.Code, loaded.Body.String())
	var reloaded datatug.Board
	require.NoError(t, json.Unmarshal(loaded.Body.Bytes(), &reloaded))
	require.Equal(t, created, reloaded)
	require.Equal(t, branch, runGit(t, dir, "symbolic-ref", "--short", "HEAD"))
}

func TestNativeBoardCreateRejectsAmbiguousOrMalformedSelectors(t *testing.T) {
	for _, selector := range []string{
		"storage=local&project=board-project&branch=&branch=other",
		"storage=local&project=board-project&project=other",
		"storage=local&project=board-project&storage=other",
		"storage=local&project=board-project&branch=%zz",
		"storage=local&project=board-project&extra=true",
		"storage=local&project=board-project&proj=other",
	} {
		t.Run(selector, func(t *testing.T) {
			router, dir, token := nativeBoardHTTP(t)
			before := treeDigest(t, dir)
			response := nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?"+selector, `{"title":"Ambiguous target","folder":"~"}`, token)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			assertTreeUnchanged(t, dir, before)
		})
	}
}

func TestNativeBoardCreateKeepsFullValidationAndRequestGuards(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, token string
		status                  int
	}{
		{"title required", "project=board-project", `{"folder":"~"}`, "valid", 400},
		{"folder confinement", "project=board-project", `{"title":"Invalid","folder":"outside"}`, "valid", 400},
		{"invalid parameter", "project=board-project", `{"title":"Invalid","parameters":[{}]}`, "valid", 400},
		{"invalid widget", "project=board-project", `{"title":"Invalid","rows":[{"cards":[{"id":"card","widget":{}}]}]}`, "valid", 400},
		{"missing project", "", `{"title":"Invalid","folder":"~"}`, "valid", 400},
		{"unsafe project", "project=..%2Foutside", `{"title":"Invalid","folder":"~"}`, "valid", 400},
		{"missing access token", "project=board-project", `{"title":"Unauthenticated","folder":"~"}`, "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, dir, token := nativeBoardHTTP(t)
			before := treeDigest(t, dir)
			if tc.token == "" {
				token = ""
			}
			response := nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?"+tc.path, tc.body, token)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			assertTreeUnchanged(t, dir, before)
		})
	}
}

func TestNativeBoardCreateNeverUsesCallerIDOrOverwritesACollision(t *testing.T) {
	router, dir, token := nativeBoardHTTP(t)
	previous := generateBoardID
	generateBoardID = func() string { return "server-id" }
	t.Cleanup(func() { generateBoardID = previous })
	response := nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?project=board-project", `{"id":"client-target","title":"Server owned","folder":"~"}`, token)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var board datatug.Board
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &board))
	require.Equal(t, "server-id", board.ID)
	require.NoDirExists(t, filepath.Join(dir, "boards", "client-target"))
	file := filepath.Join(dir, "boards", "server-id", "board.json")
	before := treeDigest(t, dir)
	response = nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?project=board-project", `{"id":"different-client","title":"Must not replace","folder":"~"}`, token)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "already exists")
	assertTreeUnchanged(t, dir, before)
	// A corrupt retained definition also cannot be interpreted as absent.
	require.NoError(t, os.WriteFile(file, []byte(`{"id":"server-id",broken`), 0o600))
	before = treeDigest(t, dir)
	response = nativeBoardRequest(t, router, http.MethodPost, "/datatug/boards/create_board?project=board-project", `{"title":"Must not replace corrupt data","folder":"~"}`, token)
	require.NotEqual(t, http.StatusCreated, response.Code, response.Body.String())
	assertTreeUnchanged(t, dir, before)
}

func TestNativeBoardCreateRefusesMissingOrRestrictedServerPrincipalAndWrongBranch(t *testing.T) {
	for _, change := range []string{"missing principal", "restricted principal", "writes revoked", "wrong branch"} {
		t.Run(change, func(t *testing.T) {
			router, dir, token := nativeBoardHTTP(t)
			session := secureread.Session{Principal: &access.Principal{ID: "operator"}, Unrestricted: true}
			caps := api.Capabilities{AllowWrites: true}
			if change == "missing principal" {
				session.Principal = nil
				session.Unrestricted = false
			}
			if change == "restricted principal" {
				session.Unrestricted = false
			}
			if change == "writes revoked" {
				caps.AllowWrites = false
			}
			api.ConfigureSecureSession(session, map[string]string{"board-project": dir}, caps)
			path := "/datatug/boards/create_board?project=board-project"
			if change == "wrong branch" {
				path += "&branch=wrong-branch"
			}
			before := treeDigest(t, dir)
			response := nativeBoardRequest(t, router, http.MethodPost, path, `{"title":"No authority","folder":"~"}`, token)
			require.NotEqual(t, http.StatusCreated, response.Code, response.Body.String())
			assertTreeUnchanged(t, dir, before)
		})
	}
}

func TestNativeProjectCapabilitiesExplicitlyAdvertisesGuardedBoardCreation(t *testing.T) {
	router, _, token := nativeBoardHTTP(t)
	response := nativeBoardRequest(t, router, http.MethodGet, "/datatug/projects/capabilities?storage=local&project=board-project", "", token)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var capabilities map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &capabilities))
	require.Equal(t, true, capabilities["guardedBoardCreate"])
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
}

func TestNativeGuardedBoardCapabilityRetainsAgentAccessGuard(t *testing.T) {
	router, dir, _ := nativeBoardHTTP(t)
	response := nativeBoardRequest(t, router, http.MethodGet, "/datatug/projects/capabilities?storage=local&project=board-project", "", "")
	require.Equal(t, http.StatusUnauthorized, response.Code, response.Body.String())
	require.NoDirExists(t, filepath.Join(dir, "boards"))
}

func TestNativeGuardedBoardCapabilityFailsClosedForUnavailableOrAmbiguousScope(t *testing.T) {
	for _, kind := range []string{"readonly", "restricted", "custom provider", "wrong branch", "duplicate branch", "duplicate project", "unknown selector", "malformed selector"} {
		t.Run(kind, func(t *testing.T) {
			router, dir, token := nativeBoardHTTP(t)
			path := "/datatug/projects/capabilities?storage=local&project=board-project"
			switch kind {
			case "readonly", "restricted":
				api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "board-operator"}, Unrestricted: kind != "restricted"}, map[string]string{"board-project": dir}, api.Capabilities{AllowWrites: kind != "readonly"})
			case "custom provider":
				previous := storage.NewDatatugStore
				t.Cleanup(func() { storage.NewDatatugStore = previous })
				storage.NewDatatugStore = func(string) (storage.Store, error) {
					s, err := filestore.NewStore("files", map[string]string{"board-project": dir})
					return boardCapabilityHiddenStore{Store: s}, err
				}
			case "wrong branch":
				path += "&branch=does-not-exist"
			case "duplicate branch":
				path += "&branch=&branch=does-not-exist"
			case "duplicate project":
				path += "&project=unserved"
			case "unknown selector":
				path += "&extra=true"
			case "malformed selector":
				path += "&branch=%zz"
			}
			response := nativeBoardRequest(t, router, http.MethodGet, path, "", token)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			require.Equal(t, false, body["guardedBoardCreate"])
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			require.NoDirExists(t, filepath.Join(dir, "boards"))
		})
	}
}

type boardCapabilityHiddenStore struct{ storage.Store }

func (s boardCapabilityHiddenStore) GetProjectStore(id string) datatug.ProjectStore {
	// Keep every existing ProjectStore method but deliberately omit optional
	// GuardedBoardCreator, as a custom/older provider would.
	return struct{ datatug.ProjectStore }{s.Store.GetProjectStore(id)}
}
