package api

import (
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestSecurePolicyFingerprintIncludesPrincipalScope(t *testing.T) {
	configure := func(id string, roles, groups []string) string {
		ConfigureSecureSession(secureread.Session{
			Principal:    &access.Principal{ID: id, Roles: roles, Groups: groups},
			Unrestricted: true,
		}, map[string]string{"p": t.TempDir()}, Capabilities{})
		return SecurePolicyFingerprint()
	}
	base := configure("alice", []string{"reader"}, []string{"support"})
	for _, changed := range []string{
		configure("bob", []string{"reader"}, []string{"support"}),
		configure("alice", []string{"admin"}, []string{"support"}),
		configure("alice", []string{"reader"}, []string{"finance"}),
	} {
		if changed == base {
			t.Fatal("policy fingerprint did not change with principal scope")
		}
	}
	if reordered := configure("alice", []string{"reader", "admin"}, []string{"support", "finance"}); reordered != configure("alice", []string{"admin", "reader"}, []string{"finance", "support"}) {
		t.Fatal("policy fingerprint depends on role/group ordering")
	}
}
