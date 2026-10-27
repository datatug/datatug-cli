package watchdaemon

import (
	"errors"
	"testing"
)

type mockEntitlementChecker struct {
	err error
}

func (m *mockEntitlementChecker) CheckEntitlement() error {
	return m.err
}

func TestServer_CheckEntitlement(t *testing.T) {
	// 1. Community / standalone mode (no entitlement checker configured)
	sDefault := &Server{}
	if err := sDefault.CheckEntitlement(); err != nil {
		t.Errorf("expected default community mode to return nil error, got: %v", err)
	}

	// 2. Entitled mock
	sEntitled := &Server{Entitlement: &mockEntitlementChecker{err: nil}}
	if err := sEntitled.CheckEntitlement(); err != nil {
		t.Errorf("expected entitled mock to pass, got: %v", err)
	}

	// 3. Unentitled mock
	customErr := errors.New("unauthorized tier")
	sUnentitled := &Server{Entitlement: &mockEntitlementChecker{err: customErr}}
	if err := sUnentitled.CheckEntitlement(); !errors.Is(err, customErr) {
		t.Errorf("expected unentitled mock to return customErr, got: %v", err)
	}
}
