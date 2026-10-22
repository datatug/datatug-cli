package endpoints

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/stretchr/testify/require"
)

type cardRouteRecorder map[string]bool

func (r cardRouteRecorder) HandlerFunc(method, path string, _ http.HandlerFunc) {
	r[method+" "+path] = true
}

func TestCardsRoutesAreRegistered(t *testing.T) {
	all := cardRouteRecorder{}
	registerRoutesOn("", all, nil, false, Capabilities{})
	for _, want := range []string{
		"GET /datatug/cards/all_cards", "GET /datatug/cards/card", "POST /datatug/cards/create_card",
		"PUT /datatug/cards/save_card", "DELETE /datatug/cards/delete_card",
	} {
		require.True(t, all[want], want)
	}
	writeOnly := cardRouteRecorder{}
	registerRoutesOn("", writeOnly, nil, true, Capabilities{})
	require.False(t, writeOnly["GET /datatug/cards/all_cards"])
	require.False(t, writeOnly["GET /datatug/cards/card"])
	require.True(t, writeOnly["POST /datatug/cards/create_card"])
	require.True(t, writeOnly["DELETE /datatug/cards/delete_card"])
}

const customerCardJSON = `{"id":"customer","title":"Customer","display":"panel","parameters":[{"id":"customerId","type":"string","isRequired":true,"match":[{"column":"CustomerID"}]}],"widget":{"name":"text","data":{"markdown":"Customer **{{customerId}}**"}}}`

func TestCardEndpointsRoundTrip(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	cardFile := filepath.Join(projectDir, "cards", "customer", "card.json")
	project := url.Values{"project": {scope.Project}}
	item := func(id string) url.Values { return url.Values{"project": {scope.Project}, "id": {id}} }
	send := func(handler http.HandlerFunc, method, path string, query url.Values, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path+"?"+query.Encode(), strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		handler(w, r)
		return w
	}

	// Nothing yet.
	w := send(getCards, http.MethodGet, "/datatug/cards/all_cards", project, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `[]`, w.Body.String())

	// create_card writes the folder form and answers 201.
	w = send(createCard, http.MethodPost, "/datatug/cards/create_card", project, customerCardJSON)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.FileExists(t, cardFile)
	w = send(createCard, http.MethodPost, "/datatug/cards/create_card", project, customerCardJSON)
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "already exists")
	w = send(createCard, http.MethodPost, "/datatug/cards/create_card", project, `{"id":"../x","title":"T","widget":{"name":"text"}}`)
	require.NotEqual(t, http.StatusCreated, w.Code)
	w = send(createCard, http.MethodPost, "/datatug/cards/create_card", project, `{"id":"extra","title":"T","unknown":1,"widget":{"name":"text"}}`)
	require.NotEqual(t, http.StatusCreated, w.Code)
	require.NoFileExists(t, filepath.Join(projectDir, "cards", "extra", "card.json"))

	// all_cards lists briefs with the match rules; card returns the full card.
	w = send(getCards, http.MethodGet, "/datatug/cards/all_cards", project, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.JSONEq(t, `[{"id":"customer","title":"Customer","display":"panel","parameters":[{"id":"customerId","type":"string","isRequired":true,"match":[{"column":"CustomerID"}]}]}]`, w.Body.String())
	w = send(getCard, http.MethodGet, "/datatug/cards/card", item("customer"), "")
	// Like boards/board, the unqualified read answers through getProjectItem, a 201.
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"markdown":"Customer **{{customerId}}**"`)
	w = send(getCard, http.MethodGet, "/datatug/cards/card", item("missing"), "")
	require.NotEqual(t, http.StatusOK, w.Code)

	// save_card replaces it.
	w = send(saveCard, http.MethodPut, "/datatug/cards/save_card", item("customer"), strings.Replace(customerCardJSON, `"Customer","display"`, `"Customer 2","display"`, 1))
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	// An id in the query that disagrees with the body is refused and writes nothing.
	w = send(saveCard, http.MethodPut, "/datatug/cards/save_card", item("customer"), strings.Replace(customerCardJSON, `"id":"customer"`, `"id":"other"`, 1))
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.NoFileExists(t, filepath.Join(projectDir, "cards", "other", "card.json"))
	raw, err := os.ReadFile(cardFile)
	require.NoError(t, err)
	require.Contains(t, string(raw), "Customer 2")

	// The same reads at the checked-out branch.
	runGit(t, projectDir, "add", "-A")
	runGit(t, projectDir, "commit", "-q", "-m", "cards")
	branch := runGit(t, projectDir, "symbolic-ref", "--short", "HEAD")
	withBranch := func(v url.Values) url.Values { v.Set("branch", branch); return v }
	w = send(getCards, http.MethodGet, "/datatug/cards/all_cards", withBranch(url.Values{"project": {scope.Project}, "storage": {api.LocalStoreID}}), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"title":"Customer 2"`)
	w = send(getCard, http.MethodGet, "/datatug/cards/card", withBranch(url.Values{"project": {scope.Project}, "id": {"customer"}, "storage": {api.LocalStoreID}}), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"id":"customer"`)
	w = send(getCard, http.MethodGet, "/datatug/cards/card", withBranch(url.Values{"project": {scope.Project}, "id": {"missing"}, "storage": {api.LocalStoreID}}), "")
	require.NotEqual(t, http.StatusOK, w.Code)
	w = send(getCard, http.MethodGet, "/datatug/cards/card", withBranch(url.Values{"project": {scope.Project}, "storage": {api.LocalStoreID}}), "")
	require.NotEqual(t, http.StatusOK, w.Code, "an item request needs an id")
	w = send(getCards, http.MethodGet, "/datatug/cards/all_cards", withBranch(url.Values{"storage": {api.LocalStoreID}}), "")
	require.NotEqual(t, http.StatusOK, w.Code, "a project is required")

	// An unsafe project is refused before any read.
	w = send(getCards, http.MethodGet, "/datatug/cards/all_cards", withBranch(url.Values{"project": {"../x"}}), "")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = send(getCard, http.MethodGet, "/datatug/cards/card", withBranch(url.Values{"project": {"../x"}, "id": {"customer"}}), "")
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	// delete_card: 404 for a missing card, 200 and the file is gone for a present one.
	w = send(deleteCard, http.MethodDelete, "/datatug/cards/delete_card", item("missing"), "")
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	w = send(deleteCard, http.MethodDelete, "/datatug/cards/delete_card", item("customer"), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoFileExists(t, cardFile)
	require.NoDirExists(t, filepath.Dir(cardFile))
	w = send(getCards, http.MethodGet, "/datatug/cards/all_cards", project, "")
	require.JSONEq(t, `[]`, w.Body.String())
}

func TestCardEndpointsRefuseWithoutLocalReadPrincipal(t *testing.T) {
	withApicoreHandle(t)
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	for name, call := range map[string]func(w http.ResponseWriter, r *http.Request){"all": getCards, "one": getCard} {
		w := httptest.NewRecorder()
		call(w, httptest.NewRequest(http.MethodGet, "/x?project=p&branch=main&id=c", nil))
		require.NotEqual(t, http.StatusOK, w.Code, name)
	}
}
