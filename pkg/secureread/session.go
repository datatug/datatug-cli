package secureread

import (
	"fmt"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
)

// DatabaseProfile classifies the existing database-read policy mode. It never
// conveys project ownership or permission to perform a database mutation.
type DatabaseProfile uint8

const (
	// DatabaseProfileUnspecified accepts legacy literals at construction only.
	DatabaseProfileUnspecified DatabaseProfile = iota
	// DatabasePolicyGoverned requires the existing policy evaluator to grant reads.
	DatabasePolicyGoverned
	// DatabasePolicyFreeReads permits today's no-policy reads. Loaded policies
	// and the existing native safeguards still apply.
	DatabasePolicyFreeReads
)

// PolicyFreeReads reports the read profile independently of project ownership.
func (p DatabaseProfile) PolicyFreeReads() bool { return p == DatabasePolicyFreeReads }

// Session fixes identity, project ownership and the database read profile.
// Database requests use DatabaseProfile and the existing policy evaluator;
// project-file and asset operations use LocalProjectOwner.
type Session struct {
	Principal         *access.Principal
	Policies          []accesspolicies.Loaded
	LocalProjectOwner bool
	DatabaseProfile   DatabaseProfile
	// AllowAnonymousLocalIdentity preserves the local read authentication fallback.
	// It grants neither project ownership nor database access.
	AllowAnonymousLocalIdentity bool
	// Unrestricted is translated only when DatabaseProfile is unspecified. Its
	// output mirrors the read profile; execution never uses this field.
	// Deprecated: use the separate ownership, database and identity fields.
	Unrestricted bool
	// AllowOpaqueSQL is the existing native-query capability, never a data-write grant.
	AllowOpaqueSQL   bool
	configurationErr error
}

// NormalizeSession translates legacy construction input and validates profiles.
// On error the returned session retains no authority. Explicit profiles never
// rederive ownership or identity from the legacy compatibility echo.
func NormalizeSession(s Session) (Session, error) {
	if s.configurationErr != nil {
		return Session{configurationErr: s.configurationErr}, s.configurationErr
	}
	switch s.DatabaseProfile {
	case DatabaseProfileUnspecified:
		s.DatabaseProfile = DatabasePolicyGoverned
		if s.Unrestricted {
			s.DatabaseProfile = DatabasePolicyFreeReads
			s.LocalProjectOwner = true
			s.AllowAnonymousLocalIdentity = true
		}
	case DatabasePolicyGoverned:
		if s.Unrestricted {
			err := fmt.Errorf("%w: legacy no-policy input contradicts the explicit policy-governed profile", ErrInvalidSession)
			return Session{configurationErr: err}, err
		}
	case DatabasePolicyFreeReads:
	default:
		err := fmt.Errorf("%w: unknown database profile", ErrInvalidSession)
		return Session{configurationErr: err}, err
	}
	s.Unrestricted = s.DatabaseProfile.PolicyFreeReads()
	return s, nil
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
	profile := DatabasePolicyGoverned
	if o.NoPolicies {
		profile = DatabasePolicyFreeReads
	}
	return NormalizeSession(Session{Principal: principal, Policies: loaded,
		LocalProjectOwner: o.NoPolicies, DatabaseProfile: profile,
		AllowAnonymousLocalIdentity: o.NoPolicies})
}
