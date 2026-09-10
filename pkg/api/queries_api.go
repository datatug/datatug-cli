package api

import (
	"context"
	"fmt"
	"strings"

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
		FolderPath: folderPathFromCanonicalID(canonicalID),
		QueryDef:   *queryDef,
	}, nil
}

// folderPathFromCanonicalID derives QueryDefWithFolderPath.FolderPath — a
// field its own Validate() requires non-empty — from a canonical,
// folder-qualified query id (ResolveQueryID's return value): everything
// before the last "/", or datatug.RootSharedFolderName ("~") for a query
// directly under queries/ with no subfolder.
//
// Found while restoring GET /queries/all_queries (S121, Task 17): this field
// was never populated (always its zero value, ""), so every live GET
// /datatug/queries/get_query response failed apicore's own automatic
// response validation with "response is not valid: bad value for field
// [folderPath]: missing required field" — a 500 for every query, a
// pre-existing regression this codebase's own get_query unit tests never
// caught because they stub getQueryHandler's `getQuery` var directly
// (pkg/server/endpoints/param_aliases_test.go) rather than exercising the
// real api.GetQuery -> apicore.Execute response-validation path over HTTP,
// and CI's journey e2e job builds datatug-cli from a pinned older tag
// (DATATUG_CLI_REF, apps/datatug-app/e2e/README.md) that predates it. The
// wire value itself is inert today — datatug-apps' IQueryDef has no
// folderPath field at all (project-item-service.ts's ProjItem is typed
// loosely enough that the client silently ignores it) — only its presence
// (non-empty) matters for the response to pass validation.
func folderPathFromCanonicalID(canonicalID string) string {
	if i := strings.LastIndex(canonicalID, "/"); i >= 0 {
		return canonicalID[:i]
	}
	return datatug.RootSharedFolderName
}
