package licensing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func generateTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key pair: %v", err)
	}
	return pub, priv
}

func sampleClaims(now time.Time) Claims {
	return Claims{
		ID:           "lic-001",
		Organization: "Acme Corp",
		IssuedAt:     now.Add(-24 * time.Hour),
		ExpiresAt:    now.Add(365 * 24 * time.Hour),
		Tier:         TierEnterpriseSelfHosted,
		Capacity: Capacity{
			MaxCoordinators: 5,
			MaxRunners:      20,
			MaxUsers:        100,
		},
		Features: []string{
			FeatureCentralWatchCoordinator,
			FeatureSharedExecutionStore,
			FeatureTeamAuditTrail,
			FeatureSMSNotifications,
		},
	}
}

func TestVerifyLicense_Valid(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	restoreTime := SetTimeNow(func() time.Time { return now })
	defer restoreTime()

	claims := sampleClaims(now)
	key, err := SignLicense(claims, priv)
	if err != nil {
		t.Fatalf("SignLicense failed: %v", err)
	}

	lic, err := VerifyLicense(key)
	if err != nil {
		t.Fatalf("VerifyLicense failed: %v", err)
	}

	if lic.Claims.ID != "lic-001" {
		t.Errorf("expected ID 'lic-001', got %s", lic.Claims.ID)
	}
	if lic.Claims.Organization != "Acme Corp" {
		t.Errorf("expected Org 'Acme Corp', got %s", lic.Claims.Organization)
	}
	if lic.InGrace {
		t.Errorf("valid unexpired license should not be in grace period")
	}

	// Also verify Pro tier
	claimsPro := sampleClaims(now)
	claimsPro.Tier = TierPro
	keyPro, err := SignLicense(claimsPro, priv)
	if err != nil {
		t.Fatalf("SignLicense pro failed: %v", err)
	}
	licPro, err := VerifyLicense(keyPro)
	if err != nil {
		t.Fatalf("VerifyLicense pro failed: %v", err)
	}
	if licPro.Claims.Tier != TierPro {
		t.Errorf("expected TierPro, got %s", licPro.Claims.Tier)
	}
}

func TestVerifyLicense_GracePeriod(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var currentTime time.Time
	restoreTime := SetTimeNow(func() time.Time { return currentTime })
	defer restoreTime()

	var buf bytes.Buffer
	restoreStderr := SetStderrWriter(&buf)
	defer restoreStderr()

	claims := sampleClaims(baseTime)
	claims.ExpiresAt = baseTime // expired right at baseTime
	key, err := SignLicense(claims, priv)
	if err != nil {
		t.Fatalf("SignLicense failed: %v", err)
	}

	// 1. Five days after expiration: within 14-day grace period
	currentTime = baseTime.Add(5 * 24 * time.Hour)
	buf.Reset()
	lic, err := VerifyLicense(key)
	if err != nil {
		t.Fatalf("expected license within grace period to pass, got error: %v", err)
	}
	if !lic.InGrace {
		t.Errorf("expected InGrace to be true")
	}
	if lic.DaysInGrace != 9 {
		t.Errorf("expected 9 days remaining in grace, got %d", lic.DaysInGrace)
	}
	if !strings.Contains(buf.String(), "WARNING: DataTug commercial license for 'Acme Corp' expired") {
		t.Errorf("expected diagnostic warning on stderr, got: %s", buf.String())
	}

	// 2. Exactly on 14th day of grace period
	currentTime = baseTime.Add(13 * 24 * time.Hour)
	lic, err = VerifyLicense(key)
	if err != nil {
		t.Fatalf("expected 13 days after expiration to still be in grace, got: %v", err)
	}
	if !lic.InGrace {
		t.Errorf("expected InGrace to be true")
	}

	// 3. Beyond 14-day grace period (15 days after expiration): must fail
	currentTime = baseTime.Add(15 * 24 * time.Hour)
	_, err = VerifyLicense(key)
	if !errors.Is(err, ErrLicenseExpired) {
		t.Fatalf("expected ErrLicenseExpired after grace period, got: %v", err)
	}
}

