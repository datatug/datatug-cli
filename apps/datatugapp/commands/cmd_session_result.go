package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"

	"github.com/datatug/datatug-cli/pkg/sessionresultcache"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/spf13/cobra"
)

// dashboardCacheBinding performs fresh server authorization before local bytes
// are touched. The server's actor ownership and exact input scope are binding.
func (c sessionCLI) dashboardCacheBinding(ctx context.Context, runID string) (sessionresultcache.Binding, sessioncontext.Run, error) {
	var run sessioncontext.Run
	if _, err := c.request(ctx, http.MethodGet, "/runs/"+url.PathEscape(runID), nil, &run); err != nil {
		return sessionresultcache.Binding{}, run, err
	}
	if run.ID != runID || run.Kind != "dashboard" || run.ExecutionAuthority != "client" || run.DefinitionID != sessionInvoiceDashboard || run.DefinitionRevision != sessionInvoiceDashboardRevision {
		return sessionresultcache.Binding{}, run, errors.New("run has no supported local dashboard display")
	}
	var input struct {
		Input     sessioncontext.InputSnapshot   `json:"input"`
		Revisions []sessioncontext.EntryRevision `json:"revisions"`
	}
	if _, err := c.request(ctx, http.MethodGet, "/inputs/"+url.PathEscape(run.InputSnapshotID), nil, &input); err != nil {
		return sessionresultcache.Binding{}, run, err
	}
	snapshot, err := c.snapshot(ctx, run.SessionID)
	if err != nil {
		return sessionresultcache.Binding{}, run, err
	}
	if input.Input.ID != run.InputSnapshotID || input.Input.SessionID != run.SessionID || snapshot.Session.ID != run.SessionID || snapshot.Session.Owner == "" || snapshot.Session.DatabaseID != c.caps.DatabaseID || snapshot.Session.ProjectID != c.caps.ProjectID {
		return sessionresultcache.Binding{}, run, errors.New("dashboard cache scope does not match its authorized run")
	}
	authority := c.base + "|" + c.query.Get("storage") + "|" + c.query.Get("project") + "|" + c.query.Get("spaceID")
	return sessionresultcache.Binding{Authority: authority, Actor: snapshot.Session.Owner, DatabaseID: c.caps.DatabaseID, ProjectID: c.caps.ProjectID, SessionID: run.SessionID, RunID: run.ID, InputSnapshotID: input.Input.ID, Scope: input.Input.Scope, DefinitionDigest: input.Input.DefinitionDigest}, run, nil
}
func dashboardWidgetOutcomes(result sessionDashboardResult) []sessioncontext.WidgetOutcome {
	widgets := []sessioncontext.WidgetOutcome{}
	for _, widget := range []struct {
		ID   string
		Data apicontract.Recordset
	}{{"invoice-lines", result.Grid}, {"invoice-by-genre", result.Chart}} {
		encoded, _ := json.Marshal(widget.Data)
		digest := sha256.Sum256(encoded)
		widgets = append(widgets, sessioncontext.WidgetOutcome{ID: widget.ID, State: "completed", ResultRef: "sha256-" + hex.EncodeToString(digest[:])})
	}
	return widgets
}
func (c sessionCLI) cacheDashboardDisplay(ctx context.Context, runID string, result sessionDashboardResult) error {
	binding, _, err := c.dashboardCacheBinding(ctx, runID)
	if err != nil {
		return err
	}
	cache, err := sessionResultCacheNewDefault()
	if err != nil {
		return err
	}
	payload, err := sessionResultMarshal(result)
	if err != nil {
		return err
	}
	return cache.Put(binding, payload)
}
func sessionResultCommand() *cobra.Command {
	return &cobra.Command{Use: "result <run-id>", Short: "Read an authorized dashboard display cached on this device without rerunning its source", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cli, err := newSessionCLI(cmd)
		if err != nil {
			return err
		}
		binding, run, err := cli.dashboardCacheBinding(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		response := struct {
			Storage       string                  `json:"storage"`
			Availability  string                  `json:"availability"`
			OutcomeOrigin string                  `json:"outcomeOrigin"`
			Run           sessioncontext.Run      `json:"run"`
			Result        *sessionDashboardResult `json:"result,omitempty"`
		}{Storage: "local-device", Availability: "unavailable", OutcomeOrigin: "client-reported", Run: run}
		cache, cacheErr := sessionresultcache.NewDefault()
		if cacheErr == nil {
			payload, readErr := cache.Get(binding)
			var result sessionDashboardResult
			if readErr == nil && json.Unmarshal(payload, &result) == nil && result.FixtureSHA256 == sessionChinookHash && result.OutcomeOrigin == "client-reported" && result.Grid.Validate() == nil && result.Chart.Validate() == nil && (run.State == "accepted" || run.State == "completed" && reflect.DeepEqual(dashboardWidgetOutcomes(result), run.Widgets)) {
				response.Availability = "available"
				response.Result = &result
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(response)
	}}
}

var (
	sessionResultCacheNewDefault = sessionresultcache.NewDefault
	sessionResultMarshal         = json.Marshal
)
