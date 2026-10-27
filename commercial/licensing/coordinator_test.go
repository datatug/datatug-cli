package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestCoordinatorEntitlementChecker(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	now := time.Now()

	// 1. Valid license with central_watch_coordinator
	validClaims := Claims{
		ID:           "test-lic",
		Organization: "Test Org",
		IssuedAt:     now.Add(-time.Hour),
		ExpiresAt:    now.Add(24 * time.Hour),
		Tier:         TierPro,
		Features:     []string{FeatureCentralWatchCoordinator},
	}
	validKey, err := SignLicense(validClaims, priv)
	if err != nil {
		t.Fatal(err)
	}

	checkerWithValid := NewCoordinatorEntitlementChecker(validKey)
	if err := checkerWithValid.CheckEntitlement(); err != nil {
		t.Errorf("expected valid license to pass entitlement check, got: %v", err)
	}

	// 2. License without central_watch_coordinator
	unentitledClaims := Claims{
		ID:           "test-lic-2",
		Organization: "Test Org",
		IssuedAt:     now.Add(-time.Hour),
		ExpiresAt:    now.Add(24 * time.Hour),
		Tier:         TierPro,
		Features:     []string{FeatureSMSNotifications},
	}
	unentitledKey, _ := SignLicense(unentitledClaims, priv)
	checkerUnentitled := NewCoordinatorEntitlementChecker(unentitledKey)
	if err := checkerUnentitled.CheckEntitlement(); err == nil {
		t.Errorf("expected error for unentitled license")
	}

	// 3. Invalid license key format
	checkerInvalid := NewCoordinatorEntitlementChecker("invalid.key")
	if err := checkerInvalid.CheckEntitlement(); err == nil {
		t.Errorf("expected error for malformed license key")
	}

	// 4. Standalone / evaluation mode (empty key)
	t.Setenv("DATATUG_LICENSE_KEY", "")
	t.Setenv("DATATUG_LICENSE_FILE", "")
	checkerEval := NewCoordinatorEntitlementChecker("")
	if err := checkerEval.CheckEntitlement(); err != nil {
		t.Errorf("expected evaluation mode (empty license) to succeed, got: %v", err)
	}
}
