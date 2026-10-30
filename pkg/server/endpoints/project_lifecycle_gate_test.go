package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
)

func TestRegisteredLegacyDefinitionDeletionRequiresLifecycleReview(t *testing.T) {
	for _, path := range []string{
		"folders/delete_folder", "queries/delete_query", "boards/delete_board", "cards/delete_card",
		"projects/delete_project", "dbserver-delete", "entities/delete_entity",
	} {
		t.Run(path, func(t *testing.T) {
			router := httprouter.New()
			// Use the actual guarded route table. No API/store is configured:
			// executing an old handler would fail or panic instead of returning
			// this stable refusal before any project or history is touched.
			registerRoutes("", router, nil, false, Capabilities{AllowWrites: true})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "http://localhost/datatug/"+path+"?storage=local&project=p&id=q", nil))
			if response.Code != http.StatusConflict {
				t.Fatalf("legacy deletion returned %d: %s", response.Code, response.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Code != "LIFECYCLE_REVIEW_REQUIRED" {
				t.Fatalf("missing lifecycle refusal: %s (%v)", response.Body.String(), err)
			}
			if response.Header().Get("Cache-Control") != "no-store, no-transform" {
				t.Fatal("lifecycle refusal must not be cached")
			}
		})
	}
}

func TestLegacyLifecycleGatePreservesWriteAndOriginGuards(t *testing.T) {
	for _, tc := range []struct {
		name, origin string
		writes       bool
	}{
		{"writes disabled", "", false},
		{"untrusted origin", "https://untrusted.invalid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := httprouter.New()
			registerRoutes("", router, nil, false, Capabilities{AllowWrites: tc.writes})
			request := httptest.NewRequest(http.MethodDelete, "http://localhost/datatug/boards/delete_board", nil)
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("guard returned %d", response.Code)
			}
		})
	}
}
