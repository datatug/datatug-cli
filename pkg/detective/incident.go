package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
)

// IncidentOperation is a private, payload-bound local admission record. It is
// written before touching Incidentius so an interrupted cross-store create can
// resume with the same mutation ID and cannot be replayed with new content.
type IncidentOperation struct {
	Receipt core.OperationReceipt `json:"receipt"`
	// CreatedRef records the external create before attempting the Case CAS. A
	// stale Case version can then be recovered with a separate authorized link.
	CreatedRef string                `json:"createdRef,omitempty"`
	Done       bool                  `json:"done"`
	Case       *Case                 `json:"case,omitempty"`
	Context    map[string]InputValue `json:"context,omitempty"`
	Audit      *AuditEvent           `json:"audit,omitempty"`
	Before     int64                 `json:"before"`
}

func (s *LocalStore) RecordIncidentCreated(ctx context.Context, id string, binding core.OperationBinding, ref string) error {
	if !validSlug(id) || ref == "" || binding.Action != "incident:create" {
		return ErrInvalid
	}
	return s.withLock(ctx, func() error {
		data, err := safeReadFile(s.privateItemDir("incident-operations"), id)
		if err != nil {
			return err
		}
		var intent IncidentOperation
		if json.Unmarshal(data, &intent) != nil || core.CheckOperationReplay(intent.Receipt, binding) != nil {
			return ErrConflict
		}
		if intent.CreatedRef != "" {
			if intent.CreatedRef != ref {
				return ErrConflict
			}
			return nil
		}
		intent.CreatedRef = ref
		return writeItem(s.privateItemDir("incident-operations"), id, intent)
	})
}

func (s *LocalStore) GetCase(ctx context.Context, id string) (Case, error) {
	var result Case
	err := s.withLock(ctx, func() error {
		var err error
		result, err = s.getCase(id)
		return err
	})
	return result, err
}

func (s *LocalStore) BeginIncidentOperation(ctx context.Context, id string, binding core.OperationBinding, caseID string, ifMatch int64, create bool) (IncidentOperation, bool, error) {
	if !validSlug(id) || !validSlug(caseID) || ifMatch < 0 {
		return IncidentOperation{}, false, ErrInvalid
	}
	var intent IncidentOperation
	created := false
	err := s.withLock(ctx, func() error {
		if err := s.reconcilePendingActions(ctx, "cases", caseID, id); err != nil {
			return err
		}
		data, err := safeReadFile(s.privateItemDir("incident-operations"), id)
		if err == nil {
			if json.Unmarshal(data, &intent) != nil || core.CheckOperationReplay(intent.Receipt, binding) != nil {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		items, err := readItems[IncidentOperation](s.privateItemDir("incident-operations"))
		if err != nil {
			return err
		}
		if len(items) >= maxItems {
			return ErrCapacity
		}
		current, err := s.getCase(caseID)
		if create {
			if !errors.Is(err, ErrNotFound) || ifMatch != 0 {
				return ErrConflict
			}
		} else if err != nil || current.Version != ifMatch {
			return ErrConflict
		}
		receipt, err := core.NewOperationReceipt(id, binding, 0, time.Now().UTC())
		if err != nil {
			return err
		}
		intent = IncidentOperation{Receipt: receipt}
		if binding.Action == "incident:create" {
			intent.Context = current.Context
		}
		if err := writeItem(s.privateItemDir("incident-operations"), id, intent); err != nil {
			return err
		}
		created = true
		return nil
	})
	return intent, created, err
}

// CompleteIncidentOperation applies the Core Case transition under the local
// private lock, then stores a replayable response. If a prior write succeeded
// but the final receipt did not, its accepted operation can resume safely.
func (s *LocalStore) CompleteIncidentOperation(ctx context.Context, id string, binding core.OperationBinding, caseID, incidentRef, actor string, ifMatch int64, create *Case) (Case, error) {
	if !validSlug(id) || !validSlug(caseID) || incidentRef == "" {
		return Case{}, ErrInvalid
	}
	var saved Case
	err := s.withLock(ctx, func() error {
		data, err := safeReadFile(s.privateItemDir("incident-operations"), id)
		if err != nil {
			return err
		}
		var intent IncidentOperation
		if json.Unmarshal(data, &intent) != nil || core.CheckOperationReplay(intent.Receipt, binding) != nil {
			return ErrConflict
		}
		if intent.Done {
			if intent.Case == nil {
				return ErrInvalid
			}
			saved = *intent.Case
			return nil
		}
		if binding.Action == "incident:create" && intent.CreatedRef != incidentRef {
			return ErrConflict
		}
		if intent.Case != nil {
			if intent.Case.ID != caseID || !containsIncidentRef(intent.Case.IncidentRefs, incidentRef) {
				return ErrConflict
			}
			saved = *intent.Case
			return s.finishIncidentOperation(ctx, id, &intent)
		}
		old, err := s.getCase(caseID)
		at := time.Now().UTC()
		var audit AuditEvent
		if create != nil {
			if errors.Is(err, ErrNotFound) && ifMatch == 0 {
				candidate := *create
				candidate.ID = caseID
				candidate.IncidentRefs = []string{incidentRef}
				saved, audit, err = core.CreateCase(candidate, actor, at)
			} else {
				return ErrConflict
			}
		} else {
			if err != nil {
				return err
			}
			if old.Version != ifMatch {
				return ErrConflict
			}
			candidate := old
			candidate.IncidentRefs = append(append([]string(nil), old.IncidentRefs...), incidentRef)
			saved, audit, err = core.UpdateCase(old, candidate, actor, at)
		}
		if err != nil {
			return err
		}
		if err := s.compactCaseTimeline(&saved); err != nil {
			return err
		}
		audit.Action, audit.Project, audit.CaseID = binding.Action, s.projectID, caseID
		intent.Receipt.ResourceVersion = saved.Version
		intent.Case = &saved
		intent.Audit = &audit
		intent.Before = ifMatch
		if err := writeItem(s.privateItemDir("incident-operations"), id, intent); err != nil {
			return err
		}
		return s.finishIncidentOperation(ctx, id, &intent)
	})
	return saved, err
}

func (s *LocalStore) finishIncidentOperation(ctx context.Context, id string, intent *IncidentOperation) error {
	if intent.Case == nil || intent.Audit == nil {
		return ErrInvalid
	}
	current, err := s.getCase(intent.Case.ID)
	if errors.Is(err, ErrNotFound) && intent.Before == 0 {
		current = Case{}
	} else if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, *intent.Case) {
		if current.ID != "" && current.Version != intent.Before || current.ID == "" && intent.Before != 0 {
			return ErrConflict
		}
		if err := writeItem(s.privateItemDir("cases"), intent.Case.ID, intent.Case); err != nil {
			return err
		}
	}
	recorded, err := s.auditRecorded(*intent.Audit)
	if err != nil {
		return err
	}
	if !recorded {
		if err := s.auditMutation(ctx, *intent.Audit); err != nil {
			return err
		}
	}
	intent.Done = true
	if err := writeItem(s.privateItemDir("incident-operations"), id, intent); err != nil {
		return ErrOutcomeUncertain
	}
	return nil
}

func containsIncidentRef(refs []string, wanted string) bool {
	for _, ref := range refs {
		if ref == wanted {
			return true
		}
	}
	return false
}
