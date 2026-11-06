// Package policyexceptions keeps the local policy exceptions of one DataTug
// project: time-boxed allow rules that the policy author adds to their own
// local policy, on this machine, for one role, one table of one source and an
// exact list of fields. An agent proposes one; the person applies, rejects or
// revokes it in their own terminal. An exception is never an approval that is
// valid anywhere else, and the policy files themselves are never changed: an
// applied, unexpired exception is overlaid on the policy document it names
// each time policies are loaded, and only while that document is byte for
// byte the one the exception was bound to.
//
// The store lives at <project>/.datatug/policy-exceptions/: one JSON file per
// exception and an append-only audit.jsonl, which records names and scopes
// and never a row value.
//
// Local Perimeter keeps an honest agent inside the lines. A real security
// boundary needs DB-native permissions or DataTug Cloud approvals.
package policyexceptions

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/callercontext"
)

// Kind is the kind every exception and audit event carries.
const Kind = "local-exception"

// Limits of an exception.
const (
	DefaultDuration = 15 * time.Minute
	MaxDuration     = 8 * time.Hour
	// PendingTTL is how long a proposal waits for the person.
	PendingTTL = 30 * time.Minute
	// RulePrefix starts the id of the rule and rule set an exception adds.
	RulePrefix = "exception-"
)

// HonestLimit is the sentence every exception screen carries.
const HonestLimit = "A local policy exception is a time-boxed rule you add to your own local policy on this machine; it is not an approval valid anywhere else. Local Perimeter keeps an honest agent inside the lines. A real security boundary needs DB-native permissions or DataTug Cloud approvals."

// State is where an exception is in its life.
type State string

// States of an exception.
const (
	StatePending  State = "pending"
	StateApplied  State = "applied"
	StateRejected State = "rejected"
	StateRevoked  State = "revoked"
	StateExpired  State = "expired"
	// StateEnded is a session exception whose session was ended explicitly.
	StateEnded State = "ended"
)

// Lifetimes of an exception.
const (
	// LifetimeSession limits an exception to the harness session that
	// proposed it (and its deadline): only a reader reporting the same
	// harness and session gets it. The session is self-reported, so this
	// narrows who can use an exception; it does not authenticate anyone.
	LifetimeSession = "session"
	// LifetimeDeadline limits an exception to its deadline only.
	LifetimeDeadline = "deadline"
)

// SessionBinding is the harness session a session exception is bound to.
type SessionBinding struct {
	Harness string `json:"harness"`
	Session string `json:"session"`
}

// Short is the binding as screens show it: "claude-code session 0f88afc6…".
func (s SessionBinding) Short() string {
	session := s.Session
	if len(session) > 8 {
		session = session[:8] + "…"
	}
	return s.Harness + " session " + session
}

// PolicyRef binds an exception to one policy document of the project.
type PolicyRef struct {
	Name   string `json:"name"`
	Path   string `json:"path"` // relative to the project directory, slash-separated
	SHA256 string `json:"sha256"`
}

