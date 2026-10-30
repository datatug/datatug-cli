package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/querywrite"
	"github.com/datatug/datatug-cli/pkg/sqlexecute"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/gofrs/flock"
	"github.com/mitchellh/go-homedir"
)

var executeSingleSeam = func(ctx context.Context, e sqlexecute.Executor, command sqlexecute.RequestCommand) (sqlexecute.Response, error) {
	return e.ExecuteSingleContext(ctx, command)
}

var homedirExpand = homedir.Expand
var homedirDir = homedir.Dir
var randRead = rand.Read
var executionstoreNewManager = executionstore.NewManager
var filepathRel = filepath.Rel
var filepathAbs = filepath.Abs
var readCatalogFile = os.ReadFile
var readRecordsetFile = os.ReadFile
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

var (
	osMkdirTemp                   = os.MkdirTemp
	osUserConfigDir               = os.UserConfigDir
	osCreateTemp                  = os.CreateTemp
	osRename                      = os.Rename
	osOpenDir                     = os.Open
	fileChmod                     = func(f *os.File, mode os.FileMode) error { return f.Chmod(mode) }
	fileWrite                     = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	fileSync                      = func(f *os.File) error { return f.Sync() }
	fileClose                     = func(f *os.File) error { return f.Close() }
	jsonMarshal                   = json.Marshal
	previewLocalQueryRevisionSeam = previewLocalQueryRevision
	writeLocalQueryJournalSeam    = writeLocalQueryJournal
	localGitOutputSeam            = localGitOutput
	localGitInputSeam             = localGitInput
	saveQueryPayloadDigestSeam    = func(r dto.SaveQueryRequest) (string, error) {
		return r.PayloadDigest()
	}
	flockTryLockContextSeam = func(f *flock.Flock, ctx context.Context, retryDelay time.Duration) (bool, error) {
		return f.TryLockContext(ctx, retryDelay)
	}
	localGitStateSeam = localGitState
	osReadFile        = os.ReadFile
)

// scanOps are the calls of the operating system that every write of a scan into the project
// folder is made with (see scanTree), so that a test makes one fail, or makes Lstat report a
// junction on a platform that has none. Always the real ones in production.
var scanOps = plainfs.OSOps()

// scanTree is the project folder at projectDir as a scan writes into it: only plain files in
// plain folders, never through a link (see package plainfs). Folders are made with the mode 0755.
func scanTree(projectDir string) plainfs.Tree {
	return plainfs.NewWithOps(projectDir, 0o755, scanOps)
}
