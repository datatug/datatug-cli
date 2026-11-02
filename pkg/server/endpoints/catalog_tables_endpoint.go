package endpoints

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/access"
	"github.com/dal-go/dalgo/dal"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// getCatalogTablesHandler is GET /datatug/catalog-tables (Task 17 item A.2,
// S121) — feeds datatug-apps' EnvDbPageComponent, the catalog-overview page
// that lists a catalog's tables/views (see api.CatalogTables's own doc
// comment for why no existing endpoint already carries this). Params follow
// this codebase's established alias convention (paramAlias/fillProjectRef):
// project id as "project"/"proj", environment id as "environment"/"env",
// catalog id as "catalog"/"db" (the "db" alias matches AgentService.select's
// own `db` param for the same catalog identity — env-db-table.page.ts's
// sibling deeper route already sends that name for it). Uses the same
// legacy returnJSON/handleError envelope getEntities/getQueriesHandler use
// (not the newer apicontract envelope — this route has no
// api-contract.md entry to conform to), so an unknown project/catalog is
// reported via api.ErrCatalogNotFound the same way GetQuery/getAllQueries
// already repurpose api.ErrQueryNotFound for both "unknown project" and
// "unknown query" — see util_error_handling.go's handleError.
var catalogProjectDir = api.ProjectDir

// getCatalogTablesFunc reads the tables of a catalog, a seam so a test can count
// the requests that got as far as reading.
var getCatalogTablesFunc = api.GetCatalogTables

func getCatalogTablesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		writeContractError(w, r, newContractError(apicontract.ErrCodeTimeout, "catalog read cancelled", ""))
		return
	}
	q := r.URL.Query()
	ref, err := newProjectRef(q)
	if err != nil {
		handleError(err, w, r)
		return
	}
	if err := ref.Validate(); err != nil {
		handleError(err, w, r)
		return
	}
	projectDir, ok := catalogProjectDir(ref.ProjectID)
	if !ok {
		handleError(fmt.Errorf("%w: unknown project %q", api.ErrCatalogNotFound, dbcopy.SourceIDDisplay(ref.ProjectID)), w, r)
		return
	}
	environmentID := paramAlias(q, "environment", "env")
	catalogID := paramAlias(q, "catalog", "db")
	// Both become folder names under the project: refuse anything but a plain name
	// before either reaches a file path. The refusal is a 400 whose message names
	// the parameter and echoes nothing.
	if err := api.ValidateCatalogIdentifiers(environmentID, catalogID); err != nil {
		handleError(err, w, r)
		return
	}
	executor, configured := secureExecutorHook()
	if !configured {
		writeContractError(w, r, newContractError(apicontract.ErrCodeAccessDenied, "access denied", ""))
		return
	}
	store, err := relatedProjectStoreFor(ref.ProjectID)
	if err != nil {
		writeContractError(w, r, newSourceUnavailable("the source cannot be read"))
		return
	}
	var driver string
	if api.SourceBoundProfile() {
		// The configured profile must resolve its exact validated native target.
		registered, err := apiResolveSource(r.Context(), store, projectDir, environmentID, catalogID)
		if err != nil {
			writeContractError(w, r, newSourceUnavailable("the source cannot be read"))
			return
		}
		parsed, err := dbcopy.Parse(registered.URL)
		if err != nil {
			writeContractError(w, r, newSourceUnavailable("the source cannot be read"))
			return
		}
		driver = parsed.Scheme
	} else {
		// Legacy stored-scan metadata does not open or resolve live connections,
		// unrelated HTTP queries, or recordsets. Its exact stored catalog supplies
		// the same default-schema identity used by reads; policies still intersect.
		catalog, err := store.LoadEnvDbCatalog(r.Context(), environmentID, "", catalogID)
		if err != nil {
			writeContractError(w, r, newSourceUnavailable("the source cannot be read"))
			return
		}
		driver = catalog.Driver
	}
	driver = strings.ToLower(driver)
	if dbcopy.IsLibSQLScheme(driver) {
		driver = api.DriverLibSQL
	}
	tables, err := getCatalogTablesFunc(projectDir, environmentID, catalogID)
	if err != nil {
		returnJSON(w, r, http.StatusOK, err, nil)
		return
	}
	if tables == nil {
		writeContractError(w, r, newContractError(codeInternal, "catalog permissions could not be verified", ""))
		return
	}
	filter := func(items []api.CatalogTable) ([]api.CatalogTable, error) {
		allowed := make([]api.CatalogTable, 0, len(items))
		for _, table := range items {
			if err := r.Context().Err(); err != nil {
				return nil, err
			}
			collection, supported := lookupCollectionName(table.Schema, table.Name, driver)
			if table.Name == "" || !utf8.ValidString(table.Name) || !utf8.ValidString(table.Schema) {
				return nil, errors.New("catalog table identity is unsupported")
			}
			query := authorizeQuery(collection)
			if !supported && api.SecureSessionUnrestricted() && table.Schema != "" {
				query = dal.From(dal.NewQualifiedRootCollectionRef(table.Schema, table.Name, "")).NewQuery().SelectIntoRecord(nil)
			} else if !supported {
				return nil, errors.New("catalog table identity is unsupported")
			}
			err := executor.Authorize(r.Context(), query, nil)
			if err == nil {
				allowed = append(allowed, table)
			} else if !catalogTableDenied(err) {
				return nil, err
			}
		}
		return allowed, nil
	}
	// Use the same fixed session and source spelling as records/schema and
	// records/lookup. Stored catalog names never grant their own visibility.
	allowedTables, filterErr := filter(tables.Tables)
	var allowedViews []api.CatalogTable
	if filterErr == nil {
		allowedViews, filterErr = filter(tables.Views)
	}
	if filterErr != nil || r.Context().Err() != nil || api.CheckSourceBinding(r.Context()) != nil {
		writeContractError(w, r, newContractError(codeInternal, "catalog permissions could not be verified", ""))
		return
	}
	returnJSON(w, r, http.StatusOK, nil, &api.CatalogTables{Tables: allowedTables, Views: allowedViews})
}

// A policy evaluation failure is not evidence that one relation is invisible.
// Only ordinary definitive table denials may be omitted from discovery.
func catalogTableDenied(err error) bool {
	decisions := access.DecisionsFromError(err)
	denied := false
	for _, decision := range decisions {
		if decision.Allowed {
			continue
		}
		switch decision.Code {
		case access.CodeNoMatch, access.CodeRuleDenied, access.CodeCollectionDenied:
			denied = true
		default:
			return false
		}
	}
	return denied
}
