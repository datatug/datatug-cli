package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/datatug/datatug-cli/pkg/detectiveui"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/google/uuid"
)

// detectiveCloudService pins every request to one verified hosted project.
// The server authenticates the bearer and independently authorizes the Space,
// project, source, environment and linked resource for each operation.
type detectiveCloudService struct {
	session           cloudSession
	space, project    string
	mu                sync.Mutex
	cases             map[string]int64
	checks            map[string]int64
	validators        map[string]int64
	queryValidators   map[string]int64
	scenarios         map[string]int64
	pendingOperations map[string]string
}

// An uncertain response retains its operation ID. Repeating the same scoped
// payload then asks the server for the same durable receipt instead of
// creating a second Case, run, Incident or proposal.
func (s *detectiveCloudService) operation(kind string, payload any) (string, string) {
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(append([]byte(kind+"\x00"+s.space+"\x00"+s.project+"\x00"), encoded...))
	key := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingOperations == nil {
		s.pendingOperations = make(map[string]string)
	}
	if id := s.pendingOperations[key]; id != "" {
		return key, id
	}
	id := uuid.NewString()
	s.pendingOperations[key] = id
	return key, id
}
func (s *detectiveCloudService) completeOperation(key string) {
	s.mu.Lock()
	delete(s.pendingOperations, key)
	s.mu.Unlock()
}

func newDetectiveCloudService(ctx context.Context, baseURL, space, project string, insecure bool) (*detectiveCloudService, error) {
	if strings.TrimSpace(space) == "" || strings.TrimSpace(project) == "" || strings.ContainsAny(space+project, " /?#") {
		return nil, fmt.Errorf("cloud Detective requires explicit --space and --project identifiers")
	}
	session, err := loadCloudSession(ctx, chatOptions{baseURL: baseURL, insecureStorage: insecure})
	if err != nil {
		return nil, err
	}
	return &detectiveCloudService{session: session, space: space, project: project}, nil
}

