package watch

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/dal-go/dalgo/dal"
)

// CloudRetainedWrite represents a project-owned change. Nil Before is an insert;
// nil After is a physical removal. Both are immutable copies of the envelopes,
// not a reference graph or a claim that retained dependencies can be purged.
type CloudRetainedWrite struct {
	Before *CloudRetainedDocument
	After  *CloudRetainedDocument
}

// CloudRetainedWriteAdmission must validate typed dependencies and read the
// project fence in the supplied transaction. It must not write during prepare.
// For a nonempty batch it must return a nonnil write-only persistence closure,
// with conservative row/byte costs for every additional write. These are charged
// against the existing transaction budget before any physical write is issued.
// Errors from either stage abort the entire source transaction. Domain-specific
// authorization and runner/recipient admission remain independently required.
type CloudRetainedWritePlan struct {
	Persist    func() error
	WriteRows  int
	WriteBytes int
}
type CloudRetainedWriteAdmission func(context.Context, dal.ReadwriteTransaction, string, string, []CloudRetainedWrite) (CloudRetainedWritePlan, error)

func (t *firestoreSession) retainedDocument(row cloudRow) (*CloudRetainedDocument, error) {
	if row.key == nil || !row.exists {
		return nil, ErrInvalid
	}
	if err := t.validateRow(row.key.Collection(), row.data); err != nil {
		return nil, err
	}
	id, idOK := row.key.ID.(string)
	env, envOK := row.data["environment"].(string)
	watchID, watchOK := row.data["watch"].(string)
	owner, ownerOK := row.data["owner"].(string)
	payload := row.data["payload"].(string)
	if !idOK || !nonceHashPattern.MatchString(id) || !envOK || !watchOK || !ownerOK || len(payload) > 128<<10 || !json.Valid([]byte(payload)) {
		return nil, ErrInvalid
	}
	return &CloudRetainedDocument{Collection: row.key.Collection(), ID: id, Environment: env, WatchID: watchID, Owner: owner, Payload: append(json.RawMessage(nil), payload...)}, nil
}

func (t *firestoreSession) prepareRetainedAdmission(ctx context.Context) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	admit := t.database.options.AdmitRetainedWrite
	if admit == nil || len(t.pending) == 0 {
		return nil, nil
	}
	collections, err := projectRetainedCollections()
	if err != nil {
		return nil, err
	}
	projectTable := map[string]bool{}
	for _, table := range collections {
		projectTable[table] = true
	}
	ids := make([]string, 0, len(t.pending))
	for id := range t.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	changes := make([]CloudRetainedWrite, 0, len(ids))
	for _, id := range ids {
		before, loaded := t.original[id]
		if !loaded || before.key == nil {
			return nil, ErrInvalid
		}
		if !projectTable[before.key.Collection()] {
			continue
		}
		change := CloudRetainedWrite{}
		if before.exists {
			change.Before, err = t.retainedDocument(before)
			if err != nil {
				return nil, err
			}
		}
		if after := t.pending[id]; after != nil {
			change.After, err = t.retainedDocument(*after)
			if err != nil {
				return nil, err
			}
		}
		if change.Before != nil || change.After != nil {
			changes = append(changes, change)
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	plan, err := admit(ctx, t.ReadwriteTransaction, t.database.options.Config.Namespace, t.database.options.ProjectID, changes)
	if err != nil {
		return nil, err
	}
	if plan.Persist == nil || plan.WriteRows < 0 || plan.WriteBytes < 0 {
		return nil, ErrInvalid
	}
	if plan.WriteRows > cloudWriteRows || plan.WriteBytes > cloudWriteBytes {
		return nil, ErrCapacity
	}
	t.admissionRows, t.admissionBytes = plan.WriteRows, plan.WriteBytes
	if err := t.budget(); err != nil {
		return nil, err
	}
	return plan.Persist, ctx.Err()
}
