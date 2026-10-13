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

type aiCatalogOperation struct {
	Kind    string          `json:"kind"`
	Receipt ai.Receipt      `json:"receipt"`
	Before  json.RawMessage `json:"before,omitempty"`
	After   json.RawMessage `json:"after"`
	Audit   AuditEvent      `json:"audit"`
	Done    bool            `json:"done"`
}

// ConfirmCatalog journals the already-reviewed shared Core draft, the exact
// previous definition and an audit intent before replacing a project file.
// A crash between any two writes can be reconciled by the same operation ID.
func (s LocalAIStore) ConfirmCatalog(ctx context.Context, command ai.Command) (ai.Receipt, error) {
	if s.Store == nil || command.Scope != s.Scope || command.Proposal.Scope != s.Scope || !validSlug(command.OperationID) || !core.ValidSlug(command.Proposal.ID) || command.Binding.Project != s.Scope.Key || command.Binding.Actor != s.Scope.Actor || command.Binding.ResourceID != command.Proposal.ID || len(command.Binding.PayloadDigest) != 64 {
		return ai.Receipt{}, ErrInvalid
	}
	kind := command.Proposal.Draft.Kind
	if kind != ai.KindCheck && kind != ai.KindValidator && kind != ai.KindScenario {
		return ai.Receipt{}, ErrInvalid
	}
	if command.Binding.Action != "ai-confirm-"+kind {
		return ai.Receipt{}, ErrInvalid
	}
	var receipt ai.Receipt
	err := s.Store.withLock(ctx, func() error {
		data, err := safeReadFile(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID)
		if err == nil {
			var prior aiCatalogOperation
			if json.Unmarshal(data, &prior) != nil || prior.Kind != kind || !aiReceiptMatchesCommand(prior.Receipt, command) {
				return ErrConflict
			}
			if !prior.Done {
				if err := s.finishAICatalog(ctx, command.OperationID, &prior); err != nil {
					return err
				}
			}
			receipt = prior.Receipt
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		resourceID, before, after, audit, err := s.prepareAICatalog(command)
		if err != nil {
			return err
		}
		at := audit.At
		receipt = ai.Receipt{OperationID: command.OperationID, ScopeKey: s.Scope.Key, Project: s.Scope.Project, Actor: s.Scope.Actor, ProposalID: command.Proposal.ID, PayloadDigest: command.Binding.PayloadDigest, ResourceID: resourceID, At: at}
		operation := aiCatalogOperation{Kind: kind, Receipt: receipt, Before: before, After: after, Audit: audit}
		if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), command.OperationID, operation); err != nil {
			return err
		}
		return s.finishAICatalog(ctx, command.OperationID, &operation)
	})
	return receipt, err
}

