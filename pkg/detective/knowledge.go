package detective

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	knowledge "github.com/datatug/datatug-core/pkg/detectiveknowledge"
)

type knowledgeOperation struct {
	Receipt  core.OperationReceipt `json:"receipt"`
	Response knowledge.Response    `json:"response"`
	Previous *knowledge.Proposal   `json:"previous,omitempty"`
	Audit    AuditEvent            `json:"audit"`
	Done     bool                  `json:"done"`
}

// KnowledgePage reads only this project's private proposals and returns a
// bounded, stable ID page. No proposal or handoff enters the project Git tree.
func (s *LocalStore) KnowledgePage(ctx context.Context, after string) (knowledge.Page, error) {
	page := knowledge.Page{Proposals: []knowledge.Proposal{}}
	if after != "" && !validSlug(after) {
		return page, ErrInvalid
	}
	err := s.withLock(ctx, func() error {
		items, err := readItems[knowledge.Proposal](s.privateItemDir("knowledge-proposals"))
		if err != nil {
			return err
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		for _, item := range items {
			if item.ID <= after {
				continue
			}
			if len(page.Proposals) == 50 {
				page.HasMore = true
				page.NextCursor = page.Proposals[49].ID
				break
			}
			page.Proposals = append(page.Proposals, item)
		}
		return nil
	})
	return page, err
}

// KnowledgeAction uses Core's portable transitions and reference validation.
// The local lock serializes the proposal, metadata-only audit and replay
// receipt. The accepted operation ID is bound to project, actor and payload.
func (s *LocalStore) KnowledgeAction(ctx context.Context, request knowledge.Action, actor string) (knowledge.Response, error) {
	var response knowledge.Response
	if request.Project != s.projectID || request.Storage != "local" || request.SpaceID != "" || !validSlug(request.OperationID) || actor == "" || knowledge.ValidateActionShape(request) != nil {
		return response, ErrInvalid
	}
	id := request.ProposalID
	if request.Proposal != nil {
		id = request.Proposal.ID
	}
	binding, err := core.BindOperation(s.projectID, actor, "knowledge:"+request.Type, id, request)
	if err != nil {
		return response, ErrInvalid
	}
	err = s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "knowledge-proposals", id, request.OperationID); err != nil {
			return err
		}
		operationData, err := safeReadFile(s.privateItemDir("knowledge-operations"), request.OperationID)
		if err == nil {
			var operation knowledgeOperation
			if json.Unmarshal(operationData, &operation) != nil || core.CheckOperationReplay(operation.Receipt, binding) != nil || operation.Response.Proposal == nil {
				return ErrConflict
			}
			facts, err := s.knowledgeFacts(*operation.Response.Proposal)
			if err != nil {
				return err
			}
			if knowledge.ValidateReferences(*operation.Response.Proposal, facts) != nil {
				return ErrInvalid
			}
			if !operation.Done {
				if err := s.finishKnowledgeOperation(ctx, request.OperationID, &operation); err != nil {
					return err
				}
			}
			response = operation.Response
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := safeReadFile(s.privateItemDir("knowledge-proposals"), id)
		var old *knowledge.Proposal
		if err == nil {
			old = new(knowledge.Proposal)
			if json.Unmarshal(data, old) != nil || old.ID != id {
				return ErrInvalid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		at := time.Now().UTC()
		var next knowledge.Proposal
		var handoff knowledge.Handoff
		switch request.Type {
		case "save-draft":
			if old == nil {
				items, err := readItems[knowledge.Proposal](s.privateItemDir("knowledge-proposals"))
				if err != nil {
					return err
				}
				if len(items) >= maxItems {
					return ErrCapacity
				}
			}
			next, err = knowledge.SaveDraft(old, *request.Proposal, request.IfMatch, actor, at)
		case "review", "reject":
			if old == nil {
				return ErrNotFound
			}
			next, err = knowledge.Review(*old, request.IfMatch, request.Type, request.ReviewNote, actor, at)
		case "export":
			if old == nil {
				return ErrNotFound
			}
			next, handoff, err = knowledge.PrepareHandoff(*old, request.IfMatch, *request.Target, actor, request.OperationID, at)
		}
		if err != nil {
			return knowledgeError(err)
		}
		facts, err := s.knowledgeFacts(next)
		if err != nil {
			return err
		}
		if err := knowledge.ValidateReferences(next, facts); err != nil {
			return ErrInvalid
		}
		receipt, err := core.NewOperationReceipt(request.OperationID, binding, next.Version, at)
		if err != nil {
			return ErrInvalid
		}
		response = knowledge.Response{Proposal: &next, Receipt: request.OperationID}
		if request.Type == "export" {
			response.Export = &handoff
		}
		operation := knowledgeOperation{Receipt: receipt, Response: response, Previous: old, Audit: AuditEvent{ID: request.OperationID, At: at, Actor: actor, Project: s.projectID, Action: "knowledge:" + request.Type, CaseID: next.CaseID}}
		if err := writeItem(s.privateItemDir("knowledge-operations"), request.OperationID, operation); err != nil {
			return err
		}
		return s.finishKnowledgeOperation(ctx, request.OperationID, &operation)
	})
	return response, err
}

// An accepted receipt is durable before the proposal changes. Replay finishes
// an interrupted write and avoids duplicate JSONL audit entries.
func (s *LocalStore) finishKnowledgeOperation(ctx context.Context, id string, operation *knowledgeOperation) error {
	next := operation.Response.Proposal
	if next == nil {
		return ErrInvalid
	}
	data, err := safeReadFile(s.privateItemDir("knowledge-proposals"), next.ID)
	var current *knowledge.Proposal
	if err == nil {
		current = new(knowledge.Proposal)
		if json.Unmarshal(data, current) != nil {
			return ErrInvalid
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !reflect.DeepEqual(current, next) {
		if !reflect.DeepEqual(current, operation.Previous) {
			return ErrConflict
		}
		if err := writeItem(s.privateItemDir("knowledge-proposals"), next.ID, next); err != nil {
			return err
		}
	}
	recorded, err := s.auditRecorded(operation.Audit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.auditMutation(ctx, operation.Audit); err != nil {
			return err
		}
	}
	operation.Done = true
	if err := writeItem(s.privateItemDir("knowledge-operations"), id, operation); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}

func (s *LocalStore) auditRecorded(wanted AuditEvent) (bool, error) {
	audit, ok := s.audit.(localJSONLAudit)
	if !ok {
		return false, nil
	}
	if err := ensureAbsentOrRegular(audit.path); err != nil {
		return false, err
	}
	file, err := os.Open(audit.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event AuditEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			return false, ErrInvalid
		}
		if event.ID == wanted.ID && event.Action == wanted.Action && event.Project == wanted.Project && event.Actor == wanted.Actor {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func knowledgeError(err error) error {
	switch {
	case errors.Is(err, knowledge.ErrConflict):
		return ErrConflict
	case errors.Is(err, knowledge.ErrCapacity):
		return ErrCapacity
	default:
		return ErrInvalid
	}
}

func (s *LocalStore) knowledgeFacts(proposal knowledge.Proposal) (knowledge.EvidenceFacts, error) {
	c, err := s.getCase(proposal.CaseID)
	if err != nil {
		return knowledge.EvidenceFacts{}, err
	}
	facts := knowledge.EvidenceFacts{CaseID: c.ID}
	seen := make(map[string]bool)
	for _, evidence := range c.Evidence {
		if !validSlug(evidence.ID) || seen[evidence.ID] {
			return knowledge.EvidenceFacts{}, ErrInvalid
		}
		seen[evidence.ID] = true
		facts.EvidenceIDs = append(facts.EvidenceIDs, evidence.ID)
	}
	for _, id := range proposal.EvidenceRefs {
		if seen[id] {
			continue
		}
		runID, ok := strings.CutPrefix(id, "evidence-")
		if !ok || !validSlug(runID) || !containsString(c.RunRefs, runID) {
			return knowledge.EvidenceFacts{}, ErrInvalid
		}
		run, err := s.getRun(runID)
		if err != nil || run.CaseID != c.ID || run.ExecutionStatus != "completed" {
			return knowledge.EvidenceFacts{}, ErrInvalid
		}
		seen[id] = true
		facts.EvidenceIDs = append(facts.EvidenceIDs, id)
	}
	for _, finding := range c.Findings {
		facts.Findings = append(facts.Findings, knowledge.FindingFact{ID: finding.ID, Status: finding.Status, EvidenceRefs: append([]string(nil), finding.EvidenceRefs...)})
	}
	return facts, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
