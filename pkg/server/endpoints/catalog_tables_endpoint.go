package endpoints

import (
	"fmt"
	"net/http"

	"github.com/datatug/datatug-cli/pkg/api"
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
		handleError(fmt.Errorf("%w: unknown project %q", api.ErrCatalogNotFound, ref.ProjectID), w, r)
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
	tables, err := getCatalogTablesFunc(projectDir, environmentID, catalogID)
	returnJSON(w, r, http.StatusOK, err, tables)
}
