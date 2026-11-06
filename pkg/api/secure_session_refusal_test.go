package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func resetRefusalTestSession(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := ConfigureSecureSessionChecked(secureread.Session{}, nil, Capabilities{}); err != nil {
			t.Error(err)
		}
	})
}
func TestConfigureSecureSessionRegistryFailureDropsAllAuthority(t *testing.T) {
	resetRefusalTestSession(t)
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true, As: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.Mkdir(filepath.Join(root, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".datatug", "security.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	caps := Capabilities{AllowWrites: true, AllowOpaqueSQL: true, AllowLiveConnections: true, EnvironmentMetadataPolicies: map[string]EnvironmentMetadataPolicy{"p": {PrincipalID: "operator", CanReadOrder: true, CanWriteOrder: true}}}
	err = ConfigureSecureSessionChecked(session, map[string]string{"p": root}, caps)
	if !errors.Is(err, secureread.ErrSourceBinding) {
		t.Fatal("configuration failure was swallowed", err)
	}
	// The original void entry point must install the same refused state.
	ConfigureSecureSession(session, map[string]string{"p": root}, caps)
	if SecureLocalProjectOwner() || SecureSessionUnrestricted() || SecurePrincipalID() != "" || SecurityContextID() != "" {
		t.Fatal("invalid session retained owner/read/identity authority")
	}
	if !reflect.DeepEqual(GetCapabilities(), Capabilities{}) {
		t.Fatal("invalid configuration retained capabilities", GetCapabilities())
	}
	if !sessionConfigured() || len(SecureConfiguredProjectIDs()) != 0 {
		t.Fatal("failed configuration must serve an empty explicit registry")
	}
	if _, err = ProjectStoreFor("p"); err == nil {
		t.Fatal("failed configuration regained project fallback")
	}
	token, err := AuthTokenFromHTTPRequest(nil, true)
	if err != nil || token != nil {
		t.Fatal("invalid configuration retained anonymous/identified token", token, err)
	}
	executor, ok := SecureExecutor()
	if !ok || executor == nil {
		t.Fatal("missing failclosed executor")
	}
	query := dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().SelectIntoRecord(nil)
	if err = executor.Authorize(context.Background(), query, nil); !errors.Is(err, secureread.ErrSourceBinding) {
		t.Fatal("executor lost configuration refusal", err)
	}
}
func TestConfigureSecureSessionUnboundPositiveRetainsExplicitAuthority(t *testing.T) {
	resetRefusalTestSession(t)
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true, As: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = ConfigureSecureSessionChecked(session, map[string]string{"p": root}, Capabilities{AllowWrites: true, AllowOpaqueSQL: true}); err != nil {
		t.Fatal(err)
	}
	if !SecureLocalProjectOwner() || !SecureSessionUnrestricted() || SecurePrincipalID() != "operator" || SecurityContextID() == "" {
		t.Fatal("valid legacy authority changed")
	}
	if !GetCapabilities().AllowWrites || !GetCapabilities().AllowOpaqueSQL {
		t.Fatal("valid explicit capabilities removed")
	}
	if path, ok := ProjectDir("p"); !ok || path != root {
		t.Fatal("valid project registration lost", path, ok)
	}
	executor, ok := SecureExecutor()
	if !ok || executor == nil {
		t.Fatal("valid executor absent")
	}
	query := dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().SelectIntoRecord(nil)
	if err = executor.Authorize(context.Background(), query, nil); err != nil {
		t.Fatal("valid unbound policyfree read refused", err)
	}
}
