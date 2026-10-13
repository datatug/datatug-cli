package detective

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	ai "github.com/datatug/datatug-core/pkg/detectiveai"
)

// ActionOperation is the local, private counterpart of the hosted operation
// receipt. Its accepted state is durable before any query or LLM network call.
// An accepted run without a produced result is intentionally uncertain.
type actionOperation struct {
	Receipt  core.OperationReceipt `json:"receipt"`
	Response ActionResponse        `json:"response,omitempty"`
	Kind     string                `json:"kind"`
	ID       string                `json:"id,omitempty"`
	Before   json.RawMessage       `json:"before,omitempty"`
	After    json.RawMessage       `json:"after,omitempty"`
	Audit    AuditEvent            `json:"audit,omitempty"`
	RunAudit *AuditEvent           `json:"runAudit,omitempty"`
	CaseID   string                `json:"caseId,omitempty"`
	Produced bool                  `json:"produced"`
	Done     bool                  `json:"done"`
}

func actionResourceID(req ActionRequest) string {
	switch req.Type {
	case "save-check":
		if req.Check != nil {
			return req.Check.ID
		}
	case "save-validator":
		if req.Validator != nil {
			return req.Validator.ID
		}
	case "save-query-validators":
		if req.QueryValidators != nil {
			return req.QueryValidators.QueryID
		}
	case "save-scenario":
		if req.Scenario != nil {
			return req.Scenario.ID
		}
	case "create-case", "update-case":
		if req.Case != nil {
			return req.Case.ID
		}
	case "run-check":
		return req.CheckID
	case "run-query-validators":
		return req.QueryID
	default:
		return req.CaseID
	}
	return ""
}

func actionResourceTarget(req ActionRequest) (string, string) {
	switch req.Type {
	case "save-check":
		return "checks", actionResourceID(req)
	case "save-validator":
		return "validators", actionResourceID(req)
	case "save-query-validators":
		return "query-validators", queryValidatorFileID(actionResourceID(req))
	case "save-scenario":
		return "scenarios", actionResourceID(req)
	case "create-case", "update-case":
		return "cases", actionResourceID(req)
	case "apply-scenario", "record-scenario-step", "append-event", "run-check", "run-query-validators":
		return "cases", req.CaseID
	}
	return "", ""
}

func operationTarget(op actionOperation) (string, string) {
	if op.Kind == "run" {
		return "cases", op.CaseID
	}
	return op.Kind, op.ID
}

// A later edit must first finish a prior materialized write and its audit.
// Otherwise a retry of the earlier operation can be stranded by the newer
// resource image. The journal is read in bounded directory batches.
func (s *LocalStore) reconcilePendingActions(ctx context.Context, collection, resourceID, exceptID string) error {
	if collection == "" || resourceID == "" {
		return nil
	}
	if err := s.reconcilePendingOther(ctx, collection, resourceID); err != nil {
		return err
	}
	dir := s.privateItemDir("action-operations")
	if err := ensureDirIfExists(dir); err != nil {
		return err
	}
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for {
		names, err := f.Readdirnames(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			if id == exceptID {
				continue
			}
			data, readErr := safeReadFile(dir, id)
			if readErr != nil {
				return readErr
			}
			var op actionOperation
			if json.Unmarshal(data, &op) != nil {
				return ErrInvalid
			}
			kind, targetID := operationTarget(op)
			if !op.Done && op.Produced && kind == collection && targetID == resourceID {
				if finishErr := s.finishAction(ctx, id, &op); finishErr != nil {
					return finishErr
				}
			}
		}
		if len(names) == 0 {
			return nil
		}
	}
}

// ActionBinding omits the ephemeral securityContextId; the endpoint verifies
// the current session and actor before every call, including exact replay.
func (s *LocalStore) ActionBinding(req ActionRequest, actor string) (core.OperationBinding, error) {
	if req.Project != s.projectID || actor == "" || req.Type == "" || actionResourceID(req) == "" {
		return core.OperationBinding{}, ErrInvalid
	}
	req.SecurityContextID = ""
	return core.BindOperation(s.projectID, actor, req.Type, actionResourceID(req), req)
}

