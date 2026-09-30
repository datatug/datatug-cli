package commands

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/stretchr/testify/require"
)

// covBCompareResult builds a valid CompareResult that exercises every optional
// section of the human output: a 50-value truncated distribution, a
// rows-filtering limitation and truncated diff rows.
func covBCompareResult(t *testing.T) apicontract.CompareResult {
	t.Helper()
	const total = 51
	side := func(id string) apicontract.CompareSideReceipt {
		return apicontract.CompareSideReceipt{
			Execution:   apicontract.ExecutionRef{StoreID: "st", ProjectID: "p", ExecutionID: id},
			ExecutedAt:  "2026-09-14T08:00:00Z",
			RowCount:    total,
			Limitations: []apicontract.Limitation{{Policy: "row-policy", RowsFiltered: true, HiddenColumns: []string{}}},
		}
	}
	pct := float64(1) * 100 / float64(total)
	ratio := pct / pct
	values := make([]apicontract.CompareDistributionValue, 0, apicontract.CompareDistributionMaximumValues)
	for i := 0; i < apicontract.CompareDistributionMaximumValues; i++ {
		values = append(values, apicontract.CompareDistributionValue{
			Value: apicontract.NewStringValue(fmt.Sprintf("v%02d", i)),
			Left:  apicontract.CompareDistributionSide{Count: 1, Pct: pct},
			Right: apicontract.CompareDistributionSide{Count: 1, Pct: pct},
			Ratio: &ratio,
		})
	}
	result := apicontract.CompareResult{
		Left: side("left"), Right: side("right"),
		Columns: []apicontract.Column{{Name: "id", Type: "string"}},
		Key:     []string{"id"},
		Added:   []apicontract.CompareRow{}, Removed: []apicontract.CompareRow{}, Changed: []apicontract.CompareChangedRow{},
		Summary:       apicontract.CompareSummary{Added: 2, Removed: 2, Unchanged: 49},
		Distribution:  &apicontract.CompareDistribution{Column: "id", Values: values, Truncated: true},
		PolicyLimited: true,
		Truncated:     true,
	}
	require.NoError(t, result.Validate())
	return result
}

func covBCompareServer(t *testing.T, info string, compare http.HandlerFunc) *httptest.Server {
	t.Helper()
	routes := map[string]http.HandlerFunc{}
	if compare != nil {
		routes["/datatug/compare"] = compare
	}
	return covBAgent(t, info, routes)
}

func covBExecSides() []string {
	return []string{"--query", "q", "--left", "execution=st/p/l", "--right", "execution=st/p/r", "--key", "id"}
}

func TestCovBCompareHumanOutputOptionalSections(t *testing.T) {
	server := covBCompareServer(t, covBInfoJSON("p"), covBBody(covBJSON(t, covBCompareResult(t))))
	args := append([]string{"--agent", server.URL + "/", "--limit", "10"}, covBExecSides()...)
	out, err := covBRun(compareCommand(), args...)
	require.NoError(t, err)
	require.Contains(t, out, "distribution id:")
	require.Contains(t, out, "ratio 1")
	require.Contains(t, out, "distribution values are truncated")
	require.Contains(t, out, "comparison is limited by the current access policy")
	require.Contains(t, out, "diff rows are truncated")
}

func TestCovBCompareRequestErrors(t *testing.T) {
	two := covBCompareServer(t, covBInfoJSON("a", "b"), nil)
	one := covBCompareServer(t, covBInfoJSON("a"), func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "must not be reached", http.StatusTeapot)
	})
	cases := map[string]struct {
		server *httptest.Server
		args   []string
		want   string
	}{
		"project required": {two, []string{"--query", "q", "--left", "env=prod", "--right", "env=uat"}, "--project is required"},
		"bad left":         {one, []string{"--query", "q", "--left", "bogus", "--right", "env=uat"}, "--left:"},
		"bad right":        {one, []string{"--query", "q", "--left", "env=prod", "--right", "bogus"}, "--right:"},
		"bad incident":     {one, append(covBExecSides(), "--incident", "nope"), "--incident:"},
		"incident projects": {one, []string{
			"--query", "q", "--left", "execution=st/a/l", "--right", "execution=st/b/r", "--key", "id",
			"--incident", "st/inc", "--mutation", "m1",
		}, "one unambiguous project"},
		"invalid request": {one, []string{"--query", "q", "--left", "execution=st/p/l", "--right", "execution=st/p/r"}, "key"},
		"agent status":    {one, append(covBExecSides(), "--limit", "5"), "HTTP 418"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := covBRun(compareCommand(), append([]string{"--agent", tc.server.URL}, tc.args...)...)
			require.ErrorContains(t, err, tc.want)
		})
	}

	// Env sides default the project from the single served project; the
	// request reaches the agent (which then rejects it).
	_, err := covBRun(compareCommand(), "--agent", one.URL, "--query", "q", "--left", "env=prod", "--right", "env=uat", "--key", "id")
	require.ErrorContains(t, err, "HTTP 418")
}

