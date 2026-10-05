package api

import (
	"crypto/rand"
	"os"
	"path/filepath"

	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/mitchellh/go-homedir"
)

var executeSingleSeam = func(e sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
	return e.ExecuteSingle(command)
}

var homedirExpand = homedir.Expand
var homedirDir = homedir.Dir
var randRead = rand.Read
var executionstoreNewManager = executionstore.NewManager
var filepathRel = filepath.Rel
var filepathAbs = filepath.Abs
var readCatalogFile = os.ReadFile
var dbcopyParse = dbcopy.Parse
var updateSchemaModelSeam = updateSchemaModel
var loadHTTPQueries = httpsource.LoadHTTPQueries
var scanDbCatalogSeam = scanCatalog
var accesspoliciesExplain = accesspolicies.Explain
var querywriteQueryCredentialReason = querywrite.QueryCredentialReason
var newProjectWithDatabaseSeam = newProjectWithDatabase

// scanReadDir is how a rescan lists the folders of the tables and views it may take back (see
// scan_rescan.go): a read, which a test makes fail. Always os.ReadDir in production.
var scanReadDir = os.ReadDir

// scanOps are the calls of the operating system that every write of a scan into the project
// folder is made with (see scanTree), so that a test makes one fail, or makes Lstat report a
// junction on a platform that has none. Always the real ones in production.
var scanOps = plainfs.OSOps()

// scanTree is the project folder at projectDir as a scan writes into it: only plain files in
// plain folders, never through a link (see package plainfs). Folders are made with the mode 0755.
func scanTree(projectDir string) plainfs.Tree {
	return plainfs.NewWithOps(projectDir, 0o755, scanOps)
}
