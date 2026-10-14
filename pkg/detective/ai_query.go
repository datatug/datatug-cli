package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
	"github.com/datatug/datatug-core/pkg/dto"
)

// The existing revisioned query adapter owns the durable query pair and its
// receipt. This small outer journal adds the AI proposal receipt and audit;
// retrying an accepted operation replays the same immutable inner request.
type aiQueryOperation struct {
	Kind    string               `json:"kind"`
	Receipt ai.Receipt           `json:"receipt"`
	Request dto.SaveQueryRequest `json:"request"`
	Done    bool                 `json:"done"`
}

func (s LocalAIStore) ConfirmQuery(ctx context.Context, command ai.Command) (ai.Receipt, error) {
	if s.Store == nil || command.Scope != s.Scope || command.Proposal.Scope != s.Scope || command.Proposal.Draft.Kind != ai.KindQuery || command.Proposal.Draft.Query == nil || !validSlug(command.OperationID) || command.Binding.Project != s.Scope.Key || command.Binding.Actor != s.Scope.Actor || command.Binding.Action != "ai-confirm-draft-query" || command.Binding.ResourceID != command.Proposal.ID || len(command.Binding.PayloadDigest) != 64 {
		return ai.Receipt{}, ErrInvalid
	}
	var receipt ai.Receipt
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID)
		if err == nil {
			var prior aiQueryOperation
			if json.Unmarshal(data, &prior) != nil || prior.Kind != "query" || !aiReceiptMatchesCommand(prior.Receipt, command) {
				return ErrConflict
			}
			if !prior.Done {
				if err := s.finishAIQuery(ctx, command.OperationID, &prior); err != nil {
					return err
				}
			}
			receipt = prior.Receipt
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.verifyAIBasisLocked(ctx, command.Proposal); err != nil {
			return err
		}
		request, err := s.aiQueryRequest(ctx, command)
		if err != nil {
			return err
		}
		at := time.Now().UTC()
		receipt = ai.Receipt{OperationID: command.OperationID, ScopeKey: s.Scope.Key, Project: s.Scope.Project, Actor: s.Scope.Actor, ProposalID: command.Proposal.ID, PayloadDigest: command.Binding.PayloadDigest, ResourceID: command.Proposal.Draft.Query.ID, At: at}
		op := aiQueryOperation{Kind: "query", Receipt: receipt, Request: request}
		if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID, op); err != nil {
			return err
		}
		return s.finishAIQuery(ctx, command.OperationID, &op)
	})
	return receipt, err
}

func (s LocalAIStore) aiQueryRequest(ctx context.Context, command ai.Command) (dto.SaveQueryRequest, error) {
	draft := command.Proposal.Draft.Query
	if draft == nil || !core.ValidQueryRef(draft.ID) || draft.Title == "" || draft.Text == "" || len(draft.Inputs) > 64 {
		return dto.SaveQueryRequest{}, ErrInvalid
	}
	ref := dto.ProjectRef{StoreID: api.LocalStoreID, ProjectID: s.Scope.Project}
	branches, err := api.LocalProjectBranches(ctx, ref)
	if err != nil {
		return dto.SaveQueryRequest{}, err
	}
	if branches == nil || branches.CurrentBranch == "" {
		return dto.SaveQueryRequest{}, dto.ErrUnsupportedCapability
	}
	head := ""
	for _, branch := range branches.Branches {
		if branch.Name == branches.CurrentBranch {
			head = branch.Head
			break
		}
	}
	if head == "" {
		return dto.SaveQueryRequest{}, dto.ErrUnsupportedCapability
	}
	folder, id := datatug.RootSharedFolderName, draft.ID
	if pos := strings.LastIndexByte(draft.ID, '/'); pos >= 0 {
		folder, id = draft.ID[:pos], draft.ID[pos+1:]
	}
	query := datatug.QueryDefWithFolderPath{FolderPath: folder, QueryDef: datatug.QueryDef{}}
	query.ID, query.Title, query.Type, query.Text, query.ConnectionID = id, draft.Title, datatug.QueryType(draft.Type), draft.Text, draft.ConnectionID
	for _, input := range draft.Inputs {
		if !core.ValidParameterName(input.Name) || !core.ValidInputType(input.Type) || input.Cardinality == "range" || input.Cardinality != "scalar" && input.Cardinality != "set" {
			return dto.SaveQueryRequest{}, ErrInvalid
		}
		query.Parameters = append(query.Parameters, datatug.ParameterDef{ID: input.Name, Type: string(input.Type), IsRequired: input.Required, IsMultiValue: input.Cardinality == "set"})
	}
	request := dto.SaveQueryRequest{ProjectRef: ref, Branch: branches.CurrentBranch, ExpectedBranchHead: head, OperationID: command.OperationID, IfNoneMatch: draft.ExpectedRevision == "", IfMatch: draft.ExpectedRevision, Query: query}
	if err := request.Validate(); err != nil {
		return dto.SaveQueryRequest{}, ErrInvalid
	}
	return request, nil
}

func (s LocalAIStore) finishAIQuery(ctx context.Context, id string, op *aiQueryOperation) error {
	if op.Kind != "query" || op.Request.ProjectID != s.Scope.Project || op.Request.StoreID != api.LocalStoreID || op.Request.OperationID != id || op.Receipt.OperationID != id {
		return ErrInvalid
	}
	scope, err := api.SecureProjectMutationScope(op.Request.ProjectRef, op.Request.Branch, "save-query")
	if err != nil {
		return err
	}
	if scope.ActorID != s.Scope.Actor {
		return ErrConflict
	}
	result, err := (api.LocalProjectQueryAdapter{}).SaveQuery(ctx, scope, op.Request)
	if err != nil {
		return err
	}
	if result == nil || result.Revision == "" {
		return ErrOutcomeUncertain
	}
	audit := AuditEvent{ID: id, At: op.Receipt.At, Actor: s.Scope.Actor, Project: s.Scope.Project, Action: "ai-confirm-draft-query"}
	recorded, err := s.Store.auditRecorded(audit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.Store.auditMutation(ctx, audit); err != nil {
			return err
		}
	}
	op.Done = true
	if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), id, op); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}
