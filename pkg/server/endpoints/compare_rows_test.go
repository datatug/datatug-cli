package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/stretchr/testify/require"
)

func TestCompareRowsHandlerRejectsStaleContext(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?comparisonId=ops/p/a%7Cops/p/b&state=matched&securityContextId=missing", nil)
	res := httptest.NewRecorder()

	compareRowsHandler(res, req)

	require.Equal(t, http.StatusConflict, res.Code)
	var envelope apicontract.ErrorEnvelope
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	require.Equal(t, string(apicontract.ErrCodeStaleContext), envelope.Error.Code)
}

func TestCompareRowsHandlerDoesNotLeakCacheWhenSnapshotsAreMissing(t *testing.T) {
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	req := httptest.NewRequest(http.MethodGet, "/datatug/compare/rows?comparisonId=missing&state=matched&securityContextId="+api.SecurityContextID(), nil)
	res := httptest.NewRecorder()

	compareRowsHandler(res, req)

	require.Equal(t, http.StatusServiceUnavailable, res.Code)
	var envelope apicontract.ErrorEnvelope
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
	require.Equal(t, string(apicontract.ErrCodeSourceUnavailable), envelope.Error.Code)
	require.NotContains(t, res.Body.String(), `"rows"`)
}
