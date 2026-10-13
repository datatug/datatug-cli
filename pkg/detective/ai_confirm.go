package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

type aiCaseOperation struct {
	Receipt ai.Receipt `json:"receipt"`
	Before  Case       `json:"before"`
	After   Case       `json:"after"`
	Audit   AuditEvent `json:"audit"`
	Done    bool       `json:"done"`
}

// AIReceipt returns a previously accepted confirmation and finishes an
// interrupted local Case/audit write before revealing the replay receipt.
func (s LocalAIStore) AIReceipt(ctx context.Context, id string) (ai.Receipt, bool, error) {
	if s.Store == nil || !validSlug(id) {
		return ai.Receipt{}, false, ErrInvalid
	}
	var receipt ai.Receipt
	var found bool
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), id)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var operation aiCaseOperation
		var header struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(data, &header) != nil {
			return ErrConflict
		}
		if header.Kind == "query" {
			var query aiQueryOperation
			if json.Unmarshal(data, &query) != nil || query.Receipt.ScopeKey != s.Scope.Key || query.Receipt.Actor != s.Scope.Actor || query.Receipt.Project != s.Scope.Project {
				return ErrConflict
			}
			if !query.Done {
				if err := s.finishAIQuery(ctx, id, &query); err != nil {
					return err
				}
			}
			receipt, found = query.Receipt, true
			return nil
		}
		if header.Kind == "run" {
			var run aiRunOperation
			if json.Unmarshal(data, &run) != nil || run.Receipt.ScopeKey != s.Scope.Key || run.Receipt.Actor != s.Scope.Actor || run.Receipt.Project != s.Scope.Project {
				return ErrConflict
			}
			if !run.Produced {
				return ErrOutcomeUncertain
			}
			if !run.Done {
				if err := s.finishAIRun(ctx, id, &run); err != nil {
					return err
				}
			}
			receipt, found = run.Receipt, true
			return nil
		}
		if header.Kind != "" && header.Kind != "case" {
			var catalog aiCatalogOperation
			if json.Unmarshal(data, &catalog) != nil || catalog.Receipt.ScopeKey != s.Scope.Key || catalog.Receipt.Actor != s.Scope.Actor || catalog.Receipt.Project != s.Scope.Project {
				return ErrConflict
			}
			if !catalog.Done {
				if err := s.finishAICatalog(ctx, id, &catalog); err != nil {
					return err
				}
			}
			receipt, found = catalog.Receipt, true
			return nil
		}
		if json.Unmarshal(data, &operation) != nil || operation.Receipt.ScopeKey != s.Scope.Key || operation.Receipt.Actor != s.Scope.Actor || operation.Receipt.Project != s.Scope.Project {
			return ErrConflict
		}
		if !operation.Done {
			if err := s.finishAICase(ctx, id, &operation); err != nil {
				return err
			}
		}
		receipt, found = operation.Receipt, true
		return nil
	})
	return receipt, found, err
}

// ConfirmCase applies only reviewed editable fields to the current Case.
// The prepared Core transition, metadata audit and scoped AI receipt are
// journaled before the Case file is replaced, making exact retries resumable.
func (s LocalAIStore) ConfirmCase(ctx context.Context, command ai.Command) (ai.Receipt, error) {
	if s.Store == nil || command.Scope != s.Scope || command.Proposal.Scope != s.Scope || command.Proposal.Draft.Kind != ai.KindCase || command.Proposal.Draft.Case == nil || !validSlug(command.OperationID) || !core.ValidSlug(command.Proposal.ID) || command.Binding.Project != s.Scope.Key || command.Binding.Actor != s.Scope.Actor || command.Binding.Action != "ai-confirm-edit-case" || command.Binding.ResourceID != command.Proposal.ID || len(command.Binding.PayloadDigest) != 64 {
		return ai.Receipt{}, ErrInvalid
	}
	var receipt ai.Receipt
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID)
		if err == nil {
			var prior aiCaseOperation
			if json.Unmarshal(data, &prior) != nil || !aiReceiptMatchesCommand(prior.Receipt, command) {
				return ErrConflict
			}
			if !prior.Done {
				if err := s.finishAICase(ctx, command.OperationID, &prior); err != nil {
					return err
				}
			}
			receipt = prior.Receipt
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		edit := command.Proposal.Draft.Case
		old, err := s.Store.getCase(edit.CaseID)
		if err != nil {
			return err
		}
		if old.Version != edit.ExpectedVersion {
			return ErrConflict
		}
		next := old
		if edit.Title != "" {
			next.Title = edit.Title
		}
		if edit.Context != nil {
			next.Context = make(map[string]InputValue, len(old.Context)+len(edit.Context))
			for key, value := range old.Context {
				next.Context[key] = value
			}
			for key, value := range edit.Context {
				next.Context[key] = value
			}
		}
		if edit.Findings != nil {
			next.Findings = append([]Finding(nil), edit.Findings...)
		}
		if edit.Status != "" {
			next.Status = edit.Status
		}
		if edit.ClosureReason != "" {
			next.ClosureReason = edit.ClosureReason
		}
		at := time.Now().UTC()
		next, audit, err := core.UpdateCase(old, next, s.Scope.Actor, at)
		if err != nil {
			return err
		}
		if err := s.Store.compactCaseTimeline(&next); err != nil {
			return err
		}
		audit.ID, audit.Action, audit.Project = command.OperationID, "ai-confirm-edit-case", s.Scope.Project
		receipt = ai.Receipt{OperationID: command.OperationID, ScopeKey: s.Scope.Key, Project: s.Scope.Project, Actor: s.Scope.Actor, ProposalID: command.Proposal.ID, PayloadDigest: command.Binding.PayloadDigest, ResourceID: next.ID, At: at}
		operation := aiCaseOperation{Receipt: receipt, Before: old, After: next, Audit: audit}
		if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID, operation); err != nil {
			return err
		}
		return s.finishAICase(ctx, command.OperationID, &operation)
	})
	return receipt, err
}

func aiReceiptMatchesCommand(receipt ai.Receipt, command ai.Command) bool {
	return receipt.OperationID == command.OperationID && receipt.ScopeKey == command.Scope.Key && receipt.Project == command.Scope.Project && receipt.Actor == command.Scope.Actor && receipt.ProposalID == command.Proposal.ID && receipt.PayloadDigest == command.Binding.PayloadDigest
}

func (s LocalAIStore) finishAICase(ctx context.Context, id string, operation *aiCaseOperation) error {
	current, err := s.Store.getCase(operation.After.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, operation.After) {
		if !reflect.DeepEqual(current, operation.Before) {
			return ErrConflict
		}
		if err := writeItem(s.Store.privateItemDir("cases"), operation.After.ID, operation.After); err != nil {
			return err
		}
	}
	recorded, err := s.Store.auditRecorded(operation.Audit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.Store.auditMutation(ctx, operation.Audit); err != nil {
			return err
		}
	}
	operation.Done = true
	if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), id, operation); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}
