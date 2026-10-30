package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var detectiveJSONMarshal = json.Marshal

// These commands open native Bubble Tea workspaces. They use the same agent
// session and action endpoint as the web UI; they never execute Checks locally.
func detectiveUICommand(name string) *cobra.Command {
	cmd := &cobra.Command{Use: name, Short: map[string]string{"checks": "Open interactive Checks and Validators", "detective": "Open interactive Detective Cases and Scenarios"}[name], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		storage, _ := cmd.Flags().GetString("storage")
		project, _ := cmd.Flags().GetString("project")
		var service detectiveui.Service
		switch storage {
		case "cloud":
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
		case "agent":
			client, info, err := newAgentHTTPClient(cmd, "agent")
			if err != nil {
				return fmt.Errorf("detective requires a running DataTug agent: %w", err)
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
			service = detectiveUIService{client: client, project: project, securityContextID: info.SecurityContextID, pending: &detectiveOperationTracker{ids: map[string]string{}}}
		default:
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
	pending                    *detectiveOperationTracker
}

type detectiveOperationTracker struct {
	mu  sync.Mutex
	ids map[string]string
}

// The local action journal requires a slug beginning with a letter. A raw
// UUID can begin with a digit, which makes an otherwise valid retry fail.
func detectiveOperationID(rawUUID string) string { return "operation-" + rawUUID }

func (s detectiveUIService) operation(req detective.ActionRequest) (string, string) {
	req.SecurityContextID = ""
	return s.operationFor("detective:"+req.Type, req)
}

func (s detectiveUIService) operationFor(kind string, payload any) (string, string) {
	encoded, _ := json.Marshal(payload)
	encoded = append([]byte(kind+"\x00"+s.project+"\x00"), encoded...)
	sum := sha256.Sum256(encoded)
	key := hex.EncodeToString(sum[:])
	if s.pending == nil {
		return key, detectiveOperationID(uuid.NewString())
	}
	s.pending.mu.Lock()
	defer s.pending.mu.Unlock()
	if id := s.pending.ids[key]; id != "" {
		return key, id
	}
	id := detectiveOperationID(uuid.NewString())
	s.pending.ids[key] = id
	return key, id
}

func (s detectiveUIService) completeOperation(key string) {
	if s.pending == nil {
		return
	}
	s.pending.mu.Lock()
	delete(s.pending.ids, key)
	s.pending.mu.Unlock()
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
	response, _, err := s.ActionWithStatus(ctx, req)
	return response, err
}
func (s detectiveUIService) ActionWithStatus(ctx context.Context, req detective.ActionRequest) (detective.ActionResponse, string, error) {
	req.Project = s.project
	req.SecurityContextID = s.securityContextID
	key, operationID := s.operation(req)
	body := struct {
		OperationID string `json:"operationId"`
		detective.ActionRequest
	}{operationID, req}
	raw, status, err := s.client.post(ctx, "/datatug/detective/action?project="+url.QueryEscape(s.project), body)
	if err != nil {
		return detective.ActionResponse{}, "", detectiveUIError(status, err)
	}
	var response struct {
		detective.ActionResponse
		CaseLinkStatus string `json:"caseLinkStatus,omitempty"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return response.ActionResponse, "", fmt.Errorf("decode Detective action response: %w", err)
	}
	s.completeOperation(key)
	return response.ActionResponse, response.CaseLinkStatus, nil
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
func (s detectiveUIService) LinkedIncidents(ctx context.Context, caseID string) (detectiveui.IncidentLinks, error) {
	query := url.Values{}
	query.Set("project", s.project)
	query.Set("securityContextId", s.securityContextID)
	query.Set("caseId", caseID)
	raw, status, err := s.client.get(ctx, "/datatug/detective/incidents?"+query.Encode())
	if err != nil {
		return detectiveui.IncidentLinks{}, detectiveUIError(status, err)
	}
	var links detectiveui.IncidentLinks
	if err := json.Unmarshal(raw, &links); err != nil {
		return links, fmt.Errorf("decode linked incidents: %w", err)
	}
	return links, nil
}
func (s detectiveUIService) IncidentAction(ctx context.Context, action detectiveui.IncidentAction) (detectiveui.IncidentResponse, error) {
	request := struct {
		Project           string          `json:"project"`
		SecurityContextID string          `json:"securityContextId"`
		OperationID       string          `json:"operationId"`
		Type              string          `json:"type"`
		CaseID            string          `json:"caseId,omitempty"`
		IncidentRef       string          `json:"incidentRef,omitempty"`
		Environment       string          `json:"environment"`
		Title             string          `json:"title,omitempty"`
		Description       string          `json:"description,omitempty"`
		IfMatch           int64           `json:"ifMatch,omitempty"`
		Case              *detective.Case `json:"case,omitempty"`
	}{Project: s.project, SecurityContextID: s.securityContextID, Type: action.Type, CaseID: action.CaseID, IncidentRef: action.IncidentRef, Environment: action.Environment, Title: action.Title, Description: action.Description, IfMatch: action.IfMatch, Case: action.Case}
	// The same request must keep its mutation identity after an uncertain HTTP
	// response. The server additionally binds that identity to actor and scope.
	operationPayload := request
	operationPayload.SecurityContextID = ""
	encoded, err := detectiveJSONMarshal(operationPayload)
	if err != nil {
		return detectiveui.IncidentResponse{}, err
	}
	sum := sha256.Sum256(encoded)
	request.OperationID = "operation-" + hex.EncodeToString(sum[:])
	raw, status, err := s.client.post(ctx, "/datatug/detective/incident-action", request)
	if err != nil {
		return detectiveui.IncidentResponse{}, detectiveUIError(status, err)
	}
	var response detectiveui.IncidentResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, fmt.Errorf("decode Incidentius action: %w", err)
	}
	return response, nil
}
func (s detectiveUIService) KnowledgeProposals(ctx context.Context, after string) (detectiveui.KnowledgePage, error) {
	query := url.Values{}
	query.Set("project", s.project)
	query.Set("securityContextId", s.securityContextID)
	if after != "" {
		query.Set("after", after)
	}
	raw, status, err := s.client.get(ctx, "/datatug/detective/knowledge_proposals?"+query.Encode())
	if err != nil {
		return detectiveui.KnowledgePage{}, detectiveUIError(status, err)
	}
	var page detectiveui.KnowledgePage
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("decode knowledge proposals: %w", err)
	}
	return page, nil
}
func (s detectiveUIService) KnowledgeAction(ctx context.Context, action detectiveui.KnowledgeAction) (detectiveui.KnowledgeResponse, error) {
	request := struct {
		Storage     string                         `json:"storage"`
		Project     string                         `json:"project"`
		OperationID string                         `json:"operationId"`
		Type        string                         `json:"type"`
		IfMatch     int64                          `json:"ifMatch,omitempty"`
		Proposal    *detectiveui.KnowledgeProposal `json:"proposal,omitempty"`
		ProposalID  string                         `json:"proposalId,omitempty"`
		ReviewNote  string                         `json:"reviewNote,omitempty"`
		Target      *detectiveui.KnowledgeTarget   `json:"target,omitempty"`
	}{Storage: "local", Project: s.project, Type: action.Type, IfMatch: action.IfMatch, Proposal: action.Proposal, ProposalID: action.ProposalID, ReviewNote: action.ReviewNote, Target: action.Target}
	encoded, err := detectiveJSONMarshal(request)
	if err != nil {
		return detectiveui.KnowledgeResponse{}, err
	}
	sum := sha256.Sum256(encoded)
	request.OperationID = "operation-" + hex.EncodeToString(sum[:])
	query := url.Values{}
	query.Set("project", s.project)
	query.Set("securityContextId", s.securityContextID)
	raw, status, err := s.client.post(ctx, "/datatug/detective/knowledge_proposal_action?"+query.Encode(), request)
	if err != nil {
		return detectiveui.KnowledgeResponse{}, detectiveUIError(status, err)
	}
	var response detectiveui.KnowledgeResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, fmt.Errorf("decode knowledge action: %w", err)
	}
	return response, nil
}
func (s detectiveUIService) AIProposals(ctx context.Context, after string) (detectiveui.AIPage, error) {
	query := url.Values{}
	query.Set("project", s.project)
	query.Set("securityContextId", s.securityContextID)
	if after != "" {
		query.Set("after", after)
	}
	raw, status, err := s.client.get(ctx, "/datatug/detective/ai_proposals?"+query.Encode())
	if err != nil {
		return detectiveui.AIPage{}, detectiveUIError(status, err)
	}
	var page detectiveui.AIPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("decode AI proposals: %w", err)
	}
	return page, nil
}
func (s detectiveUIService) GenerateAI(ctx context.Context, request detectiveui.AIGenerate) (ai.Response, error) {
	intent, err := detectiveJSONMarshal(request)
	if err != nil {
		return ai.Response{}, err
	}
	key, operationID := s.operationFor("ai-generate", intent)
	body := struct {
		Project           string         `json:"project"`
		SecurityContextID string         `json:"securityContextId"`
		OperationID       string         `json:"operationId"`
		Text              string         `json:"text"`
		CaseID            string         `json:"caseId,omitempty"`
		ExpectedCounts    map[string]int `json:"expectedCounts,omitempty"`
	}{s.project, s.securityContextID, operationID, request.Text, request.CaseID, request.ExpectedCounts}
	raw, status, err := s.client.post(ctx, "/datatug/detective/ai_proposals?project="+url.QueryEscape(s.project), body)
	if err != nil {
		return ai.Response{}, detectiveUIError(status, err)
	}
	var response ai.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		return response, fmt.Errorf("decode AI response: %w", err)
	}
	s.completeOperation(key)
	return response, nil
}
func (s detectiveUIService) ReviewAI(ctx context.Context, review detectiveui.AIReview) (ai.Proposal, error) {
	body := struct {
		Project           string                   `json:"project"`
		SecurityContextID string                   `json:"securityContextId"`
		Type              string                   `json:"type"`
		Draft             *ai.ReviewedDraftRequest `json:"draft,omitempty"`
		Context           *ai.ContextReviewRequest `json:"context,omitempty"`
	}{s.project, s.securityContextID, review.Type, review.Draft, review.Context}
	raw, status, err := s.client.post(ctx, "/datatug/detective/ai_review?project="+url.QueryEscape(s.project), body)
	if err != nil {
		return ai.Proposal{}, detectiveUIError(status, err)
	}
	var proposal ai.Proposal
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return proposal, fmt.Errorf("decode reviewed AI proposal: %w", err)
	}
	return proposal, nil
}
func (s detectiveUIService) ConfirmAI(ctx context.Context, confirm ai.ConfirmRequest) (ai.Receipt, error) {
	if confirm.OperationID == "" {
		sum := sha256.Sum256([]byte(s.project + "\x00" + confirm.ProposalID + "\x00" + confirm.Digest))
		confirm.OperationID = "operation-" + hex.EncodeToString(sum[:])
	}
	body := struct {
		Project           string `json:"project"`
		SecurityContextID string `json:"securityContextId"`
		ai.ConfirmRequest
	}{s.project, s.securityContextID, confirm}
	raw, status, err := s.client.post(ctx, "/datatug/detective/ai_confirm?project="+url.QueryEscape(s.project), body)
	if err != nil {
		return ai.Receipt{}, detectiveUIError(status, err)
	}
	var receipt ai.Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return receipt, fmt.Errorf("decode AI confirmation receipt: %w", err)
	}
	return receipt, nil
}
func (s detectiveUIService) RunSavedQuery(ctx context.Context, request detectiveui.SavedQueryRun) (detectiveui.SavedQueryResult, error) {
	origins := make([]apicontract.BindingOriginEntry, 0, len(request.Inputs))
	for name := range request.Inputs {
		origins = append(origins, apicontract.BindingOriginEntry{ParameterID: name, Origin: apicontract.BindingOriginManual})
	}
	req := apicontract.ExecutionRequest{Project: s.project, SecurityContextID: s.securityContextID, QueryID: request.QueryID, Environment: request.Environment, Parameters: request.Inputs, BindingOrigins: origins, Mode: apicontract.ProvenanceModeLive, Record: true}
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
