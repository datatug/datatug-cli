package accesspolicies

import (
	"github.com/dal-go/dalgo/access"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSessionSplitFingerprintIncludesPoliciesInEitherReadProfile(t *testing.T) {
	principal := &access.Principal{ID: "alice"}
	require.Equal(t, "aa4b1a66b13f9414243dbabb59a2fbdef7f0623a974dfb72106b2ada4f4b4db9", Fingerprint(nil, true, principal))
	require.Equal(t, "03e7265f24175a4c89115bd0aeb2ca53f9e7c94e838227a618628cfc53dfcdfa", Fingerprint(nil, false, principal))
	require.Equal(t, "47d526b09efa85c268d4a84ee8ee58144988c00bc27be3ac39ce67fb7dcf0501", Fingerprint([]Loaded{{digest: [32]byte{1, 2, 3}}}, false, principal))
	decode := func(fields string) Loaded {
		loaded, err := DecodeLoaded([]byte("apiVersion: dalgo.io/access/v1\nkind: AccessPolicy\nmetadata: {name: customer}\ndefault: deny\nscopes:\n  - path: /Customer\n    rules:\n      - id: read\n        effect: allow\n        operations: [query]\n        fields: ["+fields+"]\n"), access.YAMLCodec{}, "customer.yaml")
		require.NoError(t, err)
		return loaded
	}
	first, second := decode("ID"), decode("ID, Name")
	require.NotEqual(t, Fingerprint([]Loaded{first}, false, principal), Fingerprint([]Loaded{first}, true, principal), "retain the distinct legacy raw-capability namespace")
	for _, free := range []bool{false, true} {
		require.NotEqual(t, Fingerprint([]Loaded{first}, free, principal), Fingerprint([]Loaded{second}, free, principal))
		require.NotEqual(t, Fingerprint([]Loaded{first, second}, free, principal), Fingerprint([]Loaded{second, first}, free, principal))
		require.NotEqual(t, Fingerprint(nil, true, principal), Fingerprint([]Loaded{first}, free, principal))
	}
}
