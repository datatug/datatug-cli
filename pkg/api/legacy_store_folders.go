//go:build datatug_query_capture

package api

import (
	"cmp"
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/validation"
)

// This build compiles only against a datatug-core whose filestore resolves
// a query's folder (the Phase 2 task 2 storage: SaveQuery honours
// FolderPath, and every write refuses a symlinked or non-directory folder).
// The blank declaration makes the build tag fail to compile against a core
// without that store, so the tag can never claim a capability the store
// lacks.
var _ datatug.RevisionedQueriesStore

// legacyStoreResolvesFolders reports whether the project store the legacy
// query routes write through puts a query in the folder its request names.
// See legacy_store_default.go.
const legacyStoreResolvesFolders = true

// storeLocationRefusal reports the store's refusal of a query location
// (datatug.InvalidQueryLocationError: a symlinked, non-directory or
// unreadable folder found on disk) as the fixed message a 400 may carry,
// naming the offending segment only - never the store's reason text,
// which can hold an absolute server path and OS error text.
func storeLocationRefusal(err error) (message string, ok bool) {
	var location *datatug.InvalidQueryLocationError
	if !errors.As(err, &location) {
		return "", false
	}
	return querywrite.LocationMessage(location.FolderPath, location.ID, location.Reason), true
}

// refuseClientCapture refuses a create_query that carries a capture block:
// provenance is the server's record of a queries/capture save, never a
// client's claim.
func refuseClientCapture(mode legacyWriteMode, query *datatug.QueryDefWithFolderPath) error {
	if mode == legacyCreate && query.Capture != nil {
		return clientCaptureRefusal("create_query must not carry it")
	}
	return nil
}

// clientCaptureRefusal is the 400 for a capture block a legacy write may
// not carry.
func clientCaptureRefusal(detail string) error {
	return validation.NewBadRequestError(validation.NewErrBadRecordFieldValue("query.capture",
		"is set only by queries/capture, which records it on the server; "+detail))
}

// errLegacyStoreNotRevisioned is a legacy write through a project store
// with no conditional write: without one the stored capture cannot be kept
// or compared without a race, so the write is refused rather than risk
// dropping or overwriting provenance. filestore, the only store `datatug
// serve` builds, is revisioned in this build.
var errLegacyStoreNotRevisioned = errors.New("the project store has no revisioned query store, so a legacy query write, which must keep the stored capture provenance, is refused")

// maxLegacyWriteAttempts bounds how often writeRevisionedLegacyQuery
// re-reads and retries after another write committed to the same query
// between its read and its own conditional write.
const maxLegacyWriteAttempts = 5

// writeLegacyQuery writes query, the legacy routes' create-or-replace,
// through the store's conditional write so its capture provenance is
// decided against exactly the query the write replaces (see
// writeRevisionedLegacyQuery).
func writeLegacyQuery(ctx context.Context, store datatug.ProjectStore, queryID string, query *datatug.QueryDefWithFolderPath) error {
	revisioned, ok := store.(datatug.RevisionedQueriesStore)
	if !ok {
		return errLegacyStoreNotRevisioned
	}
	return writeRevisionedLegacyQuery(ctx, revisioned, queryID, query)
}

// writeRevisionedLegacyQuery replaces or creates the query at queryID, its
// canonical folder-qualified id, keeping the stored query's capture
// provenance. Each attempt reads the stored query and its revision, decides
// the capture, and writes with PutQuery conditioned on that revision
// (IfMatch), or on nothing being stored (IfNoneMatch), so the store commits
// the write only if the query it replaces is still the one the decision was
// made against. A write that committed in between fails the condition, and
// the attempt starts over from a fresh read; after maxLegacyWriteAttempts
// the write gives up with ErrLegacyQueryWriteConflict, having written
// nothing.
//
// The decision: a capture block in the request (update_query's; the
// create_query route refused one already) must equal the stored query's
// capture (sameCapture), or the write is refused with a 400 naming
// query.capture. The stored capture - nil when the stored query has none or
// nothing is stored - then replaces the request's, so no legacy write sets,
// changes or clears provenance, and the query must still validate with it
// (its bindings name the query's parameters).
func writeRevisionedLegacyQuery(ctx context.Context, store datatug.RevisionedQueriesStore, queryID string, query *datatug.QueryDefWithFolderPath) error {
	requested := query.Capture
	for range maxLegacyWriteAttempts {
		stored, condition, err := storedCaptureOf(ctx, store, queryID)
		if err != nil {
			return err
		}
		if requested != nil && !sameCapture(requested, stored) {
			return clientCaptureRefusal("update_query must omit it or send the stored query's own capture unchanged")
		}
		query.Capture = stored
		if err := query.QueryDef.Validate(); err != nil {
			return validation.NewBadRequestError(err)
		}
		_, err = store.PutQuery(ctx, query, condition)
		var conflict *datatug.QueryRevisionConflictError
		if !errors.As(err, &conflict) {
			return err
		}
	}
	return ErrLegacyQueryWriteConflict
}

// storedCaptureOf reads the query stored at queryID and returns its
// capture provenance together with the write condition that replaces
// exactly what was read: IfMatch its revision - an incomplete stored pair
// included, as its error carries both - or IfNoneMatch when nothing is
// stored there.
func storedCaptureOf(ctx context.Context, store datatug.RevisionedQueriesStore, queryID string) (*datatug.QueryCapture, datatug.QueryWriteCondition, error) {
	current, err := store.LoadQueryRevision(ctx, queryID)
	var incomplete *datatug.IncompleteQueryRecordError
	switch {
	case err == nil:
		return current.Query.Capture, datatug.QueryWriteCondition{IfMatch: current.Revision}, nil
	case errors.As(err, &incomplete):
		return incomplete.Query.Capture, datatug.QueryWriteCondition{IfMatch: incomplete.Revision}, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, datatug.QueryWriteCondition{IfNoneMatch: true}, nil
	default:
		return nil, datatug.QueryWriteCondition{}, err
	}
}

// sameCapture reports whether a and b record the same provenance: every
// field equal, with the bindings compared in any order and an omitted
// binding list equal to an empty one - so a capture that went out in a
// get_query response and came back in an update_query request is the same
// capture. Nil equals only nil.
func sameCapture(a, b *datatug.QueryCapture) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(normalizedCapture(*a), normalizedCapture(*b))
}

// normalizedCapture is c with its bindings sorted, or nil when it has none.
func normalizedCapture(c datatug.QueryCapture) datatug.QueryCapture {
	if len(c.Bindings) == 0 {
		c.Bindings = nil
		return c
	}
	c.Bindings = slices.Clone(c.Bindings)
	slices.SortFunc(c.Bindings, func(x, y datatug.QueryCaptureBinding) int {
		return cmp.Or(strings.Compare(x.ParameterID, y.ParameterID), strings.Compare(x.Origin, y.Origin))
	})
	return c
}
