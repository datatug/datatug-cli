package endpoints

import (
	"context"
	"errors"

	"github.com/datatug/datatug-cli/pkg/api"
)

// queryCaptureStore is the narrow slice of datatug-core's
// datatug.RevisionedQueriesStore (Phase 2 task 2 storage) that
// queries/capture needs: one conditional, atomic write of a query's
// "<id>.query.json" / "<id>.query.dtql" pair.
//
// It is declared here in this package's own terms to keep the endpoint
// coupled to only the released revisioned-store behavior it needs.
type queryCaptureStore interface {
	// PutQuery persists record under condition, with
	// RevisionedQueriesStore.PutQuery's semantics: it returns only once the
	// pair is durably stored, with the new revision, or it returns an error
	// having changed nothing - a *captureStoreError for a refused location,
	// an incomplete stored pair, a failed condition or refused content.
	PutQuery(ctx context.Context, record capturedRecord, condition captureWriteCondition) (storedCapture, error)
}

// captureWriteCondition is datatug.QueryWriteCondition: exactly one of
// IfNoneMatch (create only when nothing is stored at the location) or
// IfMatch (replace only this revision).
type captureWriteCondition struct {
	IfNoneMatch bool
	IfMatch     string
}

// capturedRecord is what a capture persists: the query as submitted plus
// the provenance the server itself recorded.
type capturedRecord struct {
	Query       capturedQuery
	Author      string
	Environment string
	Collection  string
}

// storedCapture is datatug.StoredQuery: the persisted record and the
// revision of its exact bytes.
type storedCapture struct {
	Record   capturedRecord
	Revision string
}

// captureStoreErrorKind classifies a store refusal - one per typed error
// the revisioned store returns, plus its content validation.
type captureStoreErrorKind int

const (
	// captureErrLocation is datatug.InvalidQueryLocationError.
	captureErrLocation captureStoreErrorKind = iota + 1
	// captureErrIncomplete is datatug.IncompleteQueryRecordError.
	captureErrIncomplete
	// captureErrConflict is datatug.QueryRevisionConflictError.
	captureErrConflict
	// captureErrContent is a strongo/validation bad-request error from the
	// store's own QueryDef validation.
	captureErrContent
)

// captureStoreError is a store refusal translated into this package's
// terms. Field names the request field it concerns ("query.folderPath"),
// when the store can tell.
type captureStoreError struct {
	Kind   captureStoreErrorKind
	Field  string
	Reason string
}

func (e *captureStoreError) Error() string {
	return "the query store refused the write: " + e.Reason
}

// errCaptureStoreUnavailable is what adaptRevisionedStore returns when the
// project store offers no revisioned query store.
var errCaptureStoreUnavailable = errors.New("this agent build has no revisioned query store, so queries/capture cannot save; " +
	"it needs a datatug-core release with RevisionedQueriesStore")

// captureStoreFor resolves projectID's capture store. It is a variable so
// tests can install a fake.
var captureStoreFor = func(projectID string) (queryCaptureStore, error) {
	projStore, err := api.ProjectStoreFor(projectID)
	if err != nil {
		return nil, err
	}
	return adaptRevisionedStore(projStore)
}