func actionRunID(project, actor, operationID string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + actor + "\x00" + operationID))
	return "run-" + hex.EncodeToString(sum[:16])
}

func actionEventID(project, actor, operationID string) string {
	sum := sha256.Sum256([]byte("event\x00" + project + "\x00" + actor + "\x00" + operationID))
	return "event-" + hex.EncodeToString(sum[:16])
}

// ExecuteAction owns all ordinary local Detective mutations and manual runs.
// The caller must authorize the current session, query and execution receipt
// before entering this method. A missing operation ID is a contract error.
func (s *LocalStore) ExecuteAction(ctx context.Context, operationID, actor string, req ActionRequest, runner QueryRunner, metadata ExecutionMetadata, result *apicontract.Result, proof *ScenarioQueryProof) (ActionResponse, error) {
	var empty ActionResponse
	if !validSlug(operationID) {
		return empty, ErrInvalid
	}
	binding, err := s.ActionBinding(req, actor)
	if err != nil {
		return empty, err
	}
	var prior actionOperation
	err = s.withLock(ctx, func() error {
		collection, resourceID := actionResourceTarget(req)
		if e := s.reconcilePendingActions(ctx, collection, resourceID, operationID); e != nil {
			return e
		}
		data, e := safeReadFile(s.privateItemDir("action-operations"), operationID)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if json.Unmarshal(data, &prior) != nil || core.CheckOperationReplay(prior.Receipt, binding) != nil {
			return ErrConflict
		}
		if !prior.Produced {
			return ErrOutcomeUncertain
		}
		if !prior.Done {
			return s.finishAction(ctx, operationID, &prior)
		}
		return nil
	})
	if err != nil {
		return empty, err
	}
	if prior.Receipt.ID != "" {
		return prior.Response, nil
	}
	if req.Type == "run-check" || req.Type == "run-query-validators" {
		return s.executeRunAction(ctx, operationID, actor, req, binding, runner, metadata, result)
	}
	var op actionOperation
	err = s.withLock(ctx, func() error {
		data, e := safeReadFile(s.privateItemDir("action-operations"), operationID)
		if e == nil {
			if json.Unmarshal(data, &op) != nil || core.CheckOperationReplay(op.Receipt, binding) != nil {
				return ErrConflict
			}
			if !op.Produced {
				return ErrOutcomeUncertain
			}
			return s.finishAction(ctx, operationID, &op)
		}
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		var prepErr error
		op, prepErr = s.prepareAction(req, actor, operationID, proof)
		if prepErr != nil {
			return prepErr
		}
		version := actionResponseVersion(op.Response)
		op.Receipt, prepErr = core.NewOperationReceipt(operationID, binding, version, time.Now().UTC())
		if prepErr != nil {
			return prepErr
		}
		op.Produced = true
		if e := writeItem(s.privateItemDir("action-operations"), operationID, op); e != nil {
			return e
		}
		return s.finishAction(ctx, operationID, &op)
	})
	if err != nil {
		return empty, err
	}
	return op.Response, nil
}

func actionResponseVersion(r ActionResponse) int64 {
	switch {
	case r.Check != nil:
		return r.Check.Version
	case r.Validator != nil:
		return r.Validator.Version
	case r.QueryValidators != nil:
		return r.QueryValidators.Version
	case r.Scenario != nil:
		return r.Scenario.Version
	case r.Case != nil:
		return r.Case.Version
	default:
		return 0
	}
}

