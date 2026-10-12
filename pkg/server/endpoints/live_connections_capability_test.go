package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The live connection of the db-server databases route is a capability, off by default: the route
// connects to a recorded server under the identity of the person who runs the server, so without
// `datatug serve --allow-live-connections` it answers a fixed 403 before anything is looked up or
// connected to.

func TestRequireLiveConnections(t *testing.T) {
	called := 0
	handler := func(w http.ResponseWriter, _ *http.Request) { called++; w.WriteHeader(http.StatusTeapot) }

	t.Run("refused by default", func(t *testing.T) {
		w := httptest.NewRecorder()
		requireLiveConnections(Capabilities{}, handler)(w, httptest.NewRequest(http.MethodGet, "/datatug/dbserver-databases?host=db.example.com&driver=sqlserver", nil))
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Zero(t, called)
		var envelope struct {
			Error struct{ Code, Message string }
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope), w.Body.String())
		assert.Equal(t, "ACCESS_DENIED", envelope.Error.Code)
		assert.Equal(t, liveConnectionsRefusedSentence, envelope.Error.Message)
		assert.Contains(t, envelope.Error.Message, "--allow-live-connections")
		assert.NotContains(t, w.Body.String(), "db.example.com")
	})

	t.Run("served when the process was started to allow it", func(t *testing.T) {
		w := httptest.NewRecorder()
		requireLiveConnections(Capabilities{AllowLiveConnections: true}, handler)(w, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusTeapot, w.Code)
		assert.Equal(t, 1, called)
	})

	t.Run("allowing writes does not allow it", func(t *testing.T) {
		w := httptest.NewRecorder()
		requireLiveConnections(Capabilities{AllowWrites: true}, handler)(w, httptest.NewRequest(http.MethodGet, "/", nil))
		assert.Equal(t, http.StatusForbidden, w.Code)
	})
}

// The route of the table is behind the gate: with the capability off nothing reaches the api, with
// it on the request does.
func TestRoutes_DbServerDatabasesIsBehindTheLiveConnectionsCapability(t *testing.T) {
	quietLog(t)
	reached := 0
	saved := getServerDatabasesFunc
	t.Cleanup(func() { getServerDatabasesFunc = saved })
	getServerDatabasesFunc = func(context.Context, dto.GetServerDatabasesRequest) ([]*datatug.DbCatalog, error) {
		reached++
		return nil, nil
	}
	const query = "/datatug/dbserver-databases?proj=p1&driver=sqlserver&host=db.example.com&port=1433"

	for _, tc := range []struct {
		name    string
		caps    Capabilities
		status  int
		reached int
	}{
		{"off by default", Capabilities{}, http.StatusForbidden, 0},
		{"writes allowed, live connections not", Capabilities{AllowWrites: true}, http.StatusForbidden, 0},
		{"live connections allowed", Capabilities{AllowLiveConnections: true}, http.StatusOK, 1},
	} {
		reached = 0
		recorder := &routeRecorder{}
		registerRoutes("", recorder, nil, false, tc.caps)
		var found bool
		for _, route := range recorder.routes {
			if route.method == http.MethodGet && route.path == "/datatug/dbserver-databases" {
				found = true
				w := httptest.NewRecorder()
				route.handler(w, guardRequest(http.MethodGet, query, "127.0.0.1:8989", "", ""))
				assert.Equal(t, tc.status, w.Code, tc.name)
			}
		}
		assert.True(t, found, tc.name)
		assert.Equal(t, tc.reached, reached, tc.name)
	}
}
