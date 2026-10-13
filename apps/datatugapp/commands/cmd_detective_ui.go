package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/spf13/cobra"
)

// These commands open native Bubble Tea workspaces. They use the same agent
// session and action endpoint as the web UI; they never execute Checks locally.
func detectiveUICommand(name string) *cobra.Command {
	cmd := &cobra.Command{Use: name, Short: map[string]string{"checks": "Open interactive Checks and Validators", "detective": "Open interactive Detective Cases and Scenarios"}[name], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, info, err := newAgentHTTPClient(cmd, "agent")
		if err != nil {
			return fmt.Errorf("Detective requires a running DataTug agent: %w", err)
		}
		project, _ := cmd.Flags().GetString("project")
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
		service := detectiveUIService{client: client, project: project, securityContextID: info.SecurityContextID}
		model := detectiveui.New(service, name)
		_, err = runDetectiveUI(tea.NewProgram(model, tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout())))
		return err
	}}
	cmd.Flags().String("agent", "", "DataTug agent base URL (default configured server host/port)")
	cmd.Flags().StringP("project", "p", "", "Project ID (optional when the agent serves one project)")
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
