package api

import (
	"crypto/rand"
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
var filepathGlob = filepath.Glob
var dbcopyParse = dbcopy.Parse
var updateSchemaModelSeam = updateSchemaModel
var loadHTTPQueries = httpsource.LoadHTTPQueries
var scanDbCatalogSeam = scanDbCatalog
var accesspoliciesExplain = accesspolicies.Explain
var querywriteQueryCredentialReason = querywrite.QueryCredentialReason
var newProjectWithDatabaseSeam = newProjectWithDatabase
