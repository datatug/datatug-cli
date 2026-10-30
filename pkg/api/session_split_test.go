package api

import (
	"context"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/investigation"
	"github.com/stretchr/testify/require"
)

func TestSessionSplitOwnershipAndReadAuthorityRemainIndependent(t *testing.T) {
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	for _, owner := range []bool{false, true} {
		for _, free := range []bool{false, true} {
			profile := secureread.DatabasePolicyGoverned
			if free {
				profile = secureread.DatabasePolicyFreeReads
			}
			s := secureread.Session{Principal: &access.Principal{ID: "alice"}, LocalProjectOwner: owner, DatabaseProfile: profile}
			ConfigureSecureSession(s, map[string]string{"p": "/p"}, Capabilities{AllowWrites: true})
			require.Equal(t, owner, SecureLocalProjectOwner())
			require.Equal(t, free, SecureSessionUnrestricted())
			_, _, allowed := localBoardWriter()
			require.Equal(t, owner, allowed)
			require.Equal(t, owner, canCreatePublicOVDBConnection())
			_, env, _, _, err := envPolicy("p")
			require.NoError(t, err)
			require.Equal(t, owner, env.CanCreateEnvironment)
			require.Equal(t, owner, env.CanWriteOrder)
			err = AuthorizeProjectQueryWrite(context.Background(), "p", "q", access.Set)
			require.Equal(t, owner, err == nil)
			err = authorizeMeaningWrite(context.Background(), "p")
			require.Equal(t, owner, err == nil)
			err = authorizeItemTagsWrite(context.Background(), ItemTagsSelector{ProjectID: "p", Kind: "board", ID: "b"})
			require.Equal(t, owner, err == nil)
			executor, ok := SecureExecutor()
			require.True(t, ok)
			err = executor.Authorize(context.Background(), nil, nil)
			require.Equal(t, free, err == nil)
			scope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "p", Environment: "prod"}
			fact := investigation.Fact{Scope: &scope, Entity: "customers", Field: "name"}
			visibility := incidentFactVisibility(context.Background(), fact, secureSession, map[string]string{"p": "/p"})
			if free {
				require.Equal(t, incidents.FactVisible, visibility)
			} else {
				require.Equal(t, incidents.FactHidden, visibility)
			}
			require.Equal(t, accesspolicies.Fingerprint(nil, free, s.Principal), SecurePolicyFingerprint())
			want := "03e7265f24175a4c89115bd0aeb2ca53f9e7c94e838227a618628cfc53dfcdfa"
			if free {
				want = "aa4b1a66b13f9414243dbabb59a2fbdef7f0623a974dfb72106b2ada4f4b4db9"
			}
			require.Equal(t, want, SecurePolicyFingerprint(), "keep existing stored-value fingerprint bytes")
		}
	}
}

func TestSessionSplitAnonymousIdentityIsNotAnOwnerOrDatabaseGrant(t *testing.T) {
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	for _, anonymous := range []bool{false, true} {
		ConfigureSecureSession(secureread.Session{DatabaseProfile: secureread.DatabasePolicyGoverned, AllowAnonymousLocalIdentity: anonymous}, nil, Capabilities{AllowWrites: true})
		token, err := AuthTokenFromHTTPRequest(nil, true)
		require.NoError(t, err)
		require.Equal(t, anonymous, token != nil)
		require.Error(t, AuthorizeProjectQueryWrite(context.Background(), "p", "q", access.Set))
		executor, _ := SecureExecutor()
		require.Error(t, executor.Authorize(context.Background(), nil, nil))
	}
}