func (s *LocalStore) executeRunAction(ctx context.Context, id, actor string, req ActionRequest, binding core.OperationBinding, runner QueryRunner, metadata ExecutionMetadata, result *apicontract.Result) (ActionResponse, error) {
	var empty ActionResponse
	if runner == nil || req.Environment == "" || result != nil && req.Type != "run-query-validators" {
		return empty, ErrInvalid
	}
	checkID, queryID := "", ""
	if req.Type == "run-check" {
		checkID = req.CheckID
	} else {
		queryID = req.QueryID
	}
	catalog, err := s.loadCatalog(ctx, checkID, queryID, req.CaseID)
	if err != nil {
		return empty, err
	}
	if checkID != "" {
		if err := core.ValidateInputs(catalog.Checks[checkID].Inputs, req.Inputs); err != nil {
			return empty, err
		}
	}
	runID := actionRunID(s.projectID, actor, id)
	accepted, err := core.NewOperationReceipt(id, binding, 0, time.Now().UTC())
	if err != nil {
		return empty, err
	}
	err = s.withLock(ctx, func() error {
		data, e := safeReadFile(s.privateItemDir("action-operations"), id)
		if e == nil {
			var existing actionOperation
			if json.Unmarshal(data, &existing) != nil || core.CheckOperationReplay(existing.Receipt, binding) != nil {
				return ErrConflict
			}
			return ErrOutcomeUncertain
		}
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		return writeItem(s.privateItemDir("action-operations"), id, actionOperation{Receipt: accepted, Kind: "run", ID: runID})
	})
	if err != nil {
		return empty, err
	}
	coreReq := core.RunRequest{ID: runID, CheckID: checkID, QueryID: queryID, CaseID: req.CaseID, Environment: req.Environment, Actor: actor, Inputs: req.Inputs}
	var run CheckRun
	if checkID != "" {
		run, err = core.EvaluateCheck(ctx, coreReq, catalog, runner, metadata)
	} else if result == nil {
		run, err = core.EvaluateQueryValidators(ctx, coreReq, catalog, runner, metadata)
	} else {
		run, err = core.EvaluateQueryValidatorsForResult(ctx, coreReq, catalog, *result, runner, metadata)
	}
	if err != nil {
		return empty, err
	}
	var op actionOperation
	err = s.withLock(ctx, func() error {
		data, e := safeReadFile(s.privateItemDir("action-operations"), id)
		if e != nil {
			return e
		}
		if json.Unmarshal(data, &op) != nil || core.CheckOperationReplay(op.Receipt, binding) != nil || op.Produced || op.ID != runID {
			return ErrConflict
		}
		op.Response.Run = &run
		audit := core.AuditForRun(run)
		audit.Project = s.projectID
		op.RunAudit = &audit
		op.CaseID = req.CaseID
		op.Produced = true
		if e := writeItem(s.privateItemDir("action-operations"), id, op); e != nil {
			return e
		}
		return s.finishAction(ctx, id, &op)
	})
	if err != nil {
		return empty, err
	}
	return op.Response, nil
}

func (s *LocalStore) finishAction(ctx context.Context, id string, op *actionOperation) error {
	if !op.Produced || core.CheckOperationReplay(op.Receipt, core.OperationBinding{Project: op.Receipt.Project, Actor: op.Receipt.Actor, Action: op.Receipt.Action, ResourceID: op.Receipt.ResourceID, PayloadDigest: op.Receipt.PayloadDigest}) != nil {
		return ErrInvalid
	}
	if op.Kind == "run" {
		if op.Response.Run == nil || op.Response.Run.ID != op.ID || op.RunAudit == nil {
			return ErrInvalid
		}
		if err := s.finishActionFile(s.privateItemDir("runs"), op.ID, nil, op.Response.Run); err != nil {
			return err
		}
		if err := s.appendAuditOnce(ctx, *op.RunAudit); err != nil {
			return err
		}
		if op.CaseID != "" && len(op.After) == 0 {
			before, err := s.getCase(op.CaseID)
			if err != nil {
				return err
			}
			after, caseAudit, err := core.AttachRun(before, *op.Response.Run, actionEventID(s.projectID, op.Receipt.Actor, id), op.Receipt.Actor, time.Now().UTC())
			if err != nil {
				return err
			}
			if err := s.compactCaseTimeline(&after); err != nil {
				return err
			}
			op.Before, _ = json.Marshal(before)
			op.After, _ = json.Marshal(after)
			caseAudit.Project = s.projectID
			caseAudit.ID = id + "-case"
			op.Audit = caseAudit
			if err := writeItem(s.privateItemDir("action-operations"), id, op); err != nil {
				return err
			}
		}
	}
	if len(op.After) != 0 {
		collection := op.Kind
		resourceID := op.ID
		if collection == "run" {
			collection = "cases"
			resourceID = op.CaseID
		}
		dir := s.privateItemDir(collection)
		if collection == "checks" || collection == "validators" || collection == "query-validators" || collection == "scenarios" {
			dir = s.definitionDir(collection)
		}
		if err := s.finishActionFile(dir, resourceID, op.Before, json.RawMessage(op.After)); err != nil {
			return err
		}
		if err := s.appendAuditOnce(ctx, op.Audit); err != nil {
			return err
		}
	}
	op.Done = true
	if err := writeItem(s.privateItemDir("action-operations"), id, op); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}

