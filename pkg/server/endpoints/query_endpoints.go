package endpoints

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/personalqueries"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/sneat-co/sneat-go-core/apicore"
)

var getQuery = api.GetQuery

// getQueriesHandler is GET /datatug/queries/all_queries — restored on top of
// the current datatug-core v0.27.3 QueryDef/QueriesFolder models (Task 17,
// S121). It was commented out (route registration AND handler body) in
// cf084e9 ("latest datatug-core with schemer providers moved out of it to
// dedicated repos") along with everything else in this package, and the old
// commented-out body called APIs (`storage.GetStore`,
// `project.Queries().LoadQueries`) that had already been removed from
// ProjectStore's shape by that same commit — restoring it is a fresh
// implementation, not an un-commenting.
//
// Reuses the same folder-qualified-id project loader
// semanticApplicableHandler/loadModuleQueries already use for
// queries/applicable (Task 12), rather than ProjectStore's own
// LoadQueries(ctx, folderPath): that method neither recurses into
// subfolders nor populates QueriesFolder.Folders (see loadModuleQueries's
// own doc comment on semantic_project.go), and this route's client
// (datatug-apps' QueriesTabComponent) fetches the tree exactly ONCE per
// page load and thereafter walks `folders`/`items` already in memory as the
// user clicks into subfolders (cd()/getFolderAndUpdateParents in
// queries-tab.component.ts never re-fetch) — so the response must carry
// the FULL recursive tree, every nested folder's items included, not just
// the top level or whatever `folder=` was requested.
//
// Authorization (api-contract.md: "Unauthorized queries and targets are
// omitted entirely"): queries/applicable's own omission mechanism is
// EligibleTargets, a per-*environment* source-availability check — verified
// (resolver.go) to be the ONLY per-query authorization gate this codebase
// has; it is not principal/role-based (AgentInfo's ProtectedQueries
// capability is always true regardless of principal, and OpaqueReadOnly
// only gates *execution* mode, never listing). EligibleTargets cannot apply
// here: it requires an `environment` argument, and this route's own client
// call (QueriesService.getQueriesFolder -> project-item-service.ts's
// getFolder, params: project+folder only) never carries one — nor does
// api-contract.md's own listing contract ask for one. No other
// principal-scoped filter exists for query metadata anywhere in this
// codebase today (row-level ACL policies gate query *execution results*,
// never which queries are listed). So every query in the project is listed
// for every principal: per this task's own documented fallback ("list only
// queries the principal could execute under current policy"), that IS the
// current-policy answer — not an unfiltered omission — since demo-project-1
// authorizes both `--as admin` and `--as support` to execute every one of
// its 5 demo queries today.
func getQueriesHandler(w http.ResponseWriter, r *http.Request) {
	ctx, err := getContextFromRequest(r)
	if err != nil {
		handleError(err, w, r)
	}
	ref, err := newProjectRef(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	root, err := parseQueriesRoot(r.URL.Query())
	if err != nil {
		handleError(err, w, r)
		return
	}
	var folder *datatug.QueriesFolder
	if root == queriesRootPersonal {
		folder, err = getPersonalQueries(ctx, ref)
	} else {
		folder, err = getAllQueries(ctx, ref)
	}
	returnJSON(w, r, http.StatusOK, err, folder)
}

// urlParamRoot is GET /datatug/queries/all_queries's additive S169
// parameter (lead assumption, api-contract.md's own listing section covers
// neither `all_queries` nor personal roots at all — see this route's
// getQueriesHandler doc comment and this task's PR): "shared" (the
// default, byte-identical to this route's pre-S169 behavior) or "personal"
// (the serving principal's own `user:<id>` root, never the shared tree).
const urlParamRoot = "root"

const (
	queriesRootShared   = "shared"
	queriesRootPersonal = "personal"
)

// ErrInvalidQueriesRoot is handleError's INVALID_REQUEST trigger (field
// "root") for an unrecognized ?root= value — the same {code,field} shape
// api.ErrUnknownStoreID/ErrAmbiguousStore already give ?storage=.
var ErrInvalidQueriesRoot = errors.New("invalid queries root")

// parseQueriesRoot reads and validates ?root=, defaulting a missing/blank
// value to queriesRootShared so every pre-S169 request (no `root` param at
// all) resolves exactly as before. Any other value is ErrInvalidQueriesRoot
// (handleError -> 400 INVALID_REQUEST, field "root"), never a silent
// fallback to shared.
func parseQueriesRoot(q url.Values) (string, error) {
	root := strings.TrimSpace(q.Get(urlParamRoot))
	if root == "" {
		return queriesRootShared, nil
	}
	switch root {
	case queriesRootShared, queriesRootPersonal:
		return root, nil
	default:
		return "", fmt.Errorf("%w: must be %q or %q, got %q", ErrInvalidQueriesRoot, queriesRootShared, queriesRootPersonal, root)
	}
}

// getAllQueries loads every query under ref.ProjectID's queries/ tree and
// nests them into datatug-core's QueriesFolder shape. The `_ context.Context`
// param is kept (rather than dropped) to match this package's other
// business-logic functions' signatures (e.g. computeSemanticApplicable) —
// loadModuleQueries itself does no I/O that needs cancellation (a plain
// filepath.WalkDir), so it is not threaded through further.
func getAllQueries(_ context.Context, ref dto.ProjectRef) (*datatug.QueriesFolder, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	projectDir, ok := api.ProjectDir(ref.ProjectID)
	if !ok {
		return nil, fmt.Errorf("%w: unknown project %q", api.ErrQueryNotFound, ref.ProjectID)
	}
	queries, canonicalIDs, err := loadModuleQueries(projectDir)
	if err != nil {
		return nil, err
	}
	return buildQueriesFolderTree(queries, canonicalIDs, datatug.RootSharedFolderName), nil
}

// getPersonalQueries loads the serving principal's own personal root
// rather than getAllQueries' shared tree — GET
// /datatug/queries/all_queries?root=personal (S169, on-disk layout revised
// by S172). Never creates that directory for a read: a principal with no
// personal queries yet gets an empty folder, exactly like loadModuleQueries
// already does for any other missing directory (walkJSONFiles treats
// ENOENT as "no files", not an error).
//
// On-disk location (S172, superseding S169's original
// "<project>/user:<principalID>/queries/" sibling-directory layout):
// personalqueries.ResolveProjectDir(ref.ProjectID) —
// $DATATUG_PERSONAL_DIR/<projectID> if that env var is set, else
// ~/.datatug/projects/<projectID> — entirely OUTSIDE the shared project
// directory api.ProjectDir resolves, and so never in the project's own git
// repo. Founder ruling 2026-09-11 (verbatim): "where personal queries live
// on disk for datatug serve. - I'm Ok with your suggestion for now." — see
// personalqueries.ResolveProjectDir's own doc comment for the full
// rationale (no colon in a directory name — illegal on Windows — and the
// "for now"/provisional caveat). The wire root id stays
// datatug.RootUserFolderPrefix+principalID unchanged, the same
// datatug-core convention getAllQueries' RootSharedFolderName sits beside;
// only the on-disk directory moved.
//
// An anonymous principal (no `--as` at `datatug serve` startup —
// api.SecurePrincipalID() == "") owns no personal root at all: rather than
// read a directory no real principal ID could ever produce ("user:" with
// nothing after the prefix), it returns an empty folder without touching
// disk, per this task's own documented fallback ("an
// unauthenticated/anonymous principal gets an empty personal root, never
// an error").
func getPersonalQueries(_ context.Context, ref dto.ProjectRef) (*datatug.QueriesFolder, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	// Same unknown-project rule as getAllQueries: a project serve never
	// opened is 404 for root=personal too (not an empty folder read from
	// whatever ~/.datatug/projects/<id>/ happens to hold), whoever asks.
	if _, ok := api.ProjectDir(ref.ProjectID); !ok {
		return nil, fmt.Errorf("%w: unknown project %q", api.ErrQueryNotFound, ref.ProjectID)
	}
	principalID := api.SecurePrincipalID()
	rootID := datatug.RootUserFolderPrefix + principalID
	if principalID == "" {
		return &datatug.QueriesFolder{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: rootID}}}, nil
	}
	// personalqueries.ErrInvalidProjectID (an unsafe path segment) is
	// handled by util_error_handling.go exactly like ErrInvalidQueriesRoot
	// below — 400 INVALID_REQUEST, field "project". In practice
	// ref.ProjectID always comes from a loaded datatug-project.json or a
	// served project's config key (never raw user input), so this is a
	// defensive backstop, not an expected everyday response.
	personalProjectDir, err := personalqueries.ResolveProjectDir(ref.ProjectID)
	if err != nil {
		return nil, err
	}
	queries, canonicalIDs, err := loadModuleQueries(personalProjectDir)
	if err != nil {
		return nil, err
	}
	return buildQueriesFolderTree(queries, canonicalIDs, rootID), nil
}