func TestSessionSplitInvalidConfigurationDropsAllConfiguredAuthority(t *testing.T) {
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	principal := &access.Principal{ID: "alice"}
	bad := secureread.Session{Principal: principal, LocalProjectOwner: true, AllowAnonymousLocalIdentity: true, DatabaseProfile: secureread.DatabaseProfile(255), Policies: []accesspolicies.Loaded{{Policy: access.MustPolicy("allow", access.Root(access.Allow(access.ReadWrite)))}}}
	ConfigureSecureSession(bad, map[string]string{"p": "/p"}, Capabilities{AllowWrites: true, AllowOpaqueSQL: true, EnvironmentMetadataPolicies: map[string]EnvironmentMetadataPolicy{"p": {PrincipalID: "alice", CanReadOrder: true, CanWriteOrder: true}}})
	require.Empty(t, SecurityContextID())
	require.False(t, SecureLocalProjectOwner())
	require.Empty(t, SecurePrincipalID())
	token, err := AuthTokenFromHTTPRequest(nil, true)
	require.NoError(t, err)
	require.Nil(t, token)
	require.Error(t, AuthorizeProjectQueryWrite(context.Background(), "p", "q", access.Set))
	require.Error(t, authorizeMeaningWrite(context.Background(), "p"))
	require.Error(t, authorizeItemTagsWrite(context.Background(), ItemTagsSelector{ProjectID: "p", Kind: "board", ID: "b"}))
	_, _, allowed := localBoardWriter()
	require.False(t, allowed)
	require.False(t, canCreatePublicOVDBConnection())
	_, _, _, _, err = envPolicy("p")
	require.Error(t, err)
	executor, _ := SecureExecutor()
	require.ErrorIs(t, executor.Authorize(context.Background(), nil, nil), secureread.ErrInvalidSession)
}

func TestSessionSplitInvalidSessionCannotRegainAPIOrFactAuthority(t *testing.T) {
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	session, err := secureread.NormalizeSession(secureread.Session{DatabaseProfile: secureread.DatabaseProfile(255)})
	require.ErrorIs(t, err, secureread.ErrInvalidSession)
	session.Principal = &access.Principal{ID: "alice"}
	session.Policies = []accesspolicies.Loaded{{Policy: access.MustPolicy("allow", access.Root(access.Allow(access.ReadWrite)))}}
	session.DatabaseProfile = secureread.DatabasePolicyFreeReads
	session.LocalProjectOwner, session.AllowAnonymousLocalIdentity, session.AllowOpaqueSQL = true, true, true
	scope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "p", Environment: "prod"}
	fact := investigation.Fact{Scope: &scope, Entity: "Customer", Field: "ID"}
	require.Equal(t, incidents.FactHidden, incidentFactVisibility(context.Background(), fact, session, map[string]string{"p": "/p"}))
	ConfigureSecureSession(session, map[string]string{"p": "/p"}, Capabilities{AllowWrites: true, AllowOpaqueSQL: true})
	require.Empty(t, SecurityContextID())
	require.Empty(t, SecurePrincipalID())
	require.False(t, SecureLocalProjectOwner())
	require.False(t, SecureSessionUnrestricted())
	token, err := AuthTokenFromHTTPRequest(nil, true)
	require.NoError(t, err)
	require.Nil(t, token)
	require.Error(t, AuthorizeProjectQueryWrite(context.Background(), "p", "q", access.Set))
	require.False(t, GetCapabilities().AllowWrites)
	require.False(t, GetCapabilities().AllowOpaqueSQL)
	executor, _ := SecureExecutor()
	require.ErrorIs(t, executor.CanReadWholeCollection(context.Background(), "Customer"), secureread.ErrInvalidSession)
}

func TestSessionSplitPolicyFreeProfilePreservesIncidentPolicies(t *testing.T) {
	scope := investigation.ProjectScope{StoreID: LocalStoreID, ProjectID: "p", Environment: "prod"}
	fact := investigation.Fact{Scope: &scope, Entity: "Customer", Field: "ID"}
	for _, tc := range []struct {
		name, path, rule string
		want             incidents.FactVisibility
	}{
		{"allowed", "/Customer", "", incidents.FactVisible},
		{"denied", "/Other", "", incidents.FactHidden},
		{"field restricted", "/Customer", "        fields: [Name]\n", incidents.FactValueRedacted},
		{"row restricted", "/Customer", "        where: {op: '==', left: {field: ID}, right: {value: 5}}\n", incidents.FactHidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: incident-policy}\ndefault: deny\nscopes:\n  - path: " + tc.path + "\n    rules:\n      - id: read\n        effect: allow\n        operations: [query]\n" + tc.rule
			loaded, err := accesspolicies.DecodeLoaded([]byte(doc), access.YAMLCodec{}, "incident-policy.yaml")
			require.NoError(t, err)
			for _, profile := range []secureread.DatabaseProfile{secureread.DatabasePolicyGoverned, secureread.DatabasePolicyFreeReads} {
				session := secureread.Session{DatabaseProfile: profile, Policies: []accesspolicies.Loaded{loaded}}
				require.Equal(t, tc.want, incidentFactVisibility(context.Background(), fact, session, map[string]string{"p": "/p"}), "profile %v", profile)
			}
		})
	}
}
