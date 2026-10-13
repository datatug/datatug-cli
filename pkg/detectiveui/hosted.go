package detectiveui

import (
	"context"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/incidents"
)

// HostedService adds server-authorized evidence and cross-store workflows to
// the shared Checks/Case service. Agent-only sessions expose these explicitly
// as unavailable until their corresponding agent routes exist.
type CaseEventsService interface {
	CaseEvents(context.Context, string, int) (detective.CaseEventsPage, error)
}
type IncidentService interface {
	LinkedIncidents(context.Context, string) (IncidentLinks, error)
	IncidentAction(context.Context, IncidentAction) (IncidentResponse, error)
}
type KnowledgeService interface {
	KnowledgeProposals(context.Context, string) (KnowledgePage, error)
	KnowledgeAction(context.Context, KnowledgeAction) (KnowledgeResponse, error)
}
type QueryExecutionService interface {
	RunSavedQuery(context.Context, SavedQueryRun) (SavedQueryResult, error)
}
type AIService interface {
	AIProposals(context.Context, string) (AIPage, error)
	GenerateAI(context.Context, AIGenerate) (ai.Response, error)
	ReviewAI(context.Context, AIReview) (ai.Proposal, error)
	ConfirmAI(context.Context, ai.ConfirmRequest) (ai.Receipt, error)
}
type AIPage struct {
	Proposals  []ai.Proposal `json:"proposals"`
	NextCursor string        `json:"nextCursor,omitempty"`
	HasMore    bool          `json:"hasMore"`
}
type AIGenerate struct {
	Text           string
	CaseID         string
	ExpectedCounts map[string]int
}
type AIReview struct {
	Type    string
	Draft   *ai.ReviewedDraftRequest
	Context *ai.ContextReviewRequest
}
type HostedService interface {
	CaseEventsService
	IncidentService
	KnowledgeService
	QueryExecutionService
	AIService
}

type IncidentLinks struct {
	CaseID    string                   `json:"caseId"`
	Incidents []incidents.IncidentView `json:"incidents"`
}
type IncidentAction struct {
	Type, CaseID, IncidentRef, Environment, Title, Description string
	IfMatch                                                    int64
	Case                                                       *detective.Case
}
type IncidentResponse struct {
	Incident          incidents.IncidentView `json:"incident"`
	Case              *detective.Case        `json:"case,omitempty"`
	Receipt           string                 `json:"receipt"`
	CaseLinkStatus    string                 `json:"caseLinkStatus,omitempty"`
	LinkPendingReason string                 `json:"linkPendingReason,omitempty"`
}
type KnowledgeProposal struct {
	ID           string             `json:"id"`
	CaseID       string             `json:"caseId"`
	Kind         string             `json:"kind"`
	Title        string             `json:"title"`
	Statement    string             `json:"statement"`
	EvidenceRefs []string           `json:"evidenceRefs"`
	FindingRefs  []string           `json:"findingRefs,omitempty"`
	ClaimStatus  string             `json:"claimStatus"`
	Status       string             `json:"status"`
	Version      int64              `json:"version"`
	CreatedAt    time.Time          `json:"createdAt"`
	CreatedBy    string             `json:"createdBy"`
	ReviewedAt   time.Time          `json:"reviewedAt,omitempty"`
	ReviewedBy   string             `json:"reviewedBy,omitempty"`
	ReviewNote   string             `json:"reviewNote,omitempty"`
	Handoffs     []KnowledgeHandoff `json:"handoffs,omitempty"`
}
type KnowledgePage struct {
	Proposals  []KnowledgeProposal `json:"proposals"`
	NextCursor string              `json:"nextCursor,omitempty"`
	HasMore    bool                `json:"hasMore"`
}
type KnowledgeAction struct {
	Type, ProposalID, ReviewNote string
	IfMatch                      int64
	Proposal                     *KnowledgeProposal
	Target                       *KnowledgeTarget
}
type KnowledgeTarget struct {
	System           string `json:"system"`
	Repository       string `json:"repository"`
	ResourceID       string `json:"resourceId"`
	ExpectedRevision string `json:"expectedRevision"`
}
type KnowledgeHandoff struct {
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Target          KnowledgeTarget `json:"target"`
	ProposalID      string          `json:"proposalId"`
	ProposalVersion int64           `json:"proposalVersion"`
	CaseID          string          `json:"caseId"`
	ClaimStatus     string          `json:"claimStatus"`
	Statement       string          `json:"statement"`
	EvidenceRefs    []string        `json:"evidenceRefs"`
	FindingRefs     []string        `json:"findingRefs,omitempty"`
	PreparedAt      time.Time       `json:"preparedAt"`
	PreparedBy      string          `json:"preparedBy"`
	Receipt         string          `json:"receipt"`
}
type KnowledgeResponse struct {
	Proposal *KnowledgeProposal `json:"proposal,omitempty"`
	Export   *KnowledgeHandoff  `json:"export,omitempty"`
	Receipt  string             `json:"receipt"`
}
type SavedQueryRun struct {
	QueryID, Environment string
	Inputs               map[string]apicontract.TypedValueOrSet
}
type SavedQueryResult struct {
	Result            apicontract.Result `json:"result"`
	QueryRevision     string             `json:"queryRevision"`
	ResultFingerprint string             `json:"resultFingerprint"`
	Receipt           string             `json:"receipt"`
}
