package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

type aiRunOperation struct {
	Kind              string            `json:"kind"`
	Receipt           ai.Receipt        `json:"receipt"`
	Draft             ai.RunDraft       `json:"draft"`
	CaseContextDigest string            `json:"caseContextDigest,omitempty"`
	QueryRevisions    map[string]string `json:"queryRevisions,omitempty"`
	CaseLinkStatus    string            `json:"caseLinkStatus,omitempty"`
	Run               CheckRun          `json:"run,omitempty"`
	Before            *Case             `json:"before,omitempty"`
	After             *Case             `json:"after,omitempty"`
	CaseAudit         *AuditEvent       `json:"caseAudit,omitempty"`
	Produced          bool              `json:"produced"`
	Done              bool              `json:"done"`
}

var (
	confirmRunInterlock func()
	loadCatalog         = core.LoadCatalog
	evaluateCheck       = core.EvaluateCheck
)

// ConfirmRun accepts the confirmed intent before any query dispatch. An
// interrupted network call remains uncertain and is never silently repeated.
// Once evaluation returns, its exact result is journaled before run/evidence
// and Case writes, so those writes can be reconciled without re-execution.
func (s LocalAIStore) ConfirmRun(ctx context.Context, command ai.Command, runner QueryRunner, metadata ExecutionMetadata, revisions ...QueryRevisionLookup) (ai.Receipt, error) {
	if s.Store == nil || command.Scope != s.Scope || command.Proposal.Scope != s.Scope || command.Proposal.Draft.Kind != ai.KindRun || command.Proposal.Draft.Run == nil || !validSlug(command.OperationID) || command.Binding.Project != s.Scope.Key || command.Binding.Actor != s.Scope.Actor || command.Binding.Action != "ai-confirm-run-check" || command.Binding.ResourceID != command.Proposal.ID || len(command.Binding.PayloadDigest) != 64 || runner == nil {
		return ai.Receipt{}, ErrInvalid
	}
	draft := *command.Proposal.Draft.Run
	effectiveInputs := draft.Inputs
	if !validSlug(draft.CheckID) || draft.Environment == "" || draft.CaseID != "" && !validSlug(draft.CaseID) {
		return ai.Receipt{}, ErrInvalid
	}
	var receipt ai.Receipt
	var accepted bool
	err := s.Store.withLock(ctx, func() error {
		if err := s.Store.reconcilePendingActions(ctx, "cases", draft.CaseID, command.OperationID); err != nil {
			return err
		}
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID)
		if err == nil {
			var prior aiRunOperation
			if json.Unmarshal(data, &prior) != nil || prior.Kind != "run" || !aiReceiptMatchesCommand(prior.Receipt, command) {
				return ErrConflict
			}
			if !prior.Produced {
				return ErrOutcomeUncertain
			}
			if !prior.Done {
				if err := s.finishAIRun(ctx, command.OperationID, &prior); err != nil {
					return err
				}
			}
			receipt = prior.Receipt
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
	if err != nil || receipt.OperationID != "" {
		return receipt, err
	}
	if confirmRunInterlock != nil {
		confirmRunInterlock()
	}
	var catalog core.Catalog
	var admittedRevisions map[string]string
	var lookup QueryRevisionLookup
	if len(revisions) > 0 {
		lookup = revisions[0]
	}
	sum := sha256.Sum256([]byte(s.Scope.Key + "\x00" + s.Scope.Actor + "\x00" + command.OperationID))
	runID := "run-" + hex.EncodeToString(sum[:16])
	at := time.Now().UTC()
	receipt = ai.Receipt{OperationID: command.OperationID, ScopeKey: s.Scope.Key, Project: s.Scope.Project, Actor: s.Scope.Actor, ProposalID: command.Proposal.ID, PayloadDigest: command.Binding.PayloadDigest, ResourceID: runID, At: at}
	err = s.Store.withLock(ctx, func() error {
		if err := s.Store.reconcilePendingActions(ctx, "cases", draft.CaseID, command.OperationID); err != nil {
			return err
		}
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID)
		if err == nil {
			var prior aiRunOperation
			if json.Unmarshal(data, &prior) != nil || !aiReceiptMatchesCommand(prior.Receipt, command) {
				return ErrConflict
			}
			return ErrOutcomeUncertain
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.verifyAIBasisLocked(ctx, command.Proposal); err != nil {
			return err
		}
		caseDigest := ""
		var caseContext map[string]InputValue
		if draft.CaseID != "" {
			c, err := s.Store.getCase(draft.CaseID)
			if err != nil {
				return err
			}
			if c.Status != "active" {
				return ErrInvalid
			}
			caseContext = c.Context
			caseDigest, err = caseContextDigest(c.Context)
			if err != nil {
				return err
			}
		}
		catalog, err = loadCatalog(ctx, draft.CheckID, "", catalogSnapshotReader{s.Store})
		if err != nil {
			return err
		}
		admittedRevisions, err = captureQueryRevisions(ctx, catalog, "", lookup)
		if err != nil {
			return err
		}
		check, ok := catalog.Checks[draft.CheckID]
		if !ok {
			return ErrNotFound
		}
		if draft.CaseID != "" {
			effectiveInputs, err = core.BindCaseInputs(check.Inputs, draft.Inputs, caseContext)
		} else {
			err = core.ValidateInputs(check.Inputs, draft.Inputs)
		}
		if err != nil {
			return err
		}
		accepted = true
		return writeItem(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID, aiRunOperation{Kind: "run", Receipt: receipt, Draft: draft, CaseContextDigest: caseDigest, QueryRevisions: admittedRevisions})
	})
	if err != nil || !accepted {
		return ai.Receipt{}, err
	}
	if lookup != nil {
		ctx = withAdmittedQueryRevisions(ctx, admittedRevisions)
	}
	run, err := evaluateCheck(ctx, core.RunRequest{ID: runID, CheckID: draft.CheckID, CaseID: draft.CaseID, Environment: draft.Environment, Actor: s.Scope.Actor, Inputs: effectiveInputs}, catalog, runner, metadata)
	if err != nil {
		return ai.Receipt{}, err
	}
	err = s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID)
		if err != nil {
			return err
		}
		var op aiRunOperation
		if json.Unmarshal(data, &op) != nil || op.Kind != "run" || !aiReceiptMatchesCommand(op.Receipt, command) || op.Produced {
			return ErrConflict
		}
		op.Run = run
		op.Produced = true
		if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID, op); err != nil {
			return err
		}
		return s.finishAIRun(ctx, command.OperationID, &op)
	})
	return receipt, err
}

