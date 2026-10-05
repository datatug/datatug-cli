package api

import (
	"context"
	"fmt"
	"slices"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// CheckScanDriverAgainstProject is an error when the project already records the catalog
// catalogID in environment under another driver than driver. A catalog file is kept by
// environment and id, and not by driver, so a database of one id under two drivers in one
// environment is one catalog file that two servers list: the scan of the second would put its
// own catalog file in the place of the first's, leave the id on both servers, and take back the
// tables of the first as the ones its database no longer has. A SQLite file and a PostgreSQL
// database each have their own id (--db). The message names both drivers, and says to use
// another --db. A catalog the project does not record, or records with no driver, or whose
// catalog file cannot be read, has none to disagree with: the scan writes the file anew.
func CheckScanDriverAgainstProject(projectDir, environment, catalogID, driver string) error {
	recorded := recordedCatalogDriver(projectDir, environment, catalogID)
	if recorded == "" || recorded == driver {
		return nil
	}
	// The recorded driver is what a project file says, and is named only when it is a name.
	named := "another driver"
	if dbcopy.IsPlainSourceID(recorded) {
		named = fmt.Sprintf("a %q database", recorded)
	}
	return fmt.Errorf("--db %q is %s in environment %q of this project, and this scan is -D %s: a database is kept by environment and id, whatever its driver, so scanning it again as another driver would put this scan's catalog in the place of the first and take back its tables; use another --db for this database", catalogID, named, environment, driver)
}

// recordedCatalogDriver is the driver the project records for catalog catalogID in environment,
// read through the project store as recordedDbModel reads the model, or "" when the project
// records none: it has no catalog of that id in that environment (by the names its folder lists),
// or its file cannot be read, or the file names no driver.
func recordedCatalogDriver(projectDir, environment, catalogID string) string {
	if !slices.Contains(catalogIDsOf(projectDir, environment), catalogID) {
		return ""
	}
	store := filestore.NewProjectStore(storage.SingleProjectID, projectDir)
	catalog, err := store.LoadEnvDbCatalog(context.Background(), environment, "", catalogID)
	if err != nil {
		return ""
	}
	return catalog.Driver
}
