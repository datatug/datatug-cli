package licensing

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCoordinatorEntitlementCheckerFreeUsageNotice(t *testing.T) {
	t.Setenv("DATATUG_LICENSE_KEY", "")
	t.Setenv("DATATUG_LICENSE_FILE", "")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	previous := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = previous; _ = w.Close() })
	if err := NewCoordinatorEntitlementChecker("").CheckEntitlement(); err != nil {
		t.Fatalf("notice must not change existing no-key startup behavior: %v", err)
	}
	_ = w.Close()
	os.Stderr = previous
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	text := string(output)
	for _, want := range []string{"owner-only private", "shared public", "shared private", "https://datatug.app/en/pricing/", "does not establish project-plan entitlement"} {
		if !strings.Contains(text, want) {
			t.Errorf("startup guidance does not explain %q: %s", want, text)
		}
	}
	if strings.Contains(text, "community evaluation mode") || strings.Contains(text, "Centralized shared multi-runner deployments require") {
		t.Errorf("startup guidance must not impose a blanket coordinator paywall: %s", text)
	}
}

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

	// 5. Empty key but valid license with feature provided via env
	t.Setenv("DATATUG_LICENSE_KEY", validKey)
	checkerEnvValid := NewCoordinatorEntitlementChecker("")
	if err := checkerEnvValid.CheckEntitlement(); err != nil {
		t.Errorf("expected env valid license to pass entitlement check, got: %v", err)
	}

	// 6. Empty key but unentitled license provided via env
	t.Setenv("DATATUG_LICENSE_KEY", unentitledKey)
	checkerEnvUnentitled := NewCoordinatorEntitlementChecker("")
	if err := checkerEnvUnentitled.CheckEntitlement(); err == nil {
		t.Errorf("expected env unentitled license to fail entitlement check")
	}

	// 7. Empty key but invalid license in env
	t.Setenv("DATATUG_LICENSE_KEY", "invalid.key")
	checkerEnvInvalid := NewCoordinatorEntitlementChecker("")
	if err := checkerEnvInvalid.CheckEntitlement(); err == nil {
		t.Errorf("expected env invalid license to fail entitlement check")
	}
}