func (s *LocalStore) finishActionFile(dir, id string, before []byte, after any) error {
	current, err := safeReadFile(dir, id)
	if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else if err != nil {
		return err
	}
	wanted, err := json.Marshal(after)
	if err != nil {
		return err
	}
	if jsonSame(current, wanted) {
		return nil
	}
	if !jsonSame(current, before) {
		return ErrConflict
	}
	return writeItem(dir, id, after)
}

func (s *LocalStore) appendAuditOnce(ctx context.Context, audit AuditEvent) error {
	if audit.ID == "" {
		return ErrInvalid
	}
	recorded, err := s.auditRecorded(audit)
	if err != nil {
		return err
	}
	if !recorded {
		return s.auditMutation(ctx, audit)
	}
	return nil
}

func (s *LocalStore) prepareAction(req ActionRequest, actor, id string, proof *ScenarioQueryProof) (actionOperation, error) {
	var op actionOperation
	var err error
	bad := func(e error) (actionOperation, error) { return actionOperation{}, e }
	switch req.Type {
	case "save-check", "save-validator", "save-scenario":
		kind := map[string]string{"save-check": ai.KindCheck, "save-validator": ai.KindValidator, "save-scenario": ai.KindScenario}[req.Type]
		if kind == ai.KindCheck && req.Check == nil || kind == ai.KindValidator && req.Validator == nil || kind == ai.KindScenario && req.Scenario == nil {
			return bad(ErrInvalid)
		}
		fake := LocalAIStore{Store: s, Scope: ai.Scope{Key: s.projectID, Project: s.projectID, Actor: actor}}
		command := ai.Command{Scope: fake.Scope, OperationID: id, Proposal: ai.Proposal{Draft: ai.Draft{Kind: kind, Check: req.Check, Validator: req.Validator, Scenario: req.Scenario}}}
		resource, before, after, audit, e := fake.prepareAICatalog(command)
		if e != nil {
			return bad(e)
		}
		op.Kind, op.ID, op.Before, op.After, op.Audit = aiCatalogCollection(kind), resource, before, after, audit
		if kind == ai.KindCheck {
			var v Check
			_ = json.Unmarshal(after, &v)
			op.Response.Check = &v
		}
		if kind == ai.KindValidator {
			var v PublishedValidator
			_ = json.Unmarshal(after, &v)
			op.Response.Validator = &v
		}
		if kind == ai.KindScenario {
			var v Scenario
			_ = json.Unmarshal(after, &v)
			op.Response.Scenario = &v
		}
	case "save-query-validators":
		if req.QueryValidators == nil {
			return bad(ErrInvalid)
		}
		item := *req.QueryValidators
		if err := core.ValidateQueryValidators(item); err != nil {
			return bad(err)
		}
		item.QueryID, err = s.resolveQueryRef(item.QueryID)
		if err != nil {
			return bad(err)
		}
		op.Kind, op.ID = "query-validators", queryValidatorFileID(item.QueryID)
		data, e := safeReadFile(s.definitionDir(op.Kind), op.ID)
		if e == nil {
			var old QueryValidators
			if json.Unmarshal(data, &old) != nil || old.QueryID != item.QueryID || old.Version != item.Version {
				return bad(ErrConflict)
			}
			op.Before = data
			item.Version++
		} else if errors.Is(e, os.ErrNotExist) {
			if item.Version != 0 {
				return bad(ErrConflict)
			}
			item.Version = 1
		} else {
			return bad(e)
		}
		checks, e := readItems[Check](s.definitionDir("checks"))
		if e != nil {
			return bad(e)
		}
		validators, e := readItems[PublishedValidator](s.definitionDir("validators"))
		if e != nil {
			return bad(e)
		}
		locals, e := readItems[QueryValidators](s.definitionDir(op.Kind))
		if e != nil {
			return bad(e)
		}
		if op.Before == nil && len(locals) >= maxItems {
			return bad(ErrCapacity)
		}
		cm := map[string]Check{}
		for _, v := range checks {
			cm[v.ID] = v
		}
		vm := map[string]PublishedValidator{}
		for _, v := range validators {
			vm[v.ID] = v
		}
		qm := map[string]QueryValidators{}
		for _, v := range locals {
			qm[v.QueryID] = v
		}
		qm[item.QueryID] = item
		if e := validateGraph(cm, vm, qm); e != nil {
			return bad(e)
		}
		if e := s.ensureQueryRefs(cm, qm); e != nil {
			return bad(e)
		}
		op.After, _ = json.Marshal(item)
		op.Response.QueryValidators = &item
	case "create-case", "update-case", "apply-scenario", "record-scenario-step", "append-event":
		op.Kind = "cases"
		op.ID = actionResourceID(req)
		var old Case
		if req.Type != "create-case" {
			old, err = s.getCase(op.ID)
			if err != nil {
				return bad(err)
			}
			op.Before, _ = json.Marshal(old)
		}
		at := time.Now().UTC()
		var next Case
		var audit AuditEvent
		switch req.Type {
		case "create-case":
			if req.Case == nil {
				return bad(ErrInvalid)
			}
			if _, e := s.getCase(op.ID); !errors.Is(e, ErrNotFound) {
				if e == nil {
					return bad(ErrConflict)
				}
				return bad(e)
			}
			items, e := readItems[Case](s.privateItemDir("cases"))
			if e != nil {
				return bad(e)
			}
			if len(items) >= maxItems {
				return bad(ErrCapacity)
			}
			next, audit, err = core.CreateCase(*req.Case, actor, at)
		case "update-case":
			if req.Case == nil {
				return bad(ErrInvalid)
			}
			next, audit, err = core.UpdateCase(old, *req.Case, actor, at)
		case "apply-scenario":
			data, e := safeReadFile(s.definitionDir("scenarios"), req.ScenarioID)
			if e != nil {
				return bad(e)
			}
			var scenario Scenario
			if json.Unmarshal(data, &scenario) != nil || scenario.ID != req.ScenarioID {
				return bad(ErrInvalid)
			}
			next, audit, err = core.ApplyScenario(old, scenario, "application-"+hex.EncodeToString(sha256Sum(id))[:32], actor, at)
		case "record-scenario-step":
			if proof != nil {
				next, audit, err = core.RecordScenarioQueryStep(old, req.ApplicationID, req.StepID, *proof, actor, at)
			} else {
				var run *CheckRun
				if req.StepStatus == "completed" {
					v, e := s.getRun(req.RunID)
					if e != nil {
						return bad(e)
					}
					run = &v
				}
				linked := false
				for _, v := range old.RunRefs {
					if v == req.RunID {
						linked = true
						break
					}
				}
				next, audit, err = core.RecordScenarioStep(old, req.ApplicationID, req.StepID, req.StepStatus, req.RunID, run, linked, actor, at)
			}
		case "append-event":
			if req.Event == nil {
				return bad(ErrInvalid)
			}
			var run *CheckRun
			if req.Event.Type == "link-evidence" {
				v, e := s.getRun(req.Event.Ref)
				if e != nil {
					return bad(e)
				}
				run = &v
			}
			next, audit, err = core.AppendCaseEvent(old, *req.Event, actionEventID(s.projectID, actor, id), run, actor, at)
		}
		if err != nil {
			return bad(err)
		}
		if err := s.compactCaseTimeline(&next); err != nil {
			return bad(err)
		}
		op.After, _ = json.Marshal(next)
		op.Response.Case = &next
		op.Audit = audit
	default:
		return bad(ErrInvalid)
	}
	op.Audit.ID = id
	op.Audit.Project = s.projectID
	op.Audit.Actor = actor
	op.Audit.Action = req.Type
	return op, nil
}

func sha256Sum(s string) []byte { sum := sha256.Sum256([]byte(s)); return sum[:] }
