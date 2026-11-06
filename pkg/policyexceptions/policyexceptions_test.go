package policyexceptions

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/access"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

const principalPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: agent
default: deny
ruleSets:
  agent:
    - path: /Customer
      rules:
        - id: customers
          effect: allow
          operations: [query]
          fields: [CustomerId]
bindings:
  roles:
    agent: [agent]
`

const scopesPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: global
default: deny
scopes:
  - path: /products
    rules:
      - id: all
        effect: allow
        operations: [query]
`

var demoBinding = Binding{ProjectID: "demo", Env: "local", Source: "chinook", SourceIdentity: SourceFingerprint("sqlite:///chinook.sqlite"), PolicyPath: "policies/agent.yaml"}

func applied(id string, expires time.Time) Exception {
	return Exception{
		ID: id, Kind: Kind, ProjectID: "demo", Env: "local", Source: "chinook", SourceIdentity: SourceFingerprint("sqlite:///chinook.sqlite"), Role: "agent", Table: "Employee",
		Policy: PolicyRef{Name: "agent", Path: "policies/agent.yaml", SHA256: Fingerprint([]byte(principalPolicy))},
		Fields: []string{"EmployeeId", "Title"}, AppliedFields: []string{"EmployeeId"}, DurationSeconds: 900,
		State: StateApplied, ProposedAt: t0, PendingExpiresAt: t0.Add(PendingTTL), ExpiresAt: &expires,
	}
}

func TestStatus(t *testing.T) {
	pending := Exception{State: StatePending, PendingExpiresAt: t0.Add(time.Minute)}
	assert.Equal(t, StatePending, pending.Status(t0))
	assert.Equal(t, StateExpired, pending.Status(t0.Add(time.Minute)))
	e := applied("0123456789abcdef", t0.Add(time.Minute))
	assert.True(t, e.Active(t0))
	assert.False(t, e.Active(t0.Add(time.Minute)))
	e.ExpiresAt = nil
	assert.Equal(t, StateExpired, e.Status(t0))
	assert.Equal(t, StateRejected, Exception{State: StateRejected}.Status(t0))
	assert.Equal(t, 15*time.Minute, applied("0123456789abcdef", t0).Duration())
	assert.Equal(t, "exception-0123456789abcdef", applied("0123456789abcdef", t0).RuleID())
}

func TestIDs(t *testing.T) {
	id, err := NewID()
	require.NoError(t, err)
	assert.True(t, ValidID(id))
	assert.False(t, ValidID("../x"))
	assert.False(t, ValidID("0123456789ABCDEF"))
	original := randRead
	t.Cleanup(func() { randRead = original })
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	_, err = NewID()
	assert.ErrorContains(t, err, "no entropy")
}

func TestCheckName(t *testing.T) {
	for _, name := range []string{"Employee", "support-notes", "Employee_1"} {
		assert.NoError(t, CheckName("--table", name), name)
	}
	for _, name := range []string{"", "*", "**", "Emp*", "{a,b}", "a/b", "a.b", " a", "a b", "a?", "[a]", strings.Repeat("a", 129)} {
		assert.Error(t, CheckName("--table", name), name)
	}
}

func TestCheckDurationAndSubset(t *testing.T) {
	assert.NoError(t, CheckDuration(MaxDuration))
	assert.Error(t, CheckDuration(0))
	assert.Error(t, CheckDuration(MaxDuration+time.Second))
	assert.True(t, Subset([]string{"a"}, []string{"a", "b"}))
	assert.False(t, Subset([]string{"c"}, []string{"a", "b"}))
}

func TestScope(t *testing.T) {
	e := applied("0123456789abcdef", t0)
	e.Reason = "escalations"
	scope := Scope(e, []string{"EmployeeId", "Title"}, t0.Add(15*time.Minute))
	for _, want := range []string{"0123456789abcdef", "all rows of Employee", "EmployeeId, Title", "until 2026-11-06T12:15:00Z", "escalations", HonestLimit, e.Policy.SHA256, "Env:      local", e.SourceIdentity} {
		assert.Contains(t, scope, want)
	}
}

func TestApplicable(t *testing.T) {
	assert.NoError(t, Applicable([]byte(principalPolicy), access.YAMLCodec{}))
	assert.ErrorIs(t, Applicable([]byte(scopesPolicy), access.YAMLCodec{}), ErrNotApplicable)
	assert.Error(t, Applicable([]byte("{"), access.YAMLCodec{}))
}

func decodePolicy(t *testing.T, data []byte) access.Policy {
	t.Helper()
	policy, err := access.DecodePolicy(bytes.NewReader(data), access.YAMLCodec{})
	require.NoError(t, err)
	return policy
}