// Exception is one stored local policy exception.
type Exception struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	ProjectID string `json:"projectId"`
	Env       string `json:"env"`
	Source    string `json:"source"`
	// SourceIdentity is SourceFingerprint of the source the exception was
	// proposed for, as the project resolved it: the same source id pointed at
	// another database is another source.
	SourceIdentity   string     `json:"sourceIdentity"`
	Policy           PolicyRef  `json:"policy"`
	Role             string     `json:"role"`
	Table            string     `json:"table"`
	Fields           []string   `json:"fields"`
	AppliedFields    []string   `json:"appliedFields,omitempty"`
	Reason           string     `json:"reason"`
	DurationSeconds  int64      `json:"durationSeconds"`
	State            State      `json:"state"`
	ProposedAt       time.Time  `json:"proposedAt"`
	PendingExpiresAt time.Time  `json:"pendingExpiresAt"`
	DecidedAt        *time.Time `json:"decidedAt,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	DecisionReason   string     `json:"decisionReason,omitempty"`
	ExpiryRecorded   bool       `json:"expiryRecorded,omitempty"`
	// Caller is the self-reported harness the proposal came from:
	// attribution only, never authority.
	Caller *callercontext.Caller `json:"caller,omitempty"`
	// Lifetime is session or deadline; an exception stored before lifetimes
	// existed is a deadline one.
	Lifetime string `json:"lifetime,omitempty"`
	// BoundSession is the harness session a session exception applies to.
	BoundSession *SessionBinding `json:"session,omitempty"`
}

// IsSession reports whether the exception is bound to a harness session.
func (e Exception) IsSession() bool { return e.Lifetime == LifetimeSession }

// LifetimeLine is the one line the apply and reject screens show about how
// long the exception lasts; until is the deadline when it is known.
func (e Exception) LifetimeLine(until *time.Time) string {
	deadline := "at most " + e.Duration().String() + " once applied"
	if until != nil {
		deadline = "until " + until.UTC().Format(time.RFC3339)
	}
	if !e.IsSession() {
		return "Lifetime: " + deadline
	}
	binding := "no session"
	if e.BoundSession != nil {
		binding = e.BoundSession.Short()
	}
	if until != nil {
		deadline = "at most " + deadline
	}
	return "Lifetime: this session only (" + binding + ", self-reported), " + deadline
}

// ProposedBy is the one line the apply and reject screens show about who
// proposed the exception.
func (e Exception) ProposedBy() string {
	if e.Caller == nil {
		return "Proposed by: not reported"
	}
	return "Proposed by: " + e.Caller.Line()
}

// Duration is the time an applied exception lasts.
func (e Exception) Duration() time.Duration { return time.Duration(e.DurationSeconds) * time.Second }

// Status is the state at now: a pending proposal past its wait, or an applied
// exception past its expiry, is expired.
func (e Exception) Status(now time.Time) State {
	switch {
	case e.State == StatePending && !now.Before(e.PendingExpiresAt):
		return StateExpired
	case e.State == StateApplied && (e.ExpiresAt == nil || !now.Before(*e.ExpiresAt)):
		return StateExpired
	}
	return e.State
}

// Active reports whether the exception adds its rule at now.
func (e Exception) Active(now time.Time) bool { return e.Status(now) == StateApplied }

// RuleID is the id of the rule (and of the rule set) the exception adds.
func (e Exception) RuleID() string { return RulePrefix + e.ID }

var idPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ValidID reports whether id is an exception id: 16 lower-case hex digits.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// randRead is crypto/rand.Read; a seam for tests.
var randRead = rand.Read

// NewID returns a random 64-bit id as 16 hex digits.
func NewID() (string, error) {
	var b [8]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("policy exception id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// CheckName refuses a name that is not one literal identifier: a pattern
// (*, ?, [], {}), a path separator, white space or a control character could
// widen an exception beyond the one table or field it names.
func CheckName(what, name string) error {
	if name == "" || len(name) > 128 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "*?[]{}/\\$,. \t\r\n\x00") {
		return fmt.Errorf("%s %q must be one literal name: no patterns (*, **, prefix*, {}), separators, dots or spaces", what, name)
	}
	return nil
}

// CheckDuration refuses a duration outside (0, MaxDuration].
func CheckDuration(d time.Duration) error {
	if d <= 0 || d > MaxDuration {
		return fmt.Errorf("--for must be more than 0 and at most %s", MaxDuration)
	}
	return nil
}

// Subset reports whether every name of sub is in set.
func Subset(sub, set []string) bool {
	for _, name := range sub {
		if !slices.Contains(set, name) {
			return false
		}
	}
	return true
}

// Fingerprint is the hex sha256 of a policy document's bytes.
func Fingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SourceFingerprint is the identity of a resolved source: the hex sha256 of
// the source reference the project resolves its id to (a SQLite or inGitDB
// path, or the environment variable of a server source).
func SourceFingerprint(sourceURL string) string { return Fingerprint([]byte(sourceURL)) }

// Scope renders the exact scope screen apply shows before the person
// confirms. expires is the expiry the decision records, fixed before the
// screen is shown.
func Scope(e Exception, fields []string, expires time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Local policy exception %s (%s)\n", e.ID, Kind)
	fmt.Fprintf(&b, "  Project:  %s\n", e.ProjectID)
	fmt.Fprintf(&b, "  Env:      %s\n", e.Env)
	fmt.Fprintf(&b, "  Source:   %s (identity sha256 %s)\n", e.Source, e.SourceIdentity)
	fmt.Fprintf(&b, "  Policy:   %s (%s, sha256 %s)\n", e.Policy.Name, e.Policy.Path, e.Policy.SHA256)
	fmt.Fprintf(&b, "  Role:     %s\n", e.Role)
	fmt.Fprintf(&b, "  Table:    %s (all rows of %s)\n", e.Table, e.Table)
	fmt.Fprintf(&b, "  Fields:   %s\n", strings.Join(fields, ", "))
	fmt.Fprintf(&b, "  Duration: %s, until %s\n", e.Duration(), expires.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "  %s\n", e.LifetimeLine(&expires))
	fmt.Fprintf(&b, "  Reason:   %s\n", e.Reason)
	fmt.Fprintf(&b, "  %s\n", e.ProposedBy())
	b.WriteString(HonestLimit + "\n")
	return b.String()
}

// ErrNotApplicable marks a policy document an exception cannot be added to:
// it binds no roles to rule sets.
var ErrNotApplicable = errors.New("the policy binds no roles to rule sets, so an exception cannot name a role in it")

// Applicable reports whether a document can carry exceptions: it is a
// principal policy set (rule sets bound to roles).
func Applicable(data []byte, codec access.Codec) error {
	var document access.Document
	if err := codec.Decode(bytes.NewReader(data), &document); err != nil {
		return err
	}
	if document.Bindings == nil || len(document.RuleSets) == 0 {
		return ErrNotApplicable
	}
	return nil
}

// Binding is what a read must match for an exception to apply: the project,
// its environment, the source id and identity, and the project-relative path
// of the policy document being loaded.
type Binding struct {
	ProjectID, Env, Source, SourceIdentity, PolicyPath string
	// Reader is the self-reported harness session of the reading process;
	// a session exception applies only to exactly the session it is bound to.
	Reader SessionBinding
}

func (b Binding) matches(e Exception) bool {
	if e.IsSession() && (e.BoundSession == nil || e.BoundSession.Session == "" || *e.BoundSession != b.Reader) {
		return false
	}
	return e.ProjectID == b.ProjectID && e.Env == b.Env && e.Source == b.Source && e.SourceIdentity == b.SourceIdentity && e.Policy.Path == b.PolicyPath
}

// Overlay adds to the policy document data one allow rule for each exception
// that is active at now, matches binding in full, and was bound to exactly
// these bytes. Each
// rule is its own rule set, named like the rule, bound to the exception's
// role: operations [query], path /<table>, fields as applied, and nothing
// else. It returns data itself and no ids when nothing applies, else the
// re-encoded document and the ids of the exceptions it added.
func Overlay(data []byte, codec access.Codec, binding Binding, exceptions []Exception, now time.Time) ([]byte, []string, error) {
	fingerprint := Fingerprint(data)
	var matching []Exception
	for _, e := range exceptions {
		if e.Active(now) && binding.matches(e) && e.Policy.SHA256 == fingerprint && len(e.AppliedFields) > 0 {
			matching = append(matching, e)
		}
	}
	if len(matching) == 0 {
		return data, nil, nil
	}
	var document access.Document
	if err := codec.Decode(bytes.NewReader(data), &document); err != nil {
		return nil, nil, err
	}
	if document.Bindings == nil || len(document.RuleSets) == 0 {
		return data, nil, nil
	}
	if document.Bindings.Roles == nil {
		document.Bindings.Roles = map[string][]string{}
	}
	var applied []string
	for _, e := range matching {
		name := e.RuleID()
		if _, taken := document.RuleSets[name]; taken {
			continue
		}
		document.RuleSets[name] = []access.DocumentScope{{
			Path: "/" + e.Table,
			Rules: []access.DocumentRule{{
				ID: name, Effect: "allow", Operations: []string{"query"}, Fields: append([]string(nil), e.AppliedFields...),
			}},
		}}
		document.Bindings.Roles[e.Role] = append(document.Bindings.Roles[e.Role], name)
		applied = append(applied, e.ID)
	}
	var out bytes.Buffer
	if err := codec.Encode(&out, document); err != nil {
		return nil, nil, err
	}
	return out.Bytes(), applied, nil
}