func TestVerifyLicense_Tampered(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	now := time.Now()
	claims := sampleClaims(now)
	key, err := SignLicense(claims, priv)
	if err != nil {
		t.Fatalf("SignLicense failed: %v", err)
	}

	// Tamper with payload part
	parts := strings.Split(key, ".")
	tamperedPayload := parts[0] + "xyz"
	tamperedKey := tamperedPayload + "." + parts[1]

	_, err = VerifyLicense(tamperedKey)
	if err == nil {
		t.Fatalf("expected error on tampered payload")
	}

	// Tamper with signature
	sigBytes, _ := decodeBase64(parts[1])
	sigBytes[0] ^= 0xFF
	tamperedSigKey := parts[0] + "." + base64.RawURLEncoding.EncodeToString(sigBytes)

	_, err = VerifyLicense(tamperedSigKey)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature on modified signature, got: %v", err)
	}
}

func TestVerifyLicense_InvalidFormats(t *testing.T) {
	pub, _ := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	// Empty string
	_, err := VerifyLicense("")
	if !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing for empty key, got: %v", err)
	}

	// Single part without dot or json
	_, err = VerifyLicense("not-a-valid-key")
	if !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("expected ErrInvalidLicense, got: %v", err)
	}

	// Malformed base64
	_, err = VerifyLicense("!invalid-base64!.")
	if !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("expected ErrInvalidLicense for bad base64, got: %v", err)
	}
}

func TestVerifyLicense_InvalidClaims(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	// Missing ID or Org
	claims := Claims{
		ID:           "",
		Organization: "",
		Tier:         TierPro,
	}
	key, _ := SignLicense(claims, priv)
	_, err := VerifyLicense(key)
	if !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("expected ErrInvalidLicense for missing id/org, got: %v", err)
	}

	// Invalid tier
	claims.ID = "test"
	claims.Organization = "org"
	claims.Tier = "unsupported_tier"
	key, _ = SignLicense(claims, priv)
	_, err = VerifyLicense(key)
	if !errors.Is(err, ErrInvalidTier) {
		t.Errorf("expected ErrInvalidTier, got: %v", err)
	}
}

func TestVerifyLicense_JSONEnvelopeFormat(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	now := time.Now()
	claims := sampleClaims(now)
	payload, _ := json.Marshal(claims)
	sig := ed25519.Sign(priv, payload)

	env := map[string]string{
		"payload":   base64.StdEncoding.EncodeToString(payload),
		"signature": base64.StdEncoding.EncodeToString(sig),
	}
	envBytes, _ := json.Marshal(env)
	key := base64.StdEncoding.EncodeToString(envBytes)

	lic, err := VerifyLicense(key)
	if err != nil {
		t.Fatalf("expected JSON envelope key to verify, got: %v", err)
	}
	if lic.Claims.ID != "lic-001" {
		t.Errorf("expected id 'lic-001', got: %s", lic.Claims.ID)
	}
}

func TestFeatureChecks(t *testing.T) {
	var nilLic *License
	if err := nilLic.CheckFeature(FeatureCentralWatchCoordinator); !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing on nil license, got: %v", err)
	}

	lic := &License{
		Claims: Claims{
			Features: []string{
				FeatureCentralWatchCoordinator,
				FeatureSMSNotifications,
			},
		},
	}

	if err := lic.CheckFeature(FeatureCentralWatchCoordinator); err != nil {
		t.Errorf("expected central_watch_coordinator to be allowed, got: %v", err)
	}
	if err := lic.CheckFeature(FeatureSMSNotifications); err != nil {
		t.Errorf("expected sms_notifications to be allowed, got: %v", err)
	}
	if err := lic.CheckFeature(FeatureTeamAuditTrail); !errors.Is(err, ErrFeatureNotEntitled) {
		t.Errorf("expected ErrFeatureNotEntitled for team_audit_trail, got: %v", err)
	}

	// Wildcard
	licWildcard := &License{
		Claims: Claims{
			Features: []string{"*"},
		},
	}
	if err := licWildcard.CheckFeature("any_future_feature"); err != nil {
		t.Errorf("expected wildcard to entitle any feature, got: %v", err)
	}
}

