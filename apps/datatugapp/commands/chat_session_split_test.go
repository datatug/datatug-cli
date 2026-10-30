package commands

import (
	"context"
	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSessionSplitChatJoinPreflightsExistingRootWithLoadedPolicies(t *testing.T) {
	path := covCSQLiteFile(t, "join.sqlite", "CREATE TABLE Parent(id INTEGER PRIMARY KEY)", "CREATE TABLE Child(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES Parent(id))")
	policy, err := accesspolicies.DecodeLoaded([]byte(`apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: row-restricted-child}
default: deny
scopes:
  - path: /Child
    rules:
      - id: child
        effect: allow
        operations: [query]
        where: {op: '>', left: {field: id}, right: {value: 0}}
  - path: /Parent
    rules:
      - id: parent
        effect: allow
        operations: [query]
`), access.YAMLCodec{}, "join.yaml")
	require.NoError(t, err)
	session := secureread.Session{DatabaseProfile: secureread.DatabasePolicyFreeReads, Policies: []accesspolicies.Loaded{policy}}
	source := "sqlite://" + path
	app, closeDB, err := loadChatJoinApplication(context.Background(), source, secureread.NewExecutor(session), session)
	require.NoError(t, err)
	require.NotNil(t, closeDB)
	defer closeDB()
	require.True(t, app.Secure, "loaded policies must enable the existing-root preflight")
	candidates, err := app.Candidates(context.Background(), chat.RecordSet{Source: source, TugQL: "from: {name: Child}\nlimit: 10\n"})
	require.NoError(t, err)
	require.Empty(t, candidates, "a JOIN must not expand a row-restricted existing root")
}

func TestSessionSplitChatJoinRejectsInvalidSessionBeforeMetadataOpen(t *testing.T) {
	invalid := secureread.Session{DatabaseProfile: secureread.DatabaseProfile(255)}
	sticky, err := secureread.NormalizeSession(invalid)
	require.ErrorIs(t, err, secureread.ErrInvalidSession)
	sticky.DatabaseProfile = secureread.DatabasePolicyFreeReads
	for _, session := range []secureread.Session{invalid, sticky} {
		app, closeDB, err := loadChatJoinApplication(context.Background(), "sqlite:///must-not-open", secureread.NewExecutor(session), session)
		require.ErrorIs(t, err, secureread.ErrInvalidSession)
		require.Nil(t, app)
		require.Nil(t, closeDB)
	}
}
