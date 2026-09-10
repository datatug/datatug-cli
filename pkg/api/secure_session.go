package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// secureMu guards the package-level state ConfigureSecureSession sets once,
// at `datatug serve` startup, and every request handler reads afterwards.
var secureMu sync.RWMutex

var (
	secureExecutor    *secureread.Executor
	secureSession     secureread.Session
	projectDirs       map[string]string
	securityContextID string
	capabilities      Capabilities
)

// Capabilities are the operator-controlled flags `datatug serve` fixes for
// its whole process life, beyond the principal/policy set itself:
// AllowWrites gates every project-mutation route (create/save/delete
// project/query/board/entity/recordset-rows) closed by default
// (api-contract.md "Security and errors": "this read journey must not
// expose an unauthenticated mutation endpoint as a side effect" —
// REQ:principal-selection: "Unneeded write endpoints MUST fail closed").
// AllowOpaqueSQL gates the "separate explicit opaque-query grant" the
// appendix's REQ:opaque-sql-limitation describes: without it, a SQL-typed
// saved query (or any native-SQL execution path) is refused before dispatch
// with UNSUPPORTED_PROTECTED_EXECUTION, matching "the support demo has no
// such grant". Both default false: Phase 1's read journey is safe-by-default
// unless an operator explicitly opts a serve process into either capability.
type Capabilities struct {
	AllowWrites    bool
	AllowOpaqueSQL bool
	// HTTPOffline is `datatug serve --http-offline` (Phase 1 Task 14, item
	// 7): when true, every HTTP-typed saved query's LIVE fetch fails as
	// SOURCE_UNAVAILABLE without ever touching the network (see
	// pkg/httpsource.ContextWithDispatch's offline argument), so a demo can
	// prove "network disabled -> honest failure -> explicit snapshot"
	// deterministically. It is a real operator-facing switch (offline demos),
	// not a test hook. False (the default) leaves live dispatch unaffected.
	HTTPOffline bool
	// ExecTimeout overrides pkg/server/endpoints' default 10-second
	// exec/run_query budget (api-contract.md "Bounded lookups and HTTP":
	// "Server execution has a 10-second default timeout, with a configured
	// upper bound of 30 seconds"). Zero means "use the default"; any value
	// above the 30-second ceiling is clamped down to it — see
	// pkg/server/endpoints/contract_scope.go's execTimeoutFor.
	ExecTimeout time.Duration
}

// ConfigureSecureSession wires the fixed secureread.Session `datatug serve`
// builds once for its whole process lifetime (REQ:principal-selection) into
// every request this process handles, and records the project-id ->
// filesystem-directory map serve already resolved (pathsByID) so a saved
// query's SQL/DTQL sidecar file can be located — pkg/datatug-core's
// LoadQuery does not hydrate QueryDef.Text back from that file yet (that
// lands with the datatug-core sidecar-read story; see loadQueryDocument).
//
// It also mints this process's securityContextId (api-contract.md "Scope
// and identity": "Browser state additionally keys scope by ... server-issued
// securityContextId from agent-info. That opaque ID changes on principal or
// policy-session changes"). Phase 1's serve process runs one fixed
// principal/policy set for its whole life (REQ:principal-selection), so this
// ID is minted once here and never rotates within a process — a fresh
// ConfigureSecureSession call (a new `datatug serve` invocation) is the only
// thing that changes it, which is exactly the "principal or policy-session
// changed" case the contract means STALE_CONTEXT to catch, and it is what
// every scoped handler validates a request's securityContextId against
// (see ValidateSecurityContext).
//
// Every request-handling call site MUST go through SecureExecutor rather
// than open a source or run a query directly, so every read really does
// pass through the one policy-enforced path (REQ:server-acl-all-reads).
func ConfigureSecureSession(session secureread.Session, pathsByID map[string]string, caps Capabilities) {
	secureMu.Lock()
	defer secureMu.Unlock()
	// session.AllowOpaqueSQL is the single point secureread.Executor.
	// RunNativeSQL gates on (defense in depth: every native-SQL caller —
	// exec/run_query and the legacy exec/select/exec/execute_commands
	// routes — shares this one Executor, so setting it here rather than
	// per-endpoint is what makes the refusal apply uniformly).
	session.AllowOpaqueSQL = caps.AllowOpaqueSQL
	secureSession = session
	secureExecutor = secureread.NewExecutor(session)
	projectDirs = pathsByID
	capabilities = caps
	securityContextID = newSecurityContextID()
}