// buildQueriesFolderTree nests loadModuleQueries' flat, canonical-id-keyed
// query list back into the recursive datatug.QueriesFolder shape (the wire
// IQueryFolder{id,folders,items} datatug-apps' QueriesTabComponent decodes),
// creating one QueriesFolder per distinct folder path (intermediate
// folders included, even if empty of their own items) and appending each
// query to its own folder's Items. rootID is the returned root folder's ID
// — datatug.RootSharedFolderName ("~") for getAllQueries' shared tree, or
// a personal root's "user:<principalID>" for getPersonalQueries; every
// other (sub)folder's ID is its own directory name.
func buildQueriesFolderTree(queries []*datatug.QueryDef, canonicalIDs map[*datatug.QueryDef]string, rootID string) *datatug.QueriesFolder {
	root := &datatug.QueriesFolder{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: rootID}}}
	folders := map[string]*datatug.QueriesFolder{"": root}
	var ensureFolder func(path string) *datatug.QueriesFolder
	ensureFolder = func(path string) *datatug.QueriesFolder {
		if f, ok := folders[path]; ok {
			return f
		}
		parentPath, name := "", path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			parentPath, name = path[:i], path[i+1:]
		}
		parent := ensureFolder(parentPath)
		f := &datatug.QueriesFolder{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: name}}}
		parent.Folders = append(parent.Folders, f)
		folders[path] = f
		return f
	}
	for _, q := range queries {
		folderPath, _ := splitCanonicalQueryID(canonicalIDs[q])
		f := ensureFolder(folderPath)
		f.Items = append(f.Items, q)
	}
	sortQueriesFolderTree(root)
	return root
}

