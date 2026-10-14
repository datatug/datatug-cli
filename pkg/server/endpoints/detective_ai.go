package endpoints

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/detective"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	llm "github.com/strongo/aichat/ai"
)

var newDetectiveAIProvider = func(model, baseURL, key string) (llm.LLMProvider, error) {
	return chat.NewLLMProvider(model, baseURL, key)
}

type aiGenerateRequest struct {
	Project           string         `json:"project"`
	SecurityContextID string         `json:"securityContextId"`
	OperationID       string         `json:"operationId"`
	Text              string         `json:"text"`
	CaseID            string         `json:"caseId,omitempty"`
	ExpectedCounts    map[string]int `json:"expectedCounts,omitempty"`
}

type aiReviewRequest struct {
	Project           string                   `json:"project"`
	SecurityContextID string                   `json:"securityContextId"`
	Type              string                   `json:"type"`
	Draft             *ai.ReviewedDraftRequest `json:"draft,omitempty"`
	Context           *ai.ContextReviewRequest `json:"context,omitempty"`
}

type aiConfirmRequest struct {
	Project           string `json:"project"`
	SecurityContextID string `json:"securityContextId"`
	ai.ConfirmRequest
}

func localAIScope(project, actor string) ai.Scope {
	return ai.Scope{Key: "local:" + project, Project: project, Actor: actor}
}

func detectiveAIProposalsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	if r.Method == http.MethodGet {
		store, actor, err := detectiveStore(query.Get("project"), query.Get("securityContextId"))
		if err != nil {
			writeContractError(w, r, err)
			return
		}
		page, err := (detective.LocalAIStore{Store: store, Scope: localAIScope(query.Get("project"), actor)}).Page(r.Context(), query.Get("after"))
		writeContractResponse(w, r, detectiveError(err), page)
		return
	}
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var request aiGenerateRequest
	if err := readIncidentBody(w, r, &request); err != nil {
		writeContractError(w, r, err)
		return
	}
	if request.Project != query.Get("project") || !core.ValidSlug(request.OperationID) || strings.TrimSpace(request.Text) == "" || len(request.Text) > 16*1024 || len(request.ExpectedCounts) > 64 {
		writeContractError(w, r, newInvalidRequest("body", "invalid AI generation request"))
		return
	}
	for name, count := range request.ExpectedCounts {
		if !core.ValidParameterName(name) || count < 1 || count > 100 {
			writeContractError(w, r, newInvalidRequest("expectedCounts", "invalid expected count"))
			return
		}
	}
	store, actor, err := detectiveStore(request.Project, request.SecurityContextID)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	if request.CaseID != "" {
		if _, err := store.GetCase(r.Context(), request.CaseID); err != nil {
			writeContractError(w, r, detectiveError(err))
			return
		}
	}
	scope := localAIScope(request.Project, actor)
	local := detective.LocalAIStore{Store: store, Scope: scope}
	binding, err := core.BindOperation(scope.Key, actor, "ai-generate", request.OperationID, struct {
		Text           string
		CaseID         string
		ExpectedCounts map[string]int
	}{request.Text, request.CaseID, request.ExpectedCounts})
	if err != nil {
		writeContractError(w, r, detectiveError(err))
		return
	}
	profile := api.GetCapabilities().DetectiveAIProfile
	if profile == nil {
		writeContractError(w, r, newInvalidRequest("ai", "Detective AI profile is not configured on this agent"))
		return
	}
	key := ""
	if profile.APIKeyEnv != "" {
		key = os.Getenv(profile.APIKeyEnv)
		if key == "" {
			writeContractError(w, r, newInvalidRequest("ai", "Detective AI profile credential is unavailable"))
			return
		}
	}
	provider, err := newDetectiveAIProvider(profile.Model, profile.BaseURL, key)
	if err != nil {
		writeContractError(w, r, newInvalidRequest("ai", "Detective AI provider is unavailable"))
		return
	}
	snapshot, err := detectiveAISnapshot(r.Context(), request.Project, store)
	if err != nil {
		writeContractError(w, r, detectiveError(err))
		return
	}
	source, err := local.SavePaste(r.Context(), request.Text)
	if err != nil {
		writeContractError(w, r, detectiveError(err))
		return
	}
	response, replay, err := local.BeginAIGeneration(r.Context(), request.OperationID, binding)
	if err != nil || replay {
		writeContractResponse(w, r, detectiveError(err), response)
		return
	}
	assistant := ai.Assistant{Provider: provider, Store: local}
	response, err = assistant.Run(r.Context(), ai.Request{Scope: scope, Snapshot: snapshot, Text: request.Text, Sources: []ai.Source{source}, ExpectedCounts: request.ExpectedCounts})
	if err == nil {
		// Model prose may echo the entire paste. Only typed private proposals
		// and their bounded citations survive the generation journal.
		response.Text = ""
		err = local.CompleteAIGeneration(r.Context(), request.OperationID, binding, response)
	}
	writeContractResponse(w, r, aiContractError(err), response)
}

type localAIPorts struct {
	local              detective.LocalAIStore
	project, contextID string
}

func newLocalAIPorts(store *detective.LocalStore, scope ai.Scope, project, contextID string) localAIPorts {
	p := localAIPorts{local: detective.LocalAIStore{Store: store, Scope: scope}, project: project, contextID: contextID}
	p.local.BasisVerifier = p.verifyBasis
	return p
}

