package api

import (
	"fmt"
	"sync"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// secureMu guards the package-level state ConfigureSecureSession sets once,
// at `datatug serve` startup, and every request handler reads afterwards.
var secureMu sync.RWMutex

var (
	secureExecutor *secureread.Executor
	secureSession  secureread.Session
	projectDirs    map[string]string
)

// ConfigureSecureSession wires the fixed secureread.Session `datatug serve`
// builds once for its whole process lifetime (REQ:principal-selection) into
// every request this process handles, and records the project-id ->
// filesystem-directory map serve already resolved (pathsByID) so a saved
// query's SQL/DTQL sidecar file can be located — pkg/datatug-core's
// LoadQuery does not hydrate QueryDef.Text back from that file yet (that
// lands with the datatug-core sidecar-read story; see loadQueryDocument).
//
// Every request-handling call site MUST go through SecureExecutor rather
// than open a source or run a query directly, so every read really does
// pass through the one policy-enforced path (REQ:server-acl-all-reads).
func ConfigureSecureSession(session secureread.Session, pathsByID map[string]string) {
	secureMu.Lock()
	defer secureMu.Unlock()
	secureSession = session
	secureExecutor = secureread.NewExecutor(session)
	projectDirs = pathsByID
}

// SecureExecutor returns the Executor ConfigureSecureSession built, and
// false when serve has not configured one yet (e.g. a handler under test
// with no ConfigureSecureSession call).
func SecureExecutor() (*secureread.Executor, bool) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return secureExecutor, secureExecutor != nil
}

// SecurePrincipalID returns the serving principal's ID for `agent-info` to
// report (REQ:principal-selection), or "" when the session carries no
// identified principal (Unrestricted with no --as, or a role/group-only
// principal).
func SecurePrincipalID() string {
	secureMu.RLock()
	defer secureMu.RUnlock()
	if secureSession.Principal == nil || secureSession.Principal.ID == nil {
		return ""
	}
	if id, ok := secureSession.Principal.ID.(string); ok {
		return id
	}
	return fmt.Sprint(secureSession.Principal.ID)
}

// projectDir returns the filesystem directory serve resolved for projectID,
// as configured via ConfigureSecureSession's pathsByID.
func projectDir(projectID string) (string, bool) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	dir, ok := projectDirs[projectID]
	return dir, ok
}
