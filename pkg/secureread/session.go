package secureread

import (
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// Session is the fixed identity and policy set one `datatug serve` process
// runs under for its whole life (REQ:principal-selection): the principal
// named by --as/--role/--group, the policies loaded from
// --policies-dir/--policy, and whether the session runs Unrestricted
// (--no-policies, deliberately bypassing every policy). Build one with
// NewSession and hand it to NewExecutor.
type Session struct {
	// Principal is the caller every query in this session runs as; nil only
	// when Unrestricted is set.
	Principal *access.Principal
	// Policies are the loaded documents every secured query runs through.
	Policies []accesspolicies.Loaded
	// Unrestricted must be set explicitly (--no-policies) to run with no
	// policy enforcement at all.
	Unrestricted bool
	// AllowOpaqueSQL is the "separate explicit opaque-query grant"
	// REQ:opaque-sql-limitation describes: without it (and without
	// Unrestricted), RunNativeSQL refuses before dispatch with
	// ErrOpaqueSQLNotGranted, for every caller — the appendix's exec/
	// run_query AND every legacy execution route (exec/select,
	// exec/execute_commands) share this one Executor/Session, so gating it
	// here (rather than per-endpoint) is what makes "all legacy routes obey
	// the same boundary" (api-contract.md "Security and errors") actually
	// true instead of aspirational. Set from `datatug serve
	// --allow-opaque-sql` via pkg/api.Capabilities/ConfigureSecureSession.
	AllowOpaqueSQL bool
}

// SessionOptions mirrors the serve-style flags `datatug query run` already
// accepts (see apps/datatugapp/commands/cmd_query.go), so `datatug serve`
// can build a Session from the same --as/--role/--group/--policies-dir/
// --policy/--no-policies flags without re-deriving the plumbing.
type SessionOptions struct {
	// As is the --as principal ID; empty means no ID (roles/groups only, or
	// no principal at all).
	As string
	// Roles are the --role values (repeatable).
	Roles []string
	// Groups are the --group values (repeatable).
	Groups []string
	// PoliciesDir is the --policies-dir value; empty means
	// $DATATUG_POLICIES_DIR or ~/.datatug/policies (accesspolicies.ResolveDir).
	PoliciesDir string
	// PolicyFiles are additional --policy documents, applied after the
	// directory.
	PolicyFiles []string
	// NoPolicies runs the session Unrestricted (--no-policies): no policies
	// are loaded and no principal is required.
	NoPolicies bool
}

// NewSession loads the session's policies (accesspolicies.Load) and resolves
// its principal. A secured session (NoPolicies unset) MUST name a principal
// via As, Roles or Groups — REQ:principal-selection fixes who `datatug serve`
// runs as for the whole session, never anonymously — so NewSession returns
// ErrNoPrincipal rather than silently starting an unidentified secured
// session. An Unrestricted session may omit a principal entirely.
func NewSession(o SessionOptions) (Session, error) {
	loaded, err := accesspolicies.Load(accesspolicies.LoadOptions{
		Dir:   o.PoliciesDir,
		Files: o.PolicyFiles,
		None:  o.NoPolicies,
	})
	if err != nil {
		return Session{}, err
	}
	var principal *access.Principal
	if o.As != "" || len(o.Roles) > 0 || len(o.Groups) > 0 {
		principal = &access.Principal{Roles: o.Roles, Groups: o.Groups}
		if o.As != "" {
			principal.ID = o.As
		}
	}
	if !o.NoPolicies && principal == nil {
		return Session{}, ErrNoPrincipal
	}
	return Session{Principal: principal, Policies: loaded, Unrestricted: o.NoPolicies}, nil
}
