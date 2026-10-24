package watch

import (
	"context"

	"github.com/dal-go/dalgo/dal"
)

// ReadDefinitionInTransaction reads a visible native Watch in a trusted host's
// existing transaction. The host must supply a transaction from this Store's
// underlying database, before physical writes, and a server-authenticated actor.
// DALGO transactions expose no portable database identity witness; this method
// must never be used with a transaction selected by a client.
//
// Current Store authorization and scope/visibility rules are rechecked in the
// supplied snapshot. Callers must separately validate current source admission
// before reusing a definition for an operation. This method never writes.
func (s *Store) ReadDefinitionInTransaction(ctx context.Context, tx dal.ReadTransaction, actor Actor, id string) (Definition, error) {
	if s == nil || s.cloud == nil || tx == nil || id == "" || len(id) > 128 {
		return Definition{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Definition{}, err
	}
	if err := s.actor(actor); err != nil {
		return Definition{}, err
	}
	if err := s.cloud.options.Authorize(ctx, tx); err != nil {
		return Definition{}, err
	}
	session := newFirestoreSession(s.cloud, tx)
	definition, err := read[Definition](ctx, session, definitions, actor.Scope, id)
	if err != nil {
		return Definition{}, err
	}
	if !visible(actor, definition) {
		return Definition{}, ErrNotFound
	}
	return definition, nil
}
