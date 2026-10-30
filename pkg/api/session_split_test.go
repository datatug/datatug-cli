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
