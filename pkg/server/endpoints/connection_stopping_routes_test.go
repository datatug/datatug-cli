package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A route that is not implemented answers 501 with a sentence built here, and never stops the
// connection with no answer.
func TestRoutes_ARouteThatIsNotImplementedAnswers501WithABuiltSentence(t *testing.T) {
	quietLog(t)
	for _, tc := range []struct {
		name     string
		handler  http.HandlerFunc
		request  func() *http.Request
		sentence string
	}{
		{
			"recordsets/recordset_data", getRecordsetData,
			func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/datatug/recordsets/recordset_data?project=p1&id=r1", nil)
			},
			recordsetDataNotImplementedSentence,
		},
		{
			"projects/create_project", ProjectAgentEndpoints{}.createProject,
			func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/datatug/projects/create_project?store=files", strings.NewReader(`{"id":"new-project","title":"New project"}`))
			},
			createProjectNotImplementedSentence,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.request()
			r.Header.Set("Origin", "https://datatug.app")
			w := httptest.NewRecorder()
			require.NotPanics(t, func() { tc.handler(w, r) })
			assert.Equal(t, http.StatusNotImplemented, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			assert.Equal(t, "https://datatug.app", w.Header().Get("Access-Control-Allow-Origin"), "a page of the app can read the answer")
			var answer ErrorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &answer), w.Body.String())
			assert.Equal(t, tc.sentence, answer.Error)
			assert.Equal(t, "NOT_IMPLEMENTED", answer.Code)
		})
	}
}

// An answer that cannot be written is logged, and nothing else happens.
func TestWriteNotImplemented_AnAnswerThatCannotBeWrittenIsLogged(t *testing.T) {
	logged := captureAgentLog(t)
	w := &failWriter{ResponseRecorder: *httptest.NewRecorder()}
	writeNotImplemented(w, httptest.NewRequest(http.MethodGet, "/", nil), "not implemented")
	assert.Equal(t, http.StatusNotImplemented, w.Code)
	assert.Contains(t, logged.String(), "Failed to encode error")
}

// create_project stays behind the write capability: without it the route answers the refusal of
// every write, as before.
func TestRoutes_CreateProjectIsAnswered501OnlyWithTheWriteCapability(t *testing.T) {
	quietLog(t)
	for _, tc := range []struct {
		name   string
		caps   Capabilities
		status int
	}{
		{"without writes", Capabilities{}, http.StatusForbidden},
		{"with writes", Capabilities{AllowWrites: true}, http.StatusNotImplemented},
	} {
		recorder := &routeRecorder{}
		registerRoutes("", recorder, nil, false, tc.caps)
		for _, route := range recorder.routes {
			if route.path != "/datatug/projects/create_project" {
				continue
			}
			w := httptest.NewRecorder()
			route.handler(w, guardRequest(http.MethodPost, route.path, "127.0.0.1:8989", "", ""))
			assert.Equal(t, tc.status, w.Code, tc.name)
		}
	}
}

// recordset_add_rows takes the number of rows from the client: the number is bounded, a bad one is a
// 400 with a sentence that names nothing the client wrote, and the log does not hold the text of the
// client either.
func TestRoutes_AddRowsBoundsTheCountAndLogsNothingOfTheClient(t *testing.T) {
	post := func(count string) (*httptest.ResponseRecorder, string) {
		logged := captureAgentLog(t)
		q := url.Values{"project": {"p1"}, "recordset": {"r1"}, "data": {"d1"}}
		if count != "" {
			q.Set("count", count)
		}
		w := httptest.NewRecorder()
		addRowsToRecordset(w, httptest.NewRequest(http.MethodPost, "/datatug/recordsets/recordset_add_rows?"+q.Encode(), bytes.NewBufferString(`[]`)))
		return w, logged.String()
	}

	for _, count := range []string{"-1", "-9223372036854775808", strconv.Itoa(maxRecordsetRowsCount + 1), "1000000000000", "99999999999999999999", "MARKER-not-a-number", "1.5", " 7 "} {
		w, logged := post(count)
		assert.Equal(t, http.StatusBadRequest, w.Code, "count=%q: %s", count, w.Body.String())
		assert.Equal(t, `bad value for field [count]: `+recordsetCountSentence, answerMessage(w.Body.String()), "count=%q", count)
		assert.NotContains(t, w.Body.String(), "MARKER", "count=%q", count)
		assert.NotContains(t, logged, "MARKER", "count=%q: the log holds the text of the client", count)
		assert.NotContains(t, logged, count, "count=%q: the log holds the text of the client", count)
	}

	// A count in the bound, and no count, reach the api as before (which answers that adding rows is not implemented).
	for _, count := range []string{"", "0", "5", strconv.Itoa(maxRecordsetRowsCount)} {
		w, logged := post(count)
		assert.NotEqual(t, http.StatusBadRequest, w.Code, "count=%q: %s", count, w.Body.String())
		assert.NotContains(t, strings.ToLower(logged), "warning", "count=%q", count)
	}

	// A body that is not a JSON list of rows is a 400 that says so, and does not quote the decoder.
	w := httptest.NewRecorder()
	addRowsToRecordset(w, httptest.NewRequest(http.MethodPost, "/datatug/recordsets/recordset_add_rows?project=p1&recordset=r1&data=d1", bytes.NewBufferString(`{"MARKER-key": 1}`)))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.NotContains(t, w.Body.String(), "MARKER")
	assert.NotContains(t, w.Body.String(), "json:")
}

// A ?root= that is not "shared" or "personal" is a 400 that names the field and the two values, and
// does not quote the value the client sent.
func TestRoutes_AllQueriesDoesNotQuoteRoot(t *testing.T) {
	for _, root := range []string{"MARKER-root", "../../etc", `"quoted"`, strings.Repeat("a", 300)} {
		_, err := parseQueriesRoot(url.Values{"root": {root}})
		require.ErrorIs(t, err, ErrInvalidQueriesRoot)
		assert.Equal(t, `invalid queries root: must be "shared" or "personal"`, err.Error())

		w := httptest.NewRecorder()
		handleError(err, w, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.NotContains(t, w.Body.String(), root)
		assert.Contains(t, w.Body.String(), `"field":"root"`)
	}
}
