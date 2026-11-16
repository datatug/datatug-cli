package detective

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	core "github.com/datatug/datatug-core/pkg/detective"
)

var visibilityRestoreCase = core.RestoreCase

var ErrVisibilityRestoreConflict = errors.New("native Case restore was superseded")

// RestoreVisibilityCase reverses a legacy native archive using Core's owning
// transition. Its private operation journal survives a crash between the Case
// image, audit and receipt; replay never overwrites a later Case revision.
func (s *LocalStore) RestoreVisibilityCase(ctx context.Context, id, actor, operationID string, expectedVersion int64) (result Case, err error) {
	if actor == "" || operationID == "" || len(operationID) > 128 || expectedVersion < 1 {
		return result, ErrInvalid
	}
	binding, err := core.BindOperation(s.projectID, actor, "restore-case", id, struct{ Version int64 }{expectedVersion})
	if err != nil {
		return result, err
	}
	err = s.withLock(ctx, func() error {
		var op actionOperation
		body, e := actionSafeReadFile(s.privateItemDir("action-operations"), operationID)
		if e == nil {
			if json.Unmarshal(body, &op) != nil || op.Kind != "cases" || op.ID != id || op.Response.Case == nil || !op.Produced {
				return ErrOutcomeUncertain
			}
			if core.CheckOperationReplay(op.Receipt, binding) != nil {
				return ErrVisibilityRestoreConflict
			}
			if op.Done {
				result = *op.Response.Case
				return nil
			}
		} else if errors.Is(e, os.ErrNotExist) {
			before, e := actionSafeReadFile(s.privateItemDir("cases"), id)
			if errors.Is(e, os.ErrNotExist) {
				return ErrVisibilityRestoreConflict
			}
			if e != nil {
				return e
			}
			var old Case
			if json.Unmarshal(before, &old) != nil {
				return ErrOutcomeUncertain
			}
			if old.ID != id || old.Version != expectedVersion || old.WorkItemID != "" {
				return ErrVisibilityRestoreConflict
			}
			next, audit, e := visibilityRestoreCase(old, actor, time.Now().UTC())
			if errors.Is(e, core.ErrConflict) || errors.Is(e, core.ErrInvalid) {
				return ErrVisibilityRestoreConflict
			}
			if e != nil {
				return e
			}
			after, e := stateMarshal(next)
			if e != nil {
				return e
			}
			receipt, e := actionNewOperationReceipt(operationID, binding, next.Version, time.Now().UTC())
			if e != nil {
				return e
			}
			audit.ID = operationID
			audit.Project = s.projectID
			op = actionOperation{Receipt: receipt, Kind: "cases", ID: id, Before: before, After: after, Audit: audit, Produced: true, Response: ActionResponse{Case: &next}}
			if e = writeDurableItem(s.privateItemDir("action-operations"), operationID, op); e != nil {
				return e
			}
		} else {
			return e
		}
		if e = s.finishActionFile(s.privateItemDir("cases"), id, op.Before, json.RawMessage(op.After)); e != nil {
			if errors.Is(e, ErrConflict) {
				return ErrVisibilityRestoreConflict
			}
			return e
		}
		if e = writeItemSyncDir(s.privateItemDir("cases")); e != nil {
			return e
		}
		if e = s.appendAuditOnce(ctx, op.Audit); e != nil {
			return e
		}
		op.Done = true
		if e = writeDurableItem(s.privateItemDir("action-operations"), operationID, op); e != nil {
			return ErrOutcomeUncertain
		}
		result = *op.Response.Case
		return nil
	})
	return result, err
}
