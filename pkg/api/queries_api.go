package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/querywrite"
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

// CreateQuery is the legacy queries/create_query write. See saveLegacyQuery.
func CreateQuery(ctx context.Context, request dto.CreateQuery) (*datatug.QueryDefWithFolderPath, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return saveLegacyQuery(ctx, request.StoreID, request.ProjectID, &request.Query)
}

// UpdateQuery is the legacy queries/update_query write. See saveLegacyQuery.
func UpdateQuery(ctx context.Context, request dto.UpdateQuery) (*datatug.QueryDefWithFolderPath, error) {
	if err := request.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	return saveLegacyQuery(ctx, request.StoreID, request.ProjectID, &request.Query)
}

// saveLegacyQuery is the write both legacy routes share. They used to be
// gated only by the process-wide --allow-writes flag, so any serving
// principal - a read-only one included - could write any query, an id such
// as "../x" addressed a file outside the project's queries/ tree, and an
// unvalidated query (a target carrying a password) reached git-tracked
// files. Now, before the store is touched: the folder path and id must be
// safe path segments, and a folder must be one this build's store really
// writes to (requireFolderSupport; 400 otherwise), the serving principal
// must be authorized for a project write through AuthorizeProjectQueryWrite
// (the gate queries/capture uses; 403 otherwise), and the query must pass
// QueryDef.Validate and the credential screen every query write path
// shares, over its title, purpose, text, parameter titles and defaults and
// target fields (querywrite.QueryCredentialReason; 400 otherwise). The
// write itself keeps its legacy
// create-or-replace semantics (DALgo Set): a revision-checked write is
// queries/capture's job.
func saveLegacyQuery(ctx context.Context, storeID, projectID string, query *datatug.QueryDefWithFolderPath) (*datatug.QueryDefWithFolderPath, error) {
	queryID, err := legacyQueryID(query.FolderPath, query.ID)
	if err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	// "~" (datatug.RootSharedFolderName) is this API's own id for the
	// queries root - GetQuery returns it as a root query's folderPath, so a
	// client that round-trips get_query into update_query sends it back.
	// The store's root is "": a store that honours FolderPath (the
	// revisioned filestore) would otherwise write a duplicate query into a
	// literal "queries/~/" directory.
	if query.FolderPath == datatug.RootSharedFolderName {
		query.FolderPath = ""
	}
	if err := requireFolderSupport(queryID); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	if err := AuthorizeProjectQueryWrite(ctx, projectID, queryID, access.Set); err != nil {
		return nil, err
	}
	if err := query.QueryDef.Validate(); err != nil {
		return nil, validation.NewBadRequestError(err)
	}
	if field, reason, found := querywrite.QueryCredentialReason(&query.QueryDef); found {
		return nil, validation.NewBadRequestError(validation.NewErrBadRecordFieldValue(field, reason))
	}
	store, err := projectStoreForID(storeID, projectID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveQuery(ctx, query); err != nil {
		return nil, legacyStoreFailure(err)
	}
	return query, nil
}

// legacyStoreFailure maps a failed legacy store write: a refused location
// becomes a 400 carrying a fixed message that names the offending segment
// only (storeLocationRefusal); anything else passes through.
func legacyStoreFailure(err error) error {
	if message, ok := storeLocationRefusal(err); ok {
		return validation.NewBadRequestError(errors.New(message))
	}
	return err
}

// DeleteQuery is the legacy queries/delete_query write. ref.ID is the
// query's folder-qualified id; every segment must be safe and a folder must
// be one this build's store resolves (requireFolderSupport; 400 otherwise),
// and the serving principal must be authorized to delete it (403
// otherwise) before the store is touched.
func DeleteQuery(ctx context.Context, ref dto.ProjectItemRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if err := validateQueryPath("id", ref.ID); err != nil {
		return validation.NewBadRequestError(err)
	}
	if err := requireFolderSupport(ref.ID); err != nil {
		return validation.NewBadRequestError(err)
	}
	if err := AuthorizeProjectQueryWrite(ctx, ref.ProjectID, ref.ID, access.Delete); err != nil {
		return err
	}
	store, err := projectStoreForID(ref.StoreID, ref.ProjectID)
	if err != nil {
		return err
	}
	if err := store.DeleteQuery(ctx, ref.ID); err != nil {
		return legacyStoreFailure(err)
	}
	return nil
}

// errUnsafeQueryLocation marks a folder path or query id that does not name
// a location inside the project's queries/ tree.
var errUnsafeQueryLocation = errors.New("unsafe query location")

// errFolderNotWritable marks a folder-qualified legacy write that this
// build's store would not put where the request asks
// (legacyStoreResolvesFolders).
var errFolderNotWritable = errors.New("this agent's query store writes and deletes root queries only")

// requireFolderSupport refuses a folder-qualified queryID when this build's
// store would not write or delete exactly that location, so the resource
// AuthorizeProjectQueryWrite evaluates is always the file the store
// touches. Refusing is the fail-closed choice: authorizing the root
// location instead would silently put the query somewhere the caller did
// not ask for.
func requireFolderSupport(queryID string) error {
	if legacyStoreResolvesFolders || !strings.Contains(queryID, "/") {
		return nil
	}
	return fmt.Errorf("%w: %q names a folder, and it is refused rather than written to or deleted from another location", errFolderNotWritable, queryID)
}

// legacyQueryID validates a legacy query's folder path and bare id and
// returns its folder-qualified id. A folder path of "" or
// datatug.RootSharedFolderName ("~", the root id GetQuery responses carry)
// is the queries root.
func legacyQueryID(folderPath, id string) (string, error) {
	if err := validateQueryPathSegment("id", id); err != nil {
		return "", err
	}
	if folderPath == "" || folderPath == datatug.RootSharedFolderName {
		return id, nil
	}
	if err := validateQueryPath("folderPath", folderPath); err != nil {
		return "", err
	}
	return folderPath + "/" + id, nil
}

// validateQueryPath checks every "/"-separated segment of p.
func validateQueryPath(field, p string) error {
	for _, segment := range strings.Split(p, "/") {
		if err := validateQueryPathSegment(field, segment); err != nil {
			return err
		}
	}
	return nil
}

// validateQueryPathSegment rejects a segment querywrite.SegmentReason
// refuses - the one segment validator every query write path shares, with
// datatug-core's storage rules: nothing that could leave its parent
// directory (empty, so no absolute path and no "a//b"; "." or ".."; a
// separator or NUL), be read differently by another OS or tool (control and
// bidi characters, Windows-illegal characters and device names, a trailing
// "." or space) or collide with the store's own entries (a leading ".",
// ".dt-query-txn"), nothing over 200 bytes, and never "~".
func validateQueryPathSegment(field, segment string) error {
	if reason, ok := querywrite.SegmentReason(segment); !ok {
		return fmt.Errorf("%w: %s segment %q %s", errUnsafeQueryLocation, field, segment, reason)
	}
	return nil
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