// newSecurityContextID mints a fresh opaque securityContextId: 16 random
// bytes, hex encoded — no more structure than that, per api-contract.md ("An
// ID is a staleness check, never authentication ... must not expose
// credentials or identify another session").
func newSecurityContextID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "securitycontext-unavailable"
	}
	return hex.EncodeToString(buf)
}

// SecurityContextID returns the securityContextId agent-info reports and
// every scoped request must echo back (ValidateSecurityContext).
func SecurityContextID() string {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return securityContextID
}

// ValidateSecurityContext reports whether id matches this process's current
// securityContextId. A caller with an empty configured ID (no
// ConfigureSecureSession call yet — a handler under test) always fails
// closed rather than accepting any value.
func ValidateSecurityContext(id string) bool {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return securityContextID != "" && id == securityContextID
}

// GetCapabilities returns this process's configured Capabilities.
func GetCapabilities() Capabilities {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return capabilities
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

// SecurePrincipalRolesGroups returns the serving principal's Roles/Groups
// for agent-info's exact envelope (AgentInfoPrincipal.Roles/Groups) — never
// nil, so JSON encoding always emits "[]" rather than "null" for an unset
// slice, matching the appendix's "roles:string[]" / "groups:string[]".
func SecurePrincipalRolesGroups() (roles, groups []string) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	if secureSession.Principal == nil {
		return []string{}, []string{}
	}
	roles = secureSession.Principal.Roles
	groups = secureSession.Principal.Groups
	if roles == nil {
		roles = []string{}
	}
	if groups == nil {
		groups = []string{}
	}
	return roles, groups
}

// SecureConfiguredProjectIDs returns every project ID this process is
// serving (ConfigureSecureSession's pathsByID keys), for agent-info's
// "projects" array.
func SecureConfiguredProjectIDs() []string {
	secureMu.RLock()
	defer secureMu.RUnlock()
	ids := make([]string, 0, len(projectDirs))
	for id := range projectDirs {
		ids = append(ids, id)
	}
	return ids
}

// SecureSessionUnrestricted reports whether this process's session runs
// with no access-policy enforcement at all (--no-policies).
func SecureSessionUnrestricted() bool {
	secureMu.RLock()
	defer secureMu.RUnlock()
	return secureSession.Unrestricted
}

// projectDir returns the filesystem directory serve resolved for projectID,
// as configured via ConfigureSecureSession's pathsByID.
func projectDir(projectID string) (string, bool) {
	secureMu.RLock()
	defer secureMu.RUnlock()
	dir, ok := projectDirs[projectID]
	return dir, ok
}

// ProjectDir exports projectDir for other packages (pkg/server/endpoints)
// that need a project's on-disk directory to build a ResolvedSource/resolver
// call without duplicating serve's pathsByID map.
func ProjectDir(projectID string) (string, bool) {
	return projectDir(projectID)
}

// ProjectStoreFor returns the datatug.ProjectStore for projectID, over the
// same storeID convention RunQuery/ExecuteSelect already use
// (storage.NewDatatugStore("")), for resolver calls that need one.
func ProjectStoreFor(projectID string) (datatug.ProjectStore, error) {
	store, err := storage.NewDatatugStore("")
	if err != nil {
		return nil, err
	}
	return store.GetProjectStore(projectID), nil
}
