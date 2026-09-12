package endpoints

import (
	"context"
	"errors"

	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/strongo/validation"
)

// datatug-core v0.28.1 supplies RevisionedQueriesStore and the
// QueryDef.Purpose/Capture contract, so this is the default production adapter.

// adaptRevisionedStore wires datatug-core's revisioned store in: the
// project store queries/capture writes through must implement
// datatug.RevisionedQueriesStore (filestore's does). Anything else fails
// closed with errCaptureStoreUnavailable - there is no fallback to the
// unconditional legacy SaveQuery.
func adaptRevisionedStore(projStore datatug.ProjectStore) (queryCaptureStore, error) {
	revisioned, ok := projStore.(datatug.RevisionedQueriesStore)
	if !ok {
		return nil, errCaptureStoreUnavailable
	}
	return revisionedCaptureStore{store: revisioned}, nil
}

// revisionedCaptureStore adapts datatug.RevisionedQueriesStore to
// queryCaptureStore.
type revisionedCaptureStore struct {
	store datatug.RevisionedQueriesStore
}

// PutQuery maps record onto a QueryDef and makes one RevisionedQueriesStore
// PutQuery call - the store's own atomic, conditional pair write - and
// translates its typed refusals.
func (s revisionedCaptureStore) PutQuery(ctx context.Context, record capturedRecord, condition captureWriteCondition) (storedCapture, error) {
	query := capturedQueryDef(record)
	stored, err := s.store.PutQuery(ctx, &query, datatug.QueryWriteCondition{
		IfNoneMatch: condition.IfNoneMatch,
		IfMatch:     datatug.QueryRevision(condition.IfMatch),
	})
	if err != nil {
		return storedCapture{}, translateRevisionedStoreError(err)
	}
	return storedCapture{Record: capturedRecordOf(stored.Query), Revision: string(stored.Revision)}, nil
}

// capturedQueryDef maps a capture onto the persisted query: a DTQL query
// whose text becomes the "<id>.query.dtql" sidecar, bound to its source by
// Targets [{catalog: source}] - the saved-query target resolution
// api.EligibleTargets applies - with its purpose and QueryCapture
// provenance. A capture holds no default value, fact ID or result row, so
// none can be persisted.
func capturedQueryDef(record capturedRecord) datatug.QueryDefWithFolderPath {
	q := record.Query
	params := make(datatug.Parameters, 0, len(q.Parameters))
	for _, p := range q.Parameters {
		def := datatug.ParameterDef{ID: p.ID, Type: p.Type, Title: p.Title, IsRequired: p.IsRequired}
		if p.Meta != nil {
			def.Meta = &datatug.EntityFieldRef{Entity: p.Meta.Entity, Field: p.Meta.Field}
		}
		params = append(params, def)
	}
	bindings := make([]datatug.QueryCaptureBinding, 0, len(q.BindingOrigins))
	for _, b := range q.BindingOrigins {
		bindings = append(bindings, datatug.QueryCaptureBinding{ParameterID: b.ParameterID, Origin: b.Origin})
	}
	return datatug.QueryDefWithFolderPath{
		FolderPath: q.FolderPath,
		QueryDef: datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: q.ID, Title: q.Title}},
			Type:        datatug.QueryTypeDTQL,
			Text:        q.DTQL,
			Purpose:     q.Purpose,
			Parameters:  params,
			Targets:     []datatug.QueryDefTarget{{Catalog: q.Source}},
			Capture: &datatug.QueryCapture{
				Author:      record.Author,
				Environment: record.Environment,
				Source:      q.Source,
				Collection:  record.Collection,
				Bindings:    bindings,
			},
		},
	}
}

// capturedRecordOf maps a stored query back onto the capture it records.
func capturedRecordOf(q datatug.QueryDefWithFolderPath) capturedRecord {
	record := capturedRecord{Query: capturedQuery{
		FolderPath:     q.FolderPath,
		ID:             q.ID,
		Title:          q.Title,
		Purpose:        q.Purpose,
		DTQL:           q.Text,
		Parameters:     make([]capturedParameter, 0, len(q.Parameters)),
		BindingOrigins: []captureBindingOrigin{},
	}}
	for _, p := range q.Parameters {
		param := capturedParameter{ID: p.ID, Type: p.Type, Title: p.Title, IsRequired: p.IsRequired}
		if p.Meta != nil {
			param.Meta = &entityFieldRef{Entity: p.Meta.Entity, Field: p.Meta.Field}
		}
		record.Query.Parameters = append(record.Query.Parameters, param)
	}
	if c := q.Capture; c != nil {
		record.Author, record.Environment, record.Collection = c.Author, c.Environment, c.Collection
		record.Query.Source = c.Source
		for _, b := range c.Bindings {
			record.Query.BindingOrigins = append(record.Query.BindingOrigins, captureBindingOrigin{ParameterID: b.ParameterID, Origin: b.Origin})
		}
	}
	return record
}

// translateRevisionedStoreError maps the revisioned store's typed errors
// onto *captureStoreError; any other error passes through unchanged (and
// becomes a 500 with no detail on the wire).
func translateRevisionedStoreError(err error) error {
	var location *datatug.InvalidQueryLocationError
	var incomplete *datatug.IncompleteQueryRecordError
	var conflict *datatug.QueryRevisionConflictError
	switch {
	case errors.As(err, &location):
		field := "query.folderPath"
		if location.FolderPath == "" && location.ID != "" {
			field = "query.id"
		}
		// The store's reason can carry an absolute server path and OS error
		// text; a client gets a fixed message naming the segment only.
		return &captureStoreError{Kind: captureErrLocation, Field: field, Reason: querywrite.LocationMessage(location.FolderPath, location.ID, location.Reason)}
	case errors.As(err, &incomplete):
		return &captureStoreError{Kind: captureErrIncomplete, Field: "query.id", Reason: incomplete.Reason}
	case errors.As(err, &conflict):
		return &captureStoreError{Kind: captureErrConflict, Reason: conflict.Reason}
	case validation.IsBadRequestError(err) || validation.IsBadFieldValueError(err):
		return &captureStoreError{Kind: captureErrContent, Field: "query", Reason: err.Error()}
	default:
		return err
	}
}
