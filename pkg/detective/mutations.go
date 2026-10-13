package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/google/uuid"
)

// These methods own local flock/file/JSONL mechanics. Core owns the Case
// transition semantics used identically by the hosted repository adapter.
func (s *LocalStore) SaveCase(ctx context.Context, item Case, actor, action string) (Case, error) {
	var saved Case
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "cases", item.ID, ""); err != nil {
			return err
		}
		old, err := s.getCase(item.ID)
		at := time.Now().UTC()
		var audit AuditEvent
		if errors.Is(err, ErrNotFound) {
			if action != "create-case" {
				return ErrConflict
			}
			items, listErr := readItems[Case](s.privateItemDir("cases"))
			if listErr != nil {
				return listErr
			}
			if len(items) >= maxItems {
				return ErrCapacity
			}
			saved, audit, err = core.CreateCase(item, actor, at)
		} else if err == nil {
			if action != "update-case" {
				return ErrConflict
			}
			saved, audit, err = core.UpdateCase(old, item, actor, at)
		}
		if err != nil {
			return err
		}
		if err := s.compactCaseTimeline(&saved); err != nil {
			return err
		}
		if err := writeItem(s.privateItemDir("cases"), saved.ID, saved); err != nil {
			return err
		}
		audit.Project = s.projectID
		return s.auditMutation(ctx, audit)
	})
	return saved, err
}

func (s *LocalStore) ApplyScenario(ctx context.Context, caseID, scenarioID, actor string) (Case, error) {
	if !validSlug(caseID) || !validSlug(scenarioID) {
		return Case{}, ErrInvalid
	}
	var saved Case
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "cases", caseID, ""); err != nil {
			return err
		}
		c, err := s.getCase(caseID)
		if err != nil {
			return err
		}
		data, err := safeReadFile(s.definitionDir("scenarios"), scenarioID)
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var scenario Scenario
		if err := json.Unmarshal(data, &scenario); err != nil {
			return err
		}
		if scenario.ID != scenarioID {
			return ErrInvalid
		}
		var audit AuditEvent
		saved, audit, err = core.ApplyScenario(c, scenario, "application-"+strings.ReplaceAll(uuid.NewString(), "-", ""), actor, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := s.compactCaseTimeline(&saved); err != nil {
			return err
		}
		if err := writeItem(s.privateItemDir("cases"), caseID, saved); err != nil {
			return err
		}
		audit.Project = s.projectID
		return s.auditMutation(ctx, audit)
	})
	return saved, err
}

func (s *LocalStore) RecordScenarioStep(ctx context.Context, caseID, applicationID, stepID, status, runID, actor string) (Case, error) {
	if !validSlug(caseID) {
		return Case{}, ErrInvalid
	}
	var saved Case
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "cases", caseID, ""); err != nil {
			return err
		}
		c, err := s.getCase(caseID)
		if err != nil {
			return err
		}
		var run *CheckRun
		if status == "completed" {
			loaded, err := s.getRun(runID)
			if err != nil {
				return err
			}
			if err := s.authorizeCaseRunLink(c, loaded); err != nil {
				return err
			}
			run = &loaded
		}
		linked := false
		for _, ref := range c.RunRefs {
			if ref == runID {
				linked = true
				break
			}
		}
		var audit AuditEvent
		saved, audit, err = core.RecordScenarioStep(c, applicationID, stepID, status, runID, run, linked, actor, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := s.compactCaseTimeline(&saved); err != nil {
			return err
		}
		if err := writeItem(s.privateItemDir("cases"), caseID, saved); err != nil {
			return err
		}
		audit.Project = s.projectID
		return s.auditMutation(ctx, audit)
	})
	return saved, err
}

// RecordScenarioQueryStep attaches an authorized ordinary saved-query receipt
// to manual guidance without creating a synthetic Check or positive assertion.
func (s *LocalStore) RecordScenarioQueryStep(ctx context.Context, caseID, applicationID, stepID string, proof core.ScenarioQueryProof, actor string) (Case, error) {
	if !validSlug(caseID) || proof.Execution.ProjectID != s.projectID {
		return Case{}, ErrInvalid
	}
	var saved Case
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "cases", caseID, ""); err != nil {
			return err
		}
		c, err := s.getCase(caseID)
		if err != nil {
			return err
		}
		var audit AuditEvent
		saved, audit, err = core.RecordScenarioQueryStep(c, applicationID, stepID, proof, actor, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := s.compactCaseTimeline(&saved); err != nil {
			return err
		}
		if err := writeItem(s.privateItemDir("cases"), caseID, saved); err != nil {
			return err
		}
		audit.Project = s.projectID
		return s.auditMutation(ctx, audit)
	})
	return saved, err
}

func (s *LocalStore) AppendCaseEvent(ctx context.Context, id string, event CaseEvent, actor string) (Case, error) {
	return s.appendCaseEvent(ctx, id, event, actor, false)
}

func (s *LocalStore) appendServerCaseRun(ctx context.Context, id, runID, actor string) (Case, error) {
	return s.appendCaseEvent(ctx, id, CaseEvent{Type: "check-run", Ref: runID}, actor, true)
}

func (s *LocalStore) appendCaseEvent(ctx context.Context, id string, event CaseEvent, actor string, server bool) (Case, error) {
	if !validSlug(id) {
		return Case{}, ErrInvalid
	}
	var saved Case
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "cases", id, ""); err != nil {
			return err
		}
		c, err := s.getCase(id)
		if err != nil {
			return err
		}
		var run *CheckRun
		if event.Type == "link-evidence" || server {
			loaded, err := s.getRun(event.Ref)
			if err != nil {
				return err
			}
			if err := s.authorizeCaseRunLink(c, loaded); err != nil {
				return err
			}
			run = &loaded
		}
		eventID := "event-" + strings.ReplaceAll(uuid.NewString(), "-", "")
		at := time.Now().UTC()
		var audit AuditEvent
		if server {
			if event.Type != "check-run" || run == nil {
				return ErrInvalid
			}
			saved, audit, err = core.AttachRun(c, *run, eventID, actor, at)
		} else {
			saved, audit, err = core.AppendCaseEvent(c, event, eventID, run, actor, at)
		}
		if err != nil {
			return err
		}
		if err := s.compactCaseTimeline(&saved); err != nil {
			return err
		}
		if err := writeItem(s.privateItemDir("cases"), id, saved); err != nil {
			return err
		}
		audit.Project = s.projectID
		return s.auditMutation(ctx, audit)
	})
	return saved, err
}

func (s *LocalStore) getRun(id string) (CheckRun, error) {
	if !validSlug(id) {
		return CheckRun{}, ErrInvalid
	}
	data, err := safeReadFile(s.privateItemDir("runs"), id)
	if errors.Is(err, os.ErrNotExist) {
		return CheckRun{}, ErrNotFound
	}
	if err != nil {
		return CheckRun{}, err
	}
	var run CheckRun
	if err := json.Unmarshal(data, &run); err != nil {
		return CheckRun{}, err
	}
	if run.ID != id {
		return CheckRun{}, ErrInvalid
	}
	return run, nil
}