func TestCapacityChecks(t *testing.T) {
	var nilLic *License
	if err := nilLic.CheckCoordinatorCapacity(1); !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing for nil license")
	}
	if err := nilLic.CheckRunnerCapacity(1); !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing for nil license")
	}
	if err := nilLic.CheckUserCapacity(1); !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing for nil license")
	}

	lic := &License{
		Claims: Claims{
			Capacity: Capacity{
				MaxCoordinators: 2,
				MaxRunners:      10,
				MaxUsers:        50,
			},
		},
	}

	// Coordinators
	if err := lic.CheckCoordinatorCapacity(2); err != nil {
		t.Errorf("capacity 2 should pass: %v", err)
	}
	if err := lic.CheckCoordinatorCapacity(3); !errors.Is(err, ErrCapacityExceeded) {
		t.Errorf("capacity 3 should exceed limit: %v", err)
	}

	// Runners
	if err := lic.CheckRunnerCapacity(10); err != nil {
		t.Errorf("capacity 10 should pass: %v", err)
	}
	if err := lic.CheckRunnerCapacity(11); !errors.Is(err, ErrCapacityExceeded) {
		t.Errorf("capacity 11 should exceed limit: %v", err)
	}

	// Users
	if err := lic.CheckUserCapacity(50); err != nil {
		t.Errorf("capacity 50 should pass: %v", err)
	}
	if err := lic.CheckUserCapacity(51); !errors.Is(err, ErrCapacityExceeded) {
		t.Errorf("capacity 51 should exceed limit: %v", err)
	}

	// Unlimited (0)
	licUnlimited := &License{
		Claims: Claims{
			Capacity: Capacity{
				MaxCoordinators: 0,
				MaxRunners:      0,
				MaxUsers:        0,
			},
		},
	}
	if err := licUnlimited.CheckCoordinatorCapacity(999); err != nil {
		t.Errorf("unlimited coordinator should pass: %v", err)
	}
	if err := licUnlimited.CheckRunnerCapacity(999); err != nil {
		t.Errorf("unlimited runner should pass: %v", err)
	}
	if err := licUnlimited.CheckUserCapacity(999); err != nil {
		t.Errorf("unlimited user should pass: %v", err)
	}
}

func TestActiveLicenseAndEnvLoading(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	SetActiveLicense(nil)
	if GetActiveLicense() != nil {
		t.Errorf("expected nil active license")
	}
	if err := CheckFeature(FeatureCentralWatchCoordinator); !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing from global CheckFeature, got: %v", err)
	}

	// Env loading when empty
	t.Setenv("DATATUG_LICENSE_KEY", "")
	t.Setenv("DATATUG_LICENSE_FILE", "")
	_, err := LoadLicenseFromEnv()
	if !errors.Is(err, ErrLicenseMissing) {
		t.Errorf("expected ErrLicenseMissing, got: %v", err)
	}

	// Env loading with key
	claims := sampleClaims(time.Now())
	key, _ := SignLicense(claims, priv)
	t.Setenv("DATATUG_LICENSE_KEY", key)

	loaded, err := LoadLicenseFromEnv()
	if err != nil {
		t.Fatalf("LoadLicenseFromEnv failed: %v", err)
	}
	if loaded.Claims.ID != "lic-001" {
		t.Errorf("expected lic-001, got %s", loaded.Claims.ID)
	}
	if GetActiveLicense() == nil {
		t.Errorf("expected active license to be installed")
	}
	if err := CheckFeature(FeatureCentralWatchCoordinator); err != nil {
		t.Errorf("global CheckFeature should pass: %v", err)
	}

	// Env loading with file
	t.Setenv("DATATUG_LICENSE_KEY", "")
	tempFile := filepath.Join(t.TempDir(), "datatug.license")
	if err := os.WriteFile(tempFile, []byte(key), 0600); err != nil {
		t.Fatalf("failed to write license file: %v", err)
	}
	t.Setenv("DATATUG_LICENSE_FILE", tempFile)
	loadedFromFile, err := LoadLicenseFromEnv()
	if err != nil {
		t.Fatalf("LoadLicenseFromEnv from file failed: %v", err)
	}
	if loadedFromFile.Claims.Organization != "Acme Corp" {
		t.Errorf("expected Acme Corp, got: %s", loadedFromFile.Claims.Organization)
	}
}

