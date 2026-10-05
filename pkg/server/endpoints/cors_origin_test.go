package endpoints

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// No answer of a route echoes an origin that is not on the list of IsSupportedOrigin: the writers
// that name an origin (returnJSON, handleError, writeContractResponse, writeContractError and
// writeContractResponseStatus) all go through writeCORSOrigin.
func TestAnswers_NameAnOriginOnlyWhenItIsOnTheList(t *testing.T) {
	writers := map[string]func(w http.ResponseWriter, r *http.Request){
		"returnJSON (get)": func(w http.ResponseWriter, r *http.Request) {
			returnJSON(w, r, http.StatusOK, nil, map[string]string{})
		},
		"returnJSON (post)": func(w http.ResponseWriter, r *http.Request) {
			returnJSON(w, r, http.StatusOK, nil, map[string]string{})
		},
		"returnJSON (error)":    func(w http.ResponseWriter, r *http.Request) { returnJSON(w, r, 0, errors.New("failed"), nil) },
		"handleError":           func(w http.ResponseWriter, r *http.Request) { handleError(errors.New("failed"), w, r) },
		"writeContractResponse": func(w http.ResponseWriter, r *http.Request) { writeContractResponse(w, r, nil, map[string]string{}) },
		"writeContractResponse (error)": func(w http.ResponseWriter, r *http.Request) {
			writeContractResponse(w, r, errors.New("failed"), nil)
		},
		"writeContractResponseStatus": func(w http.ResponseWriter, r *http.Request) {
			writeContractResponseStatus(w, r, http.StatusCreated, map[string]string{})
		},
		"writeContractError": func(w http.ResponseWriter, r *http.Request) { writeContractError(w, r, errors.New("failed")) },
	}
	for name, write := range writers {
		method := http.MethodGet
		if name == "returnJSON (post)" {
			method = http.MethodPost
		}
		for _, origin := range []string{"https://evil.example.com", "null", "https://evil.example.com/.datatug.app", "http://localhost:80@evil.example.com"} {
			r := httptest.NewRequest(method, "/x", nil)
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			write(w, r)
			assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"), "%s answers an origin that is not on the list: %q", name, origin)
		}
		for _, origin := range []string{"https://datatug.app", "http://localhost:4200", "https://app.datatug.app", "http://127.0.0.1:8100"} {
			r := httptest.NewRequest(method, "/x", nil)
			r.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			write(w, r)
			assert.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"), "%s: an origin that is on the list", name)
		}
		w := httptest.NewRecorder()
		write(w, httptest.NewRequest(method, "/x", nil))
		assert.Empty(t, w.Header().Values("Access-Control-Allow-Origin"), "%s: a request with no Origin", name)
	}
}
