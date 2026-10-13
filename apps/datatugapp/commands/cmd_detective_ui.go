package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	"github.com/spf13/cobra"
)

// These commands open native Bubble Tea workspaces. They use the same agent
// session and action endpoint as the web UI; they never execute Checks locally.
func detectiveUICommand(name string) *cobra.Command {
	cmd := &cobra.Command{Use: name, Short: map[string]string{"checks": "Open interactive Checks and Validators", "detective": "Open interactive Detective Cases and Scenarios"}[name], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		storage, _ := cmd.Flags().GetString("storage")
		project, _ := cmd.Flags().GetString("project")
		var service detectiveui.Service
		if storage == "cloud" {
			store, _ := cmd.Flags().GetString("store")
			if store != "firestore" {
				return fmt.Errorf("hosted Detective currently supports only the firestore store; selected %q", store)
			}
			space, _ := cmd.Flags().GetString("space")
			baseURL, _ := cmd.Flags().GetString("base-url")
			insecure, _ := cmd.Flags().GetBool("insecure-storage")
			cloud, err := newDetectiveCloudService(cmd.Context(), baseURL, space, project, insecure)
			if err != nil {
				return err
			}
			service = cloud
		} else if storage == "agent" {
			client, info, err := newAgentHTTPClient(cmd, "agent")
			if err != nil {
				return fmt.Errorf("Detective requires a running DataTug agent: %w", err)
			}
			if project == "" {
				if len(info.Projects) != 1 {
					return fmt.Errorf("--project is required when the agent serves %d projects", len(info.Projects))
				}
				project = info.Projects[0].ID
			}
			known := false
			for _, p := range info.Projects {
				if p.ID == project {
					known = true
				}
			}
			if !known {
				return fmt.Errorf("project %q is not served by the current agent", project)
			}
			if info.SecurityContextID == "" {
				return fmt.Errorf("agent-info did not provide a security context; restart or upgrade the agent")
			}
			service = detectiveUIService{client: client, project: project, securityContextID: info.SecurityContextID}
		} else {
			return fmt.Errorf("--storage must be agent or cloud")
		}
		model := detectiveui.New(service, name)
		_, err := runDetectiveUI(tea.NewProgram(model, tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout())))
		return err
	}}
	cmd.Flags().String("agent", "", "DataTug agent base URL (default configured server host/port)")
	cmd.Flags().StringP("project", "p", "", "Project ID (optional when the agent serves one project)")
	cmd.Flags().String("storage", "agent", "Detective service: agent or cloud")
	cmd.Flags().String("store", "firestore", "normalized hosted project store (cloud currently supports firestore)")
	cmd.Flags().String("space", "", "Space ID for a hosted cloud project")
	cmd.Flags().String("base-url", "", "hosted API /v0/ base URL (cloud; default DataTug API)")
	cmd.Flags().Bool("insecure-storage", false, "use the DataTug login stored with auth login --insecure-storage")
	return cmd
}

var runDetectiveUI = func(p *tea.Program) (tea.Model, error) { return p.Run() }

type detectiveUIService struct {
	client                     agentHTTPClient
	project, securityContextID string
}

func (s detectiveUIService) path() string {
	return "/datatug/detective/state?project=" + url.QueryEscape(s.project) + "&securityContextId=" + url.QueryEscape(s.securityContextID)
}
func (s detectiveUIService) State(ctx context.Context) (detective.State, error) {
	return s.StatePage(ctx, "")
}
func (s detectiveUIService) StatePage(ctx context.Context, cursor string) (detective.State, error) {
	path := s.path()
	if cursor != "" {
		path += "&runsBefore=" + url.QueryEscape(cursor)
	}
	raw, status, err := s.client.get(ctx, path)
	if err != nil {
		return detective.State{}, detectiveUIError(status, err)
	}
	var state detective.State
	if err = json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode Detective state: %w", err)
	}
	if !state.Capabilities.CanRead {
		return state, fmt.Errorf("current agent session has no Detective read capability")
	}
	return state, nil
}
func (s detectiveUIService) Action(ctx context.Context, req detective.ActionRequest) (detective.ActionResponse, error) {
	req.Project = s.project
	req.SecurityContextID = s.securityContextID
	raw, status, err := s.client.post(ctx, "/datatug/detective/action?project="+url.QueryEscape(s.project), req)
	if err != nil {
		return detective.ActionResponse{}, detectiveUIError(status, err)
	}
	var response detective.ActionResponse
	if err = json.Unmarshal(raw, &response); err != nil {
		return response, fmt.Errorf("decode Detective action response: %w", err)
	}
	return response, nil
}
func (s detectiveUIService) CaseEvents(ctx context.Context, caseID string, before int) (detective.CaseEventsPage, error) {
	query := url.Values{}
	query.Set("project", s.project)
	query.Set("securityContextId", s.securityContextID)
	query.Set("caseId", caseID)
	if before > 0 {
		query.Set("before", strconv.Itoa(before))
	}
	raw, status, err := s.client.get(ctx, "/datatug/detective/case-events?"+query.Encode())
	if err != nil {
		return detective.CaseEventsPage{}, detectiveUIError(status, err)
	}
	var page detective.CaseEventsPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("decode Case events: %w", err)
	}
	return page, nil
}
func (s detectiveUIService) RunSavedQuery(ctx context.Context, request detectiveui.SavedQueryRun) (detectiveui.SavedQueryResult, error) {
	origins := make([]apicontract.BindingOriginEntry, 0, len(request.Inputs))
	for name := range request.Inputs {
		origins = append(origins, apicontract.BindingOriginEntry{ParameterID: name, Origin: apicontract.BindingOriginManual})
	}
	req := apicontract.ExecutionRequest{Project: s.project, SecurityContextID: s.securityContextID, QueryID: request.QueryID, Environment: request.Environment, Parameters: request.Inputs, BindingOrigins: origins, Mode: apicontract.ProvenanceModeLive, Record: true, Snapshot: true}
	if err := req.Normalize(); err != nil {
		return detectiveui.SavedQueryResult{}, err
	}
	if err := req.Validate(); err != nil {
		return detectiveui.SavedQueryResult{}, err
	}
	raw, status, err := s.client.post(ctx, "/datatug/exec/run_query", req)
	if err != nil {
		return detectiveui.SavedQueryResult{}, detectiveUIError(status, err)
	}
	var result apicontract.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return detectiveui.SavedQueryResult{}, fmt.Errorf("decode saved-query result: %w", err)
	}
	if result.Execution == nil {
		return detectiveui.SavedQueryResult{}, fmt.Errorf("saved query did not return a retained execution receipt")
	}
	return detectiveui.SavedQueryResult{Result: result}, nil
}
func detectiveUIError(status int, err error) error {
	if status == httpStatusNotFound {
		return fmt.Errorf("agent lacks the Detective capability; update the agent and verify the project: %w", err)
	}
	if status == httpStatusForbidden {
		return fmt.Errorf("current agent session cannot access Detective project assets: %w", err)
	}
	if strings.Contains(err.Error(), "securityContext") {
		return fmt.Errorf("agent session changed; reopen Checks or Detective: %w", err)
	}
	return err
}

const (
	httpStatusNotFound  = 404
	httpStatusForbidden = 403
)