// splitCanonicalQueryID splits a canonical, folder-qualified query id
// ("customers/customer-invoices") into its folder path ("customers") and
// bare id ("customer-invoices") — folderPath is "" for a query directly
// under queries/, with no subfolder.
func splitCanonicalQueryID(canonicalID string) (folderPath, bareID string) {
	if i := strings.LastIndex(canonicalID, "/"); i >= 0 {
		return canonicalID[:i], canonicalID[i+1:]
	}
	return "", canonicalID
}

// sortQueriesFolderTree sorts every folder's Folders/Items by ID,
// recursively — deterministic output (this codebase's own established
// convention, e.g. semantic_applicable.go's Candidate sort), needed here
// because loadModuleQueries's own filepath.WalkDir order interleaves
// folders and does not guarantee any particular nesting-build order.
func sortQueriesFolderTree(folder *datatug.QueriesFolder) {
	sort.Slice(folder.Folders, func(i, j int) bool { return folder.Folders[i].ID < folder.Folders[j].ID })
	sort.Slice(folder.Items, func(i, j int) bool { return folder.Items[i].ID < folder.Items[j].ID })
	for _, f := range folder.Folders {
		sortQueriesFolderTree(f)
	}
}

// getQueryHandler returns query definition. The web client
// (project-item-service.ts's getProjItem, instantiated for queries with
// itemPath="query") sends the query id as `?query=`, not `?id=` — widen the
// item-id lookup to accept both.
func getQueryHandler(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	getProjectItem(w, r, &ref, func(ctx context.Context) (responseDTO apicore.ResponseDTO, err error) {
		return getQuery(ctx, ref)
	}, urlParamID, "query")
}

// createQuery handles create query endpoint
var createQuery = func(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectRef
	var request dto.CreateQuery
	tw := &timeoutStatusWriter{ResponseWriter: w}
	saveFunc := func(ctx context.Context) (apicore.ResponseDTO, error) {
		query, err := api.CreateQuery(ctx, request)
		return query, tw.observe(err)
	}
	createProjectItem(tw, r, &ref, &request, saveFunc)
}

// updateQuery handles update query endpoint
func updateQuery(w http.ResponseWriter, r *http.Request) {
	var ref dto.ProjectItemRef
	var request dto.UpdateQuery
	tw := &timeoutStatusWriter{ResponseWriter: w}
	saveFunc := func(ctx context.Context) (apicore.ResponseDTO, error) {
		query, err := api.UpdateQuery(ctx, request)
		return query, tw.observe(err)
	}
	saveProjectItem(tw, r, &ref, &request, saveFunc)
}

// deleteQuery handles delete query endpoint
var deleteQuery = timeoutAwareDelete(api.DeleteQuery)

// timeoutAwareDelete is deleteProjItem(del) answering a timed-out delete
// with 504 (timeoutStatusWriter).
func timeoutAwareDelete(del func(ctx context.Context, ref dto.ProjectItemRef) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tw := &timeoutStatusWriter{ResponseWriter: w}
		deleteProjItem(func(ctx context.Context, ref dto.ProjectItemRef) error {
			return tw.observe(del(ctx, ref))
		})(tw, r)
	}
}

// timeoutStatusWriter lets the legacy query write routes answer a write
// that timed out waiting for the query store (api's bounded write,
// querywrite.Timeout) with 504 Gateway Timeout. Those routes report errors
// through apicore, whose error mapping knows only 400, 401 and 500, so a
// timeout would otherwise be an indistinct 500; the body keeps the legacy
// error envelope and the timeout's fixed message.
type timeoutStatusWriter struct {
	http.ResponseWriter
	timedOut bool
}

// observe records whether err is a timeout and returns it unchanged.
func (w *timeoutStatusWriter) observe(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		w.timedOut = true
	}
	return err
}

// WriteHeader turns the 500 apicore writes for an observed timeout into 504.
func (w *timeoutStatusWriter) WriteHeader(status int) {
	if w.timedOut && status == http.StatusInternalServerError {
		status = http.StatusGatewayTimeout
	}
	w.ResponseWriter.WriteHeader(status)
}