func TestOverlayAddsOneRule(t *testing.T) {
	data := []byte(principalPolicy)
	e := applied("0123456789abcdef", t0.Add(time.Hour))
	out, ids, err := Overlay(data, access.YAMLCodec{}, demoBinding, []Exception{e}, t0)
	require.NoError(t, err)
	assert.Equal(t, []string{e.ID}, ids)
	var document access.Document
	require.NoError(t, access.YAMLCodec{}.Decode(bytes.NewReader(out), &document))
	assert.Equal(t, []string{"agent", e.RuleID()}, document.Bindings.Roles["agent"])
	set := document.RuleSets[e.RuleID()]
	require.Len(t, set, 1)
	assert.Equal(t, "/Employee", set[0].Path)
	require.Len(t, set[0].Rules, 1)
	rule := set[0].Rules[0]
	assert.Equal(t, access.DocumentRule{ID: e.RuleID(), Effect: "allow", Operations: []string{"query"}, Fields: []string{"EmployeeId"}}, rule)
	decodePolicy(t, out)
}

func TestOverlayContributesNothing(t *testing.T) {
	data := []byte(principalPolicy)
	live := applied("0123456789abcdef", t0.Add(time.Hour))
	cases := map[string]func(*Exception){
		"expired":       func(e *Exception) { e.ExpiresAt = &t0 },
		"pending":       func(e *Exception) { e.State = StatePending },
		"other project": func(e *Exception) { e.ProjectID = "other" },
		"other env":     func(e *Exception) { e.Env = "prod" },
		"other source":  func(e *Exception) { e.Source = "support" },
		"other db":      func(e *Exception) { e.SourceIdentity = SourceFingerprint("sqlite:///elsewhere.sqlite") },
		"other path":    func(e *Exception) { e.Policy.Path = "policies/other.yaml" },
		"policy edited": func(e *Exception) { e.Policy.SHA256 = Fingerprint([]byte("old")) },
		"no fields":     func(e *Exception) { e.AppliedFields = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := live
			change(&e)
			out, ids, err := Overlay(data, access.YAMLCodec{}, demoBinding, []Exception{e}, t0)
			require.NoError(t, err)
			assert.Empty(t, ids)
			assert.Equal(t, data, out)
		})
	}
}

func TestOverlayEdges(t *testing.T) {
	t.Run("not a principal set", func(t *testing.T) {
		data := []byte(scopesPolicy)
		e := applied("0123456789abcdef", t0.Add(time.Hour))
		e.Policy.SHA256 = Fingerprint(data)
		out, ids, err := Overlay(data, access.YAMLCodec{}, demoBinding, []Exception{e}, t0)
		require.NoError(t, err)
		assert.Empty(t, ids)
		assert.Equal(t, data, out)
	})
	t.Run("undecodable", func(t *testing.T) {
		data := []byte("{")
		e := applied("0123456789abcdef", t0.Add(time.Hour))
		e.Policy.SHA256 = Fingerprint(data)
		_, _, err := Overlay(data, access.YAMLCodec{}, demoBinding, []Exception{e}, t0)
		assert.Error(t, err)
	})
	t.Run("groups only and a taken rule set", func(t *testing.T) {
		data := []byte(strings.Replace(principalPolicy, "  roles:\n    agent: [agent]\n", "  groups:\n    staff: [agent]\n", 1))
		e := applied("0123456789abcdef", t0.Add(time.Hour))
		e.Policy.SHA256 = Fingerprint(data)
		taken := applied("fedcba9876543210", t0.Add(time.Hour))
		taken.Policy.SHA256 = e.Policy.SHA256
		data = []byte(strings.Replace(string(data), "ruleSets:\n", "ruleSets:\n  exception-fedcba9876543210:\n    - path: /Track\n      rules:\n        - {id: t, effect: allow, operations: [query]}\n", 1))
		e.Policy.SHA256, taken.Policy.SHA256 = Fingerprint(data), Fingerprint(data)
		out, ids, err := Overlay(data, access.YAMLCodec{}, demoBinding, []Exception{e, taken}, t0)
		require.NoError(t, err)
		assert.Equal(t, []string{e.ID}, ids)
		assert.Contains(t, string(out), "exception-0123456789abcdef")
	})
	t.Run("encode failure", func(t *testing.T) {
		data := []byte(principalPolicy)
		e := applied("0123456789abcdef", t0.Add(time.Hour))
		_, _, err := Overlay(data, failingEncodeCodec{}, demoBinding, []Exception{e}, t0)
		assert.ErrorContains(t, err, "cannot encode")
	})
}

type failingEncodeCodec struct{ access.YAMLCodec }

func (failingEncodeCodec) Encode(io.Writer, access.Document) error {
	return errors.New("cannot encode")
}