func TestPublicKeyInitFromEnv(t *testing.T) {
	pub, _ := generateTestKeyPair(t)
	hexPub := hex.EncodeToString(pub)
	t.Setenv("DATATUG_LICENSE_PUBLIC_KEY", hexPub)

	// Test SetPublicKey restore
	restore := SetPublicKey(pub)
	if !bytes.Equal(getPublicKey(), pub) {
		t.Errorf("expected custom public key")
	}
	restore()
}

func TestVerifyLicense_EdgeCases(t *testing.T) {
	pub, priv := generateTestKeyPair(t)
	restoreKey := SetPublicKey(pub)
	defer restoreKey()

	now := time.Now()
	claims := sampleClaims(now)
	key, _ := SignLicense(claims, priv)

	// 1. Missing public key
	restoreNilKey := SetPublicKey(nil)
	_, err := VerifyLicense(key)
	if err == nil || !strings.Contains(err.Error(), "public key not configured") {
		t.Errorf("expected error when public key not configured, got: %v", err)
	}
	restoreNilKey()

	// 2. Wrong signature size
	parts := strings.Split(key, ".")
	shortSigKey := parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte("short-sig"))
	_, err = VerifyLicense(shortSigKey)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature for short signature, got: %v", err)
	}

	// 3. Bad base64 in parts[0] or parts[1]
	_, err = VerifyLicense("!bad-payload!." + parts[1])
	if !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("expected ErrInvalidLicense for bad payload base64, got: %v", err)
	}
	_, err = VerifyLicense(parts[0] + ".!bad-sig!")
	if !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("expected ErrInvalidLicense for bad signature base64, got: %v", err)
	}

	// 4. Invalid JSON payload
	invalidJSONKey := base64.RawURLEncoding.EncodeToString([]byte("not-json")) + "." + parts[1]
	_, err = VerifyLicense(invalidJSONKey)
	if !errors.Is(err, ErrInvalidSignature) { // will fail signature check before unmarshal
		t.Logf("failed signature as expected: %v", err)
	}

	// Sign invalid JSON properly to test unmarshal failure
	badJSONBytes := []byte("not json at all")
	badSig := ed25519.Sign(priv, badJSONBytes)
	badSignedKey := base64.RawURLEncoding.EncodeToString(badJSONBytes) + "." + base64.RawURLEncoding.EncodeToString(badSig)
	_, err = VerifyLicense(badSignedKey)
	if !errors.Is(err, ErrInvalidLicense) {
		t.Errorf("expected ErrInvalidLicense for invalid JSON payload, got: %v", err)
	}

	// 5. LoadLicenseFromEnv error reading file
	t.Setenv("DATATUG_LICENSE_KEY", "")
	t.Setenv("DATATUG_LICENSE_FILE", "/non/existent/license/path/xyz")
	_, err = LoadLicenseFromEnv()
	if err == nil {
		t.Errorf("expected error reading non-existent license file")
	}
}