func (s *detectiveCloudService) scope() url.Values {
	v := url.Values{}
	v.Set("storage", "firestore")
	v.Set("spaceID", s.space)
	v.Set("project", s.project)
	return v
}
func (s *detectiveCloudService) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.session.baseURL+strings.TrimPrefix(path, "/v0/"), payload)
	if err != nil {
		return err
	}
	token, err := s.session.token(ctx)
	if err != nil {
		return fmt.Errorf("DataTug login: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.session.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hosted Detective request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(raw, &failure)
		code := failure.Code
		if code == "" {
			code = failure.Error
		}
		if code == "" {
			code = http.StatusText(res.StatusCode)
		}
		return fmt.Errorf("hosted Detective %s (%d): %s", http.StatusText(res.StatusCode), res.StatusCode, code)
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("invalid hosted Detective response")
	}
	return nil
}
func (s *detectiveCloudService) State(ctx context.Context) (detective.State, error) {
	return s.StatePage(ctx, "")
}
func (s *detectiveCloudService) StatePage(ctx context.Context, cursor string) (detective.State, error) {
	v := s.scope()
	if cursor != "" {
		v.Set("runsBefore", cursor)
	}
	var state detective.State
	err := s.do(ctx, http.MethodGet, "/v0/datatug/detective/state?"+v.Encode(), nil, &state)
	if err != nil {
		return state, err
	}
	if !state.Capabilities.CanRead {
		return state, fmt.Errorf("hosted project has no Detective read capability")
	}
	if cursor == "" {
		s.capture(state)
	}
	return state, nil
}
func (s *detectiveCloudService) capture(state detective.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cases = make(map[string]int64, len(state.Cases))
	for _, x := range state.Cases {
		s.cases[x.ID] = x.Version
	}
	s.checks = make(map[string]int64, len(state.Checks))
	for _, x := range state.Checks {
		s.checks[x.ID] = x.Version
	}
	s.validators = make(map[string]int64, len(state.Validators))
	for _, x := range state.Validators {
		s.validators[x.ID] = x.Version
	}
	s.queryValidators = make(map[string]int64, len(state.QueryValidators))
	for _, x := range state.QueryValidators {
		s.queryValidators[x.QueryID] = x.Version
	}
	s.scenarios = make(map[string]int64, len(state.Scenarios))
	for _, x := range state.Scenarios {
		s.scenarios[x.ID] = x.Version
	}
}
func (s *detectiveCloudService) ifMatch(req detective.ActionRequest) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch req.Type {
	case "save-check":
		if req.Check != nil {
			return s.checks[req.Check.ID]
		}
	case "save-validator":
		if req.Validator != nil {
			return s.validators[req.Validator.ID]
		}
	case "save-query-validators":
		if req.QueryValidators != nil {
			return s.queryValidators[req.QueryValidators.QueryID]
		}
	case "save-scenario":
		if req.Scenario != nil {
			return s.scenarios[req.Scenario.ID]
		}
	case "update-case":
		if req.Case != nil {
			return s.cases[req.Case.ID]
		}
	case "append-event", "apply-scenario", "record-scenario-step":
		return s.cases[req.CaseID]
	}
	return 0
}
func (s *detectiveCloudService) Action(ctx context.Context, req detective.ActionRequest) (detective.ActionResponse, error) {
	response, _, err := s.ActionWithStatus(ctx, req)
	return response, err
}
func (s *detectiveCloudService) ActionWithStatus(ctx context.Context, req detective.ActionRequest) (detective.ActionResponse, string, error) {
	req.Project = s.project
	req.SecurityContextID = ""
	match := s.ifMatch(req)
	key, operationID := s.operation("detective:"+req.Type, struct {
		IfMatch int64
		Request detective.ActionRequest
	}{match, req})
	body := struct {
		Storage     string `json:"storage"`
		SpaceID     string `json:"spaceID"`
		OperationID string `json:"operationId"`
		IfMatch     int64  `json:"ifMatch,omitempty"`
		detective.ActionRequest
	}{"firestore", s.space, operationID, match, req}
	var result struct {
		detective.ActionResponse
		Receipt        string `json:"receipt"`
		CaseLinkStatus string `json:"caseLinkStatus,omitempty"`
	}
	err := s.do(ctx, http.MethodPost, "/v0/datatug/detective/action", body, &result)
	if err == nil && (result.Run == nil || result.Run.ExecutionStatus != "pending") {
		s.completeOperation(key)
	}
	return result.ActionResponse, result.CaseLinkStatus, err
}
func (s *detectiveCloudService) CaseEvents(ctx context.Context, id string, before int) (detective.CaseEventsPage, error) {
	v := s.scope()
	v.Set("caseId", id)
	if before > 0 {
		v.Set("before", strconv.Itoa(before))
	}
	var page detective.CaseEventsPage
	err := s.do(ctx, http.MethodGet, "/v0/datatug/detective/case_events?"+v.Encode(), nil, &page)
	return page, err
}
func (s *detectiveCloudService) LinkedIncidents(ctx context.Context, id string) (detectiveui.IncidentLinks, error) {
	v := s.scope()
	v.Set("caseId", id)
	var page detectiveui.IncidentLinks
	err := s.do(ctx, http.MethodGet, "/v0/datatug/detective/incidents?"+v.Encode(), nil, &page)
	return page, err
}
func (s *detectiveCloudService) IncidentAction(ctx context.Context, a detectiveui.IncidentAction) (detectiveui.IncidentResponse, error) {
	var out detectiveui.IncidentResponse
	body := map[string]any{"storage": "firestore", "spaceID": s.space, "project": s.project, "type": a.Type, "environment": a.Environment, "ifMatch": a.IfMatch}
	if a.CaseID != "" {
		body["caseId"] = a.CaseID
	}
	if a.IncidentRef != "" {
		body["incidentRef"] = a.IncidentRef
	}
	if a.Title != "" {
		body["title"] = a.Title
	}
	if a.Description != "" {
		body["description"] = a.Description
	}
	if a.Case != nil {
		body["case"] = a.Case
	}
	key, operationID := s.operation("incident:"+a.Type, body)
	body["operationId"] = operationID
	err := s.do(ctx, http.MethodPost, "/v0/datatug/detective/incident_action", body, &out)
	if err == nil {
		s.completeOperation(key)
	}
	return out, err
}
func (s *detectiveCloudService) KnowledgeProposals(ctx context.Context, after string) (detectiveui.KnowledgePage, error) {
	v := s.scope()
	if after != "" {
		v.Set("after", after)
	}
	var page detectiveui.KnowledgePage
	err := s.do(ctx, http.MethodGet, "/v0/datatug/detective/knowledge_proposals?"+v.Encode(), nil, &page)
	return page, err
}
func (s *detectiveCloudService) KnowledgeAction(ctx context.Context, a detectiveui.KnowledgeAction) (detectiveui.KnowledgeResponse, error) {
	var out detectiveui.KnowledgeResponse
	body := map[string]any{"storage": "firestore", "spaceID": s.space, "project": s.project, "type": a.Type, "ifMatch": a.IfMatch}
	if a.Proposal != nil {
		body["proposal"] = a.Proposal
	}
	if a.ProposalID != "" {
		body["proposalId"] = a.ProposalID
	}
	if a.ReviewNote != "" {
		body["reviewNote"] = a.ReviewNote
	}
	if a.Target != nil {
		body["target"] = a.Target
	}
	key, operationID := s.operation("knowledge:"+a.Type, body)
	body["operationId"] = operationID
	err := s.do(ctx, http.MethodPost, "/v0/datatug/detective/knowledge_proposal_action", body, &out)
	if err == nil {
		s.completeOperation(key)
	}
	return out, err
}
func (s *detectiveCloudService) AIProposals(ctx context.Context, after string) (detectiveui.AIPage, error) {
	v := s.scope()
	if after != "" {
		v.Set("after", after)
	}
	var page detectiveui.AIPage
	err := s.do(ctx, http.MethodGet, "/v0/datatug/detective/ai_proposals?"+v.Encode(), nil, &page)
	return page, err
}
func (s *detectiveCloudService) GenerateAI(ctx context.Context, a detectiveui.AIGenerate) (ai.Response, error) {
	var out ai.Response
	body := map[string]any{"storage": "firestore", "spaceID": s.space, "project": s.project, "text": a.Text}
	if a.CaseID != "" {
		body["caseId"] = a.CaseID
	}
	if a.ExpectedCounts != nil {
		body["expectedCounts"] = a.ExpectedCounts
	}
	key, operationID := s.operation("ai-generate", body)
	body["operationId"] = operationID
	err := s.do(ctx, http.MethodPost, "/v0/datatug/detective/ai_proposals", body, &out)
	if err == nil {
		s.completeOperation(key)
	}
	return out, err
}
func (s *detectiveCloudService) ReviewAI(ctx context.Context, a detectiveui.AIReview) (ai.Proposal, error) {
	var out ai.Proposal
	body := map[string]any{"storage": "firestore", "spaceID": s.space, "project": s.project, "type": a.Type}
	if a.Draft != nil {
		body["draft"] = a.Draft
	}
	if a.Context != nil {
		body["context"] = a.Context
	}
	err := s.do(ctx, http.MethodPost, "/v0/datatug/detective/ai_review", body, &out)
	return out, err
}
func (s *detectiveCloudService) ConfirmAI(ctx context.Context, a ai.ConfirmRequest) (ai.Receipt, error) {
	var out ai.Receipt
	body := map[string]any{"storage": "firestore", "spaceID": s.space, "project": s.project, "proposalId": a.ProposalID, "digest": a.Digest}
	encoded, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(encoded)
	operationID := hex.EncodeToString(sum[:])
	if a.OperationID != "" {
		operationID = a.OperationID
	}
	body["operationId"] = operationID
	err = s.do(ctx, http.MethodPost, "/v0/datatug/detective/ai_confirm", body, &out)
	return out, err
}
func (s *detectiveCloudService) RunSavedQuery(ctx context.Context, a detectiveui.SavedQueryRun) (detectiveui.SavedQueryResult, error) {
	var out detectiveui.SavedQueryResult
	body := map[string]any{"storage": "firestore", "spaceID": s.space, "project": s.project, "queryId": a.QueryID, "environment": a.Environment, "inputs": a.Inputs}
	key, operationID := s.operation("run-query", body)
	body["operationId"] = operationID
	err := s.do(ctx, http.MethodPost, "/v0/datatug/queries/run_query", body, &out)
	if err == nil {
		s.completeOperation(key)
	}
	return out, err
}
