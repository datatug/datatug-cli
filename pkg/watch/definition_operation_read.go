package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dal-go/dalgo/dal"
)

// putDefinitionOperationDigest is the native PutDefinition request identity.
func putDefinitionOperationDigest(d Definition, version int64) (string, error) {
	d.SourceRevision = ""
	raw, err := json.Marshal(d)
	return stable(string(raw), fmt.Sprint(version)), err
}

// ReadPutDefinitionOperationInTransaction confirms an accepted save without
// executing it. A missing receipt does not prove a save never occurred.
// It requires the original pre-save definition, actor, operation
// ID and expected version. Missing receipts return found=false and never grant
// permission to retry a save that has lost human approval.
//
// As for ReadDefinitionInTransaction, the trusted host supplies this Store's
// underlying transaction before physical writes and a server-authenticated actor.
// Current project authorization and current/saved visibility are rechecked.
// Callers separately recheck source admission before using the receipt for
// another operation. This method never writes or runs an admission provider.
func (s *Store) ReadPutDefinitionOperationInTransaction(ctx context.Context, tx dal.ReadTransaction, actor Actor, requested Definition, operationID string, expectedVersion int64) (Definition, bool, error) {
	if s == nil || s.cloud == nil || tx == nil || operationID == "" || len(operationID) > 128 || expectedVersion < 0 {
		return Definition{}, false, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Definition{}, false, err
	}
	if err := s.actor(actor); err != nil {
		return Definition{}, false, err
	}
	if requested.Scope != actor.Scope || validateDefinition(requested) != nil {
		return Definition{}, false, ErrInvalid
	}
	digest, err := putDefinitionOperationDigest(requested, expectedVersion)
	if err != nil {
		return Definition{}, false, ErrInvalid
	}
	if err = s.cloud.options.Authorize(ctx, tx); err != nil {
		return Definition{}, false, err
	}
	session := newFirestoreSession(s.cloud, tx)
	previous, err := read[operation](ctx, session, operations, actor.Scope, stable(actor.ID, operationID))
	if errors.Is(err, ErrNotFound) {
		return Definition{}, false, nil
	}
	if err != nil {
		return Definition{}, false, err
	}
	if previous.Digest != digest {
		return Definition{}, false, ErrConflict
	}
	accepted := previous.Definition
	if accepted.ID != requested.ID || accepted.Scope != actor.Scope || accepted.Version < 1 || validateDefinition(accepted) != nil {
		return Definition{}, false, ErrInvalid
	}
	current, err := read[Definition](ctx, session, definitions, actor.Scope, accepted.ID)
	if err != nil {
		return Definition{}, false, err
	}
	if !visible(actor, current) || !visible(actor, accepted) {
		return Definition{}, false, ErrNotFound
	}
	return accepted, true, nil
}