func (p localAIPorts) verifyBasis(ctx context.Context, basis ai.Snapshot, state detective.State) error {
	actor := api.SecurePrincipalID()
	if actor == "" {
		actor = "local-owner"
	}
	_, projectExists := api.ProjectDir(p.project)
	if !api.ValidateSecurityContext(p.contextID) || !api.SecureSessionUnrestricted() || actor != p.local.Scope.Actor || !projectExists || !api.GetCapabilities().AllowWrites {
		return ai.ErrUnauthorized
	}
	current, err := detectiveAISnapshotFromState(ctx, p.project, state)
	if err != nil {
		return err
	}
	if current.CatalogRevision != basis.CatalogRevision {
		return ai.ErrStale
	}
	return nil
}

func (p localAIPorts) Save(ctx context.Context, proposal ai.Proposal) error {
	return p.local.Save(ctx, proposal)
}
func (p localAIPorts) Get(ctx context.Context, id string) (ai.Proposal, error) {
	return p.local.Get(ctx, id)
}
func (p localAIPorts) AuthorizeSources(ctx context.Context, scope ai.Scope, sources []ai.SourceEvidence) error {
	if _, _, err := detectiveStore(p.project, p.contextID); err != nil {
		return err
	}
	return p.local.AuthorizeSources(ctx, scope, sources)
}
func (p localAIPorts) Current(ctx context.Context, scope ai.Scope) (ai.Snapshot, error) {
	if scope != p.local.Scope {
		return ai.Snapshot{}, ai.ErrUnauthorized
	}
	store, actor, err := detectiveStore(p.project, p.contextID)
	if err != nil || actor != scope.Actor {
		return ai.Snapshot{}, ai.ErrUnauthorized
	}
	return detectiveAISnapshot(ctx, p.project, store)
}
func (p localAIPorts) Authorize(_ context.Context, scope ai.Scope, proposal ai.Proposal) error {
	if scope != p.local.Scope || proposal.Scope != scope || !api.GetCapabilities().AllowWrites {
		return ai.ErrUnauthorized
	}
	return nil
}
func (p localAIPorts) PriorReceipt(ctx context.Context, scope ai.Scope, operationID string) (ai.Receipt, bool, error) {
	if scope != p.local.Scope {
		return ai.Receipt{}, false, ai.ErrUnauthorized
	}
	return p.local.AIReceipt(ctx, operationID)
}
func (p localAIPorts) Dispatch(ctx context.Context, command ai.Command) (ai.Receipt, error) {
	if command.Proposal.Draft.Kind == ai.KindCase {
		return p.local.ConfirmCase(ctx, command)
	}
	if command.Proposal.Draft.Kind == ai.KindCheck || command.Proposal.Draft.Kind == ai.KindValidator || command.Proposal.Draft.Kind == ai.KindScenario {
		return p.local.ConfirmCatalog(ctx, command)
	}
	if command.Proposal.Draft.Kind == ai.KindQuery {
		return p.local.ConfirmQuery(ctx, command)
	}
	if command.Proposal.Draft.Kind == ai.KindRun {
		return p.local.ConfirmRun(ctx, command, detectiveQueryRunner(p.project, p.contextID), detectiveExecutionMetadata(p.project), detectiveQueryRevisionLookup(p.project))
	}
	return ai.Receipt{}, ai.ErrUnsupported
}

func detectiveAIReviewHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var request aiReviewRequest
	if err := readIncidentBody(w, r, &request); err != nil {
		writeContractError(w, r, err)
		return
	}
	if request.Project != r.URL.Query().Get("project") || request.Type == "draft" && (request.Draft == nil || request.Context != nil) || request.Type == "context" && (request.Context == nil || request.Draft != nil) || request.Type != "draft" && request.Type != "context" {
		writeContractError(w, r, newInvalidRequest("body", "exactly one AI review is required"))
		return
	}
	store, actor, err := detectiveStore(request.Project, request.SecurityContextID)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	scope := localAIScope(request.Project, actor)
	ports := newLocalAIPorts(store, scope, request.Project, request.SecurityContextID)
	var proposal ai.Proposal
	if request.Draft != nil {
		proposal, err = ai.PrepareReviewedDraft(r.Context(), scope, *request.Draft, ports, time.Now().UTC(), nil)
	} else {
		proposal, err = ai.PrepareContextCaseEdit(r.Context(), scope, *request.Context, ports, time.Now().UTC(), nil)
	}
	writeContractResponse(w, r, aiContractError(err), proposal)
}

func detectiveAIConfirmHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !api.GetCapabilities().AllowWrites {
		writeContractError(w, r, newAccessDenied("this agent was started without --allow-writes"))
		return
	}
	var request aiConfirmRequest
	if err := readIncidentBody(w, r, &request); err != nil {
		writeContractError(w, r, err)
		return
	}
	if request.Project != r.URL.Query().Get("project") {
		writeContractError(w, r, newInvalidRequest("project", "URL and body project differ"))
		return
	}
	store, actor, err := detectiveStore(request.Project, request.SecurityContextID)
	if err != nil {
		writeContractError(w, r, err)
		return
	}
	scope := localAIScope(request.Project, actor)
	ports := newLocalAIPorts(store, scope, request.Project, request.SecurityContextID)
	receipt, err := ai.Confirm(r.Context(), scope, request.ConfirmRequest, ports, time.Now().UTC())
	writeContractResponse(w, r, aiContractError(err), receipt)
}

func aiContractError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ai.ErrUnauthorized):
		return newAccessDenied("Detective AI action is not authorized")
	case errors.Is(err, ai.ErrStale):
		return detectiveError(detective.ErrConflict)
	case errors.Is(err, ai.ErrInvalid):
		return newInvalidRequest("body", "invalid Detective AI proposal or source")
	case errors.Is(err, ai.ErrUnsupported):
		return newInvalidRequest("body", "this Detective AI action is unsupported")
	default:
		return detectiveError(err)
	}
}