func (s LocalAIStore) prepareAICatalog(command ai.Command) (string, json.RawMessage, json.RawMessage, AuditEvent, error) {
	var before, after json.RawMessage
	var resourceID string
	var checkID string
	var collection string
	var value any
	var version int64
	var auditAction string
	bad := func(err error) (string, json.RawMessage, json.RawMessage, AuditEvent, error) {
		return "", nil, nil, AuditEvent{}, err
	}
	switch command.Proposal.Draft.Kind {
	case ai.KindCheck:
		if command.Proposal.Draft.Check == nil {
			return bad(ErrInvalid)
		}
		item := *command.Proposal.Draft.Check
		if err := core.ValidateCheck(item); err != nil {
			return bad(err)
		}
		for i := range item.Steps {
			if item.Steps[i].Kind == "query" {
				id, err := s.Store.resolveQueryRef(item.Steps[i].QueryID)
				if err != nil {
					return bad(err)
				}
				item.Steps[i].QueryID = id
			}
		}
		resourceID, checkID, collection, auditAction = item.ID, item.ID, "checks", "ai-confirm-draft-check"
		data, err := safeReadFile(s.Store.definitionDir(collection), resourceID)
		if err == nil {
			var old Check
			if json.Unmarshal(data, &old) != nil {
				return bad(ErrInvalid)
			}
			if item.Version != old.Version {
				return bad(ErrConflict)
			}
			before = data
			item.Version++
		} else if errors.Is(err, os.ErrNotExist) {
			if item.Version != 0 {
				return bad(ErrConflict)
			}
			item.Version = 1
		} else {
			return bad(err)
		}
		items, err := readItems[Check](s.Store.definitionDir("checks"))
		if err != nil {
			return bad(err)
		}
		if before == nil && len(items) >= maxItems {
			return bad(ErrCapacity)
		}
		graph := map[string]Check{}
		for _, v := range items {
			graph[v.ID] = v
		}
		graph[item.ID] = item
		validators, err := readItems[PublishedValidator](s.Store.definitionDir("validators"))
		if err != nil {
			return bad(err)
		}
		vm := map[string]PublishedValidator{}
		for _, v := range validators {
			vm[v.ID] = v
		}
		locals, err := readItems[QueryValidators](s.Store.definitionDir("query-validators"))
		if err != nil {
			return bad(err)
		}
		qm := map[string]QueryValidators{}
		for _, v := range locals {
			qm[v.QueryID] = v
		}
		if err := validateGraph(graph, vm, qm); err != nil {
			return bad(err)
		}
		if err := s.Store.ensureQueryRefs(graph, qm); err != nil {
			return bad(err)
		}
		item.UpdatedAt = time.Now().UTC()
		item.UpdatedBy = s.Scope.Actor
		version, value = item.Version, item
	case ai.KindValidator:
		if command.Proposal.Draft.Validator == nil {
			return bad(ErrInvalid)
		}
		item := *command.Proposal.Draft.Validator
		if err := core.ValidatePublishedValidator(item); err != nil {
			return bad(err)
		}
		resourceID, collection, auditAction = item.ID, "validators", "ai-confirm-draft-validator"
		data, err := safeReadFile(s.Store.definitionDir(collection), resourceID)
		if err == nil {
			var old PublishedValidator
			if json.Unmarshal(data, &old) != nil {
				return bad(ErrInvalid)
			}
			if item.Version != old.Version {
				return bad(ErrConflict)
			}
			before = data
			item.Version++
		} else if errors.Is(err, os.ErrNotExist) {
			if item.Version != 0 {
				return bad(ErrConflict)
			}
			item.Version = 1
		} else {
			return bad(err)
		}
		checks, err := readItems[Check](s.Store.definitionDir("checks"))
		if err != nil {
			return bad(err)
		}
		graph := map[string]Check{}
		for _, v := range checks {
			graph[v.ID] = v
		}
		validators, err := readItems[PublishedValidator](s.Store.definitionDir("validators"))
		if err != nil {
			return bad(err)
		}
		if before == nil && len(validators) >= maxItems {
			return bad(ErrCapacity)
		}
		vm := map[string]PublishedValidator{}
		for _, v := range validators {
			vm[v.ID] = v
		}
		vm[item.ID] = item
		locals, err := readItems[QueryValidators](s.Store.definitionDir("query-validators"))
		if err != nil {
			return bad(err)
		}
		qm := map[string]QueryValidators{}
		for _, v := range locals {
			qm[v.QueryID] = v
		}
		if err := validateGraph(graph, vm, qm); err != nil {
			return bad(err)
		}
		if err := s.Store.ensureQueryRefs(graph, qm); err != nil {
			return bad(err)
		}
		version, value = item.Version, item
	case ai.KindScenario:
		if command.Proposal.Draft.Scenario == nil {
			return bad(ErrInvalid)
		}
		item := *command.Proposal.Draft.Scenario
		if err := core.ValidateScenario(item); err != nil {
			return bad(err)
		}
		for i := range item.Steps {
			if item.Steps[i].Kind == "query" {
				id, err := s.Store.resolveQueryRef(item.Steps[i].Ref)
				if err != nil {
					return bad(err)
				}
				item.Steps[i].Ref = id
			} else if _, err := s.Store.getCheck(item.Steps[i].Ref); err != nil {
				return bad(err)
			}
		}
		resourceID, collection, auditAction = item.ID, "scenarios", "ai-confirm-draft-scenario"
		data, err := safeReadFile(s.Store.definitionDir(collection), resourceID)
		if err == nil {
			var old Scenario
			if json.Unmarshal(data, &old) != nil {
				return bad(ErrInvalid)
			}
			if item.Version != old.Version {
				return bad(ErrConflict)
			}
			before = data
			item.Version++
		} else if errors.Is(err, os.ErrNotExist) {
			if item.Version != 0 {
				return bad(ErrConflict)
			}
			items, e := readItems[Scenario](s.Store.definitionDir(collection))
			if e != nil {
				return bad(e)
			}
			if len(items) >= maxItems {
				return bad(ErrCapacity)
			}
			item.Version = 1
		} else {
			return bad(err)
		}
		version, value = item.Version, item
	}
	if !validSlug(resourceID) || version < 1 {
		return bad(ErrInvalid)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return bad(err)
	}
	after = encoded
	at := time.Now().UTC()
	audit := AuditEvent{ID: command.OperationID, At: at, Actor: s.Scope.Actor, Project: s.Scope.Project, Action: auditAction, CheckID: checkID}
	return resourceID, before, after, audit, nil
}

func aiCatalogCollection(kind string) string {
	switch kind {
	case ai.KindCheck:
		return "checks"
	case ai.KindValidator:
		return "validators"
	case ai.KindScenario:
		return "scenarios"
	}
	return ""
}
func (s LocalAIStore) finishAICatalog(ctx context.Context, id string, op *aiCatalogOperation) error {
	collection := aiCatalogCollection(op.Kind)
	if collection == "" || !validSlug(op.Receipt.ResourceID) {
		return ErrInvalid
	}
	current, err := safeReadFile(s.Store.definitionDir(collection), op.Receipt.ResourceID)
	if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else if err != nil {
		return err
	}
	if !jsonSame(current, op.After) {
		if !jsonSame(current, op.Before) {
			return ErrConflict
		}
		if err := writeItem(s.Store.definitionDir(collection), op.Receipt.ResourceID, json.RawMessage(op.After)); err != nil {
			return err
		}
	}
	recorded, err := s.Store.auditRecorded(op.Audit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.Store.auditMutation(ctx, op.Audit); err != nil {
			return err
		}
	}
	op.Done = true
	if err := writeItem(s.Store.privateItemDir("ai-confirm-operations"), id, op); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}
func jsonSame(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}
