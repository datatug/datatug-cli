package api

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/strongo/validation"
)

//// GetQueries returns queries
//func GetQueries(ctx context.Context, ref dto.ProjectRef, folder string) (*datatug.QueryFolder, error) {
//	store, err := storage.GetStore(ctx, ref.StoreID)
//	if err != nil {
//		return nil, err
//	}
//	//goland:noinspection GoNilness
//	project := store.GetProjectStore(ref.ProjectID)
//	return project.Queries().LoadQueries(ctx, folder)
//}

// CreateQuery creates a new query
func CreateQuery(ctx context.Context, request dto.CreateQuery) (*datatug.QueryDefWithFolderPath, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	store, err := projectStoreForID(request.StoreID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	return &request.Query, store.SaveQuery(ctx, &request.Query)
}

// UpdateQuery updates existing query
func UpdateQuery(ctx context.Context, request dto.UpdateQuery) (*datatug.QueryDefWithFolderPath, error) {
	if err := request.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	store, err := projectStoreForID(request.StoreID, request.ProjectID)
	if err != nil {
		return nil, err
	}
	return &request.Query, store.SaveQuery(ctx, &request.Query)
}

// DeleteQuery deletes query
func DeleteQuery(ctx context.Context, ref dto.ProjectItemRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	return store.DeleteQuery(ctx, ref.ID)
}

// GetQuery returns query definition. ref.ID may be bare or folder-qualified
// (S97's one saved-query id convention): resolved via ResolveQueryID before
// the store ever sees it, so an unknown id is ErrQueryNotFound and an
// ambiguous bare id is ErrAmbiguousQueryID — never the store's own raw
// filesystem error text.
func GetQuery(ctx context.Context, ref dto.ProjectItemRef) (query *datatug.QueryDefWithFolderPath, err error) {
	if err = ref.Validate(); err != nil {
		return query, err
	}
	projectDir, ok := ProjectDir(ref.ProjectID)
	if !ok {
		return nil, fmt.Errorf("%w: unknown project %q", ErrQueryNotFound, ref.ProjectID)
	}
	canonicalID, err := ResolveQueryID(projectDir, ref.ID)
	if err != nil {
		return nil, err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return nil, err
	}
	queryDef, err := store.LoadQuery(ctx, canonicalID)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrQueryNotFound, canonicalID)
	}
	return &datatug.QueryDefWithFolderPath{
		QueryDef: *queryDef,
	}, nil
}