func TestCovBCompareAgentFailureAndBadResponses(t *testing.T) {
	envelope := covBJSON(t, apicontract.CompareErrorResponse{Error: apicontract.ErrorBody{Code: "NOT_FOUND", Message: "gone", RequestID: "r"}})
	failing := covBCompareServer(t, covBInfoJSON("p"), covBStatus(404, envelope))
	base := covBExecSides()

	_, err := covBRun(compareCommand(), append([]string{"--agent", failing.URL}, base...)...)
	require.ErrorContains(t, err, "NOT_FOUND: gone")

	out, err := covBRun(compareCommand(), append([]string{"--agent", failing.URL, "--json"}, base...)...)
	require.ErrorContains(t, err, "compare failed")
	require.Contains(t, out, envelope)

	badJSON := covBCompareServer(t, covBInfoJSON("p"), covBBody("not json"))
	_, err = covBRun(compareCommand(), append([]string{"--agent", badJSON.URL}, base...)...)
	require.ErrorContains(t, err, "decode DataTug compare response")

	invalid := covBCompareResult(t)
	invalid.Key = nil
	invalidServer := covBCompareServer(t, covBInfoJSON("p"), covBBody(covBJSON(t, invalid)))
	_, err = covBRun(compareCommand(), append([]string{"--agent", invalidServer.URL}, base...)...)
	require.ErrorContains(t, err, "validate DataTug compare response")

	okServer := covBCompareServer(t, covBInfoJSON("p"), covBBody(covBJSON(t, covBCompareResult(t))))
	command := compareCommand()
	command.SetOut(covBFailWriter{})
	command.SetErr(covBFailWriter{})
	command.SetArgs(append([]string{"--agent", okServer.URL, "--json"}, base...))
	require.ErrorContains(t, command.ExecuteContext(context.Background()), "covB write failed")
}

func TestCovBCompareIncidentSameProjectReachesAgent(t *testing.T) {
	seen := make(chan string, 1)
	server := covBCompareServer(t, covBInfoJSON("p"), func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(covBJSON(t, covBCompareResult(t))))
	})
	args := append([]string{"--agent", server.URL, "--incident", "st/inc", "--mutation", "m1"}, covBExecSides()...)
	_, err := covBRun(compareCommand(), args...)
	require.NoError(t, err)
	require.Equal(t, "application/json", <-seen)
}

func TestCovBAgentHTTPClientSetup(t *testing.T) {
	// Default agent resolved from settings.
	server := covBCompareServer(t, covBInfoJSON("p"), covBBody(covBJSON(t, covBCompareResult(t))))
	covBSetSettings(t, func() (dtconfig.Settings, error) { return covBServerSettings(t, server.URL), nil })
	_, err := covBRun(compareCommand(), covBExecSides()...)
	require.NoError(t, err)

	covBSetSettings(t, func() (dtconfig.Settings, error) { return dtconfig.Settings{}, errors.New("covB settings broken") })
	_, err = covBRun(compareCommand(), covBExecSides()...)
	require.ErrorContains(t, err, "covB settings broken")

	dead := covBDeadURL(t)
	covBSetSettings(t, func() (dtconfig.Settings, error) {
		return covBServerSettings(t, dead), fmt.Errorf("wrapped: %w", os.ErrNotExist)
	})
	_, err = covBRun(compareCommand(), covBExecSides()...)
	require.ErrorContains(t, err, "DataTug agent request")

	// Undecodable agent-info.
	badInfo := covBCompareServer(t, "not json", nil)
	_, err = covBRun(compareCommand(), append([]string{"--agent", badInfo.URL}, covBExecSides()...)...)
	require.ErrorContains(t, err, "decode DataTug agent-info")
}

func TestCovBAgentHTTPClientTransportFailures(t *testing.T) {
	ctx := context.Background()
	badURL := agentHTTPClient{baseURL: "http://[::1", client: http.DefaultClient}
	_, _, err := badURL.get(ctx, "/x")
	require.Error(t, err)
	_, _, err = badURL.post(ctx, "/x", map[string]string{})
	require.Error(t, err)

	good := agentHTTPClient{baseURL: covBDeadURL(t), client: http.DefaultClient}
	_, _, err = good.post(ctx, "/x", make(chan int))
	require.Error(t, err)
	_, _, err = good.get(ctx, "/x")
	require.ErrorContains(t, err, "DataTug agent request")

	short := httptest.NewServer(http.HandlerFunc(covBTruncatedBody))
	t.Cleanup(short.Close)
	_, status, err := agentHTTPClient{baseURL: short.URL, client: http.DefaultClient}.get(ctx, "/x")
	require.Error(t, err)
	require.Equal(t, http.StatusOK, status)
}
