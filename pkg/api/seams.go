package api

import (
	"crypto/rand"
	"os"
	"path/filepath"

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
var readmeWriteFile = os.WriteFile

// What a rescan does to the folder of a table that the database no longer has (see
// scan_rescan.go): listing it, looking at it and what is above it for a link, removing
// it and rewriting its columns file.
var (
	scanReadDir   = os.ReadDir
	scanLstat     = os.Lstat
	scanRemoveAll = os.RemoveAll
	scanWriteFile = os.WriteFile
)