func (s LocalAIStore) finishAIRun(ctx context.Context, id string, op *aiRunOperation) error {
	if op.Kind != "run" || !op.Produced || op.Run.ID != op.Receipt.ResourceID || op.Run.Actor != s.Scope.Actor || op.Run.CaseID != op.Draft.CaseID {
		return ErrInvalid
	}
	if op.Draft.CaseID != "" && op.CaseContextDigest != "" {
		if err := s.Store.saveRunCohortProof(op.Run.ID, op.Draft.CaseID, op.CaseContextDigest); err != nil {
			return err
		}
	}
	data, err := safeReadFile(s.Store.privateItemDir("runs"), op.Run.ID)
	if errors.Is(err, os.ErrNotExist) {
		if err := writeItem(s.Store.privateItemDir("runs"), op.Run.ID, op.Run); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		var current CheckRun
		if json.Unmarshal(data, &current) != nil || !reflect.DeepEqual(current, op.Run) {
			return ErrConflict
		}
	}
	runAudit := core.AuditForRun(op.Run)
	runAudit.Project = s.Scope.Project
	recorded, err := s.Store.auditRecorded(runAudit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.Store.auditMutation(ctx, runAudit); err != nil {
			return err
		}
	}
	if op.Draft.CaseID != "" && op.After == nil && op.CaseLinkStatus == "" {
		before, err := s.Store.getCase(op.Draft.CaseID)
		if errors.Is(err, ErrNotFound) {
			op.CaseLinkStatus = "case-unavailable"
		} else if err != nil {
			return err
		} else if before.Status == "archived" {
			op.CaseLinkStatus = "case-archived"
		} else if err := s.Store.authorizeCaseRunLink(before, op.Run); err != nil {
			if !errors.Is(err, ErrInvalid) {
				return err
			}
			op.CaseLinkStatus = "case-changed"
		}
		if op.CaseLinkStatus == "" {
			sum := sha256.Sum256([]byte(s.Scope.Key + "\x00" + s.Scope.Actor + "\x00" + id))
			caseEventID := "event-" + hex.EncodeToString(sum[16:])
			after, audit, err := core.AttachRun(before, op.Run, caseEventID, s.Scope.Actor, time.Now().UTC())
			if err != nil {
				op.CaseLinkStatus = "link-unavailable"
			} else {
				if err := s.Store.compactCaseTimeline(&after); err != nil {
					return err
				}
				audit.Project = s.Scope.Project
				op.Before = &before
				op.After = &after
				op.CaseAudit = &audit
				op.CaseLinkStatus = "linked"
			}
		}
		if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), id, op); err != nil {
			return err
		}
	}
	if op.After != nil {
		if op.Before == nil || op.CaseAudit == nil || op.After.ID != op.Draft.CaseID {
			return ErrInvalid
		}
		current, err := s.Store.getCase(op.After.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, *op.After) {
			if !reflect.DeepEqual(current, *op.Before) {
				return ErrConflict
			}
			if err := writeItem(s.Store.privateItemDir("cases"), op.After.ID, op.After); err != nil {
				return err
			}
		}
		recorded, err = s.Store.auditRecorded(*op.CaseAudit)
		if err != nil {
			return err
		}
		if !recorded {
			if err := s.Store.auditMutation(ctx, *op.CaseAudit); err != nil {
				return err
			}
		}
	}
	op.Done = true
	if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), id, op); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}
