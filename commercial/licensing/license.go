package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"
)

// Supported Commercial Feature Names
const (
	FeatureCentralWatchCoordinator = "central_watch_coordinator"
	FeatureSharedExecutionStore    = "shared_execution_store"
	FeatureTeamAuditTrail          = "team_audit_trail"
	FeatureSMSNotifications        = "sms_notifications"
)

// Supported Commercial Tiers
const (
	TierPro                  = "pro"
	TierEnterpriseSelfHosted = "enterprise_self_hosted"
)

// Standard Grace Period: 14 days after license expiration
const GracePeriodDuration = 14 * 24 * time.Hour

var (
	ErrLicenseMissing     = errors.New("commercial license key is required")
	ErrInvalidLicense     = errors.New("invalid commercial license format")
	ErrInvalidSignature   = errors.New("invalid commercial license signature")
	ErrLicenseExpired     = errors.New("commercial license has expired and exceeded the grace period")
	ErrFeatureNotEntitled = errors.New("feature not entitled under current commercial license")
	ErrCapacityExceeded   = errors.New("license capacity exceeded")
	ErrInvalidTier        = errors.New("unrecognized license tier")
)

// Default embedded public key for production licenses (Ed25519 32-byte public key)
// This can be overridden in tests or custom enterprise installations via SetPublicKey
// or the DATATUG_LICENSE_PUBLIC_KEY environment variable (hex-encoded).
var defaultPublicKeyHex = "4d617474686577536e656174446174615475674c6963656e73654b6579303031" // 32 bytes

var (
	keyMu         sync.RWMutex
	activeKeyMu   sync.RWMutex
	activeLicense *License
	trustedPubKey ed25519.PublicKey

	timeNow                = time.Now
	stderrWriter io.Writer = os.Stderr
)

func init() {
	keyBytes, _ := hex.DecodeString(defaultPublicKeyHex)
	trustedPubKey = ed25519.PublicKey(keyBytes)
}

// SetPublicKey configures the Ed25519 public key used for offline verification.
// Returns a restore function suitable for defer in unit tests.
func SetPublicKey(pub ed25519.PublicKey) func() {
	keyMu.Lock()
	old := trustedPubKey
	trustedPubKey = pub
	keyMu.Unlock()
	return func() {
		keyMu.Lock()
		trustedPubKey = old
		keyMu.Unlock()
	}
}

// getPublicKey returns the current active public key.
func getPublicKey() ed25519.PublicKey {
	keyMu.RLock()
	defer keyMu.RUnlock()
	return trustedPubKey
}

// SetStderrWriter sets the diagnostic writer for grace-period alerts.
func SetStderrWriter(w io.Writer) func() {
	keyMu.Lock()
	old := stderrWriter
	stderrWriter = w
	keyMu.Unlock()
	return func() {
		keyMu.Lock()
		stderrWriter = old
		keyMu.Unlock()
	}
}

// SetTimeNow overrides time.Now for deterministic testing.
func SetTimeNow(fn func() time.Time) func() {
	keyMu.Lock()
	old := timeNow
	timeNow = fn
	keyMu.Unlock()
	return func() {
		keyMu.Lock()
		timeNow = old
		keyMu.Unlock()
	}
}

// Capacity specifies resource limits authorized by a license.
type Capacity struct {
	MaxCoordinators int `json:"max_coordinators,omitempty"`
	MaxRunners      int `json:"max_runners,omitempty"`
	MaxUsers        int `json:"max_users,omitempty"`
}

// Claims represents the cryptographically signed license payload.
type Claims struct {
	ID           string    `json:"id"`
	Organization string    `json:"organization"`
	IssuedAt     time.Time `json:"issued_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Tier         string    `json:"tier"`
	Capacity     Capacity  `json:"capacity"`
	Features     []string  `json:"features"`
}

// License represents a verified and decoded commercial license.
type License struct {
	Claims      Claims `json:"claims"`
	InGrace     bool   `json:"in_grace"`
	DaysInGrace int    `json:"days_in_grace,omitempty"`
}

// VerifyLicense decodes, cryptographically checks the Ed25519 signature,
// and validates the claims and expiration with a 14-day grace period.
//
// The licenseKey may be formatted as:
//   - "<base64_claims>.<base64_signature>"
//   - or base64-encoded JSON {"payload": "<base64_claims>", "signature": "<base64_signature>"}
//
// Verification is completely offline and air-gapped without network calls.
func VerifyLicense(licenseKey string) (*License, error) {
	trimmed := strings.TrimSpace(licenseKey)
	if trimmed == "" {
		return nil, ErrLicenseMissing
	}

	payloadBytes, sigBytes, err := parseLicenseKey(trimmed)
	if err != nil {
		return nil, err
	}

	pubKey := getPublicKey()
	if len(pubKey) != ed25519.PublicKeySize {
		return nil, errors.New("licensing public key not configured")
	}

	if len(sigBytes) != ed25519.SignatureSize {
		return nil, ErrInvalidSignature
	}

	if !ed25519.Verify(pubKey, payloadBytes, sigBytes) {
		return nil, ErrInvalidSignature
	}

	var claims Claims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, fmt.Errorf("%w: failed to parse license claims: %v", ErrInvalidLicense, err)
	}

	if claims.ID == "" || claims.Organization == "" {
		return nil, fmt.Errorf("%w: missing required claims (id, organization)", ErrInvalidLicense)
	}

	if claims.Tier != TierPro && claims.Tier != TierEnterpriseSelfHosted {
		return nil, fmt.Errorf("%w: '%s'", ErrInvalidTier, claims.Tier)
	}

	now := timeNow()

	// Check if expired
	lic := &License{
		Claims: claims,
	}

	if !claims.ExpiresAt.IsZero() && now.After(claims.ExpiresAt) {
		graceDeadline := claims.ExpiresAt.Add(GracePeriodDuration)
		if now.After(graceDeadline) {
			return nil, ErrLicenseExpired
		}

		// Within grace period
		lic.InGrace = true
		remainingDays := int(math.Ceil(graceDeadline.Sub(now).Hours() / 24.0))
		lic.DaysInGrace = remainingDays

		keyMu.RLock()
		out := stderrWriter
		keyMu.RUnlock()

		if out != nil {
			_, _ = fmt.Fprintf(out,
				"WARNING: DataTug commercial license for '%s' expired on %s. "+
					"Operating within 14-day grace period (%d day(s) remaining). "+
					"Please renew your subscription to maintain uninterrupted centralized services.\n",
				claims.Organization,
				claims.ExpiresAt.Format("2006-01-02"),
				remainingDays,
			)
		}
	}

	return lic, nil
}

// parseLicenseKey splits and base64-decodes the payload and signature.
func parseLicenseKey(key string) ([]byte, []byte, error) {
	// Format 1: "<base64_payload>.<base64_sig>"
	if strings.Contains(key, ".") {
		parts := strings.SplitN(key, ".", 2)
		payload, err := decodeBase64(parts[0])
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid payload encoding", ErrInvalidLicense)
		}
		sig, err := decodeBase64(parts[1])
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid signature encoding", ErrInvalidLicense)
		}
		return payload, sig, nil
	}

	// Format 2: Base64-encoded JSON envelope {"payload": "...", "signature": "..."}
	decodedJSON, err := decodeBase64(key)
	if err == nil {
		var env struct {
			Payload   string `json:"payload"`
			Signature string `json:"signature"`
		}
		if json.Unmarshal(decodedJSON, &env) == nil && env.Payload != "" && env.Signature != "" {
			payload, err1 := decodeBase64(env.Payload)
			sig, err2 := decodeBase64(env.Signature)
			if err1 == nil && err2 == nil {
				return payload, sig, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("%w: unrecognized license key format", ErrInvalidLicense)
}

func decodeBase64(s string) ([]byte, error) {
	// Try standard base64 then URL-safe base64, with or without padding
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

// CheckFeature verifies that the receiver license entitles the given feature.
func (l *License) CheckFeature(feature string) error {
	if l == nil {
		return ErrLicenseMissing
	}
	for _, f := range l.Claims.Features {
		if f == feature || f == "*" {
			return nil
		}
	}
	return fmt.Errorf("%w: '%s'", ErrFeatureNotEntitled, feature)
}

// CheckCoordinatorCapacity checks if coordinatorCount is within licensed capacity.
func (l *License) CheckCoordinatorCapacity(coordinatorCount int) error {
	if l == nil {
		return ErrLicenseMissing
	}
	if l.Claims.Capacity.MaxCoordinators > 0 && coordinatorCount > l.Claims.Capacity.MaxCoordinators {
		return fmt.Errorf("%w: active coordinators (%d) exceeds licensed limit (%d)",
			ErrCapacityExceeded, coordinatorCount, l.Claims.Capacity.MaxCoordinators)
	}
	return nil
}

// CheckRunnerCapacity checks if runnerCount is within licensed capacity.
func (l *License) CheckRunnerCapacity(runnerCount int) error {
	if l == nil {
		return ErrLicenseMissing
	}
	if l.Claims.Capacity.MaxRunners > 0 && runnerCount > l.Claims.Capacity.MaxRunners {
		return fmt.Errorf("%w: active runners (%d) exceeds licensed limit (%d)",
			ErrCapacityExceeded, runnerCount, l.Claims.Capacity.MaxRunners)
	}
	return nil
}

// CheckUserCapacity checks if userCount is within licensed capacity.
func (l *License) CheckUserCapacity(userCount int) error {
	if l == nil {
		return ErrLicenseMissing
	}
	if l.Claims.Capacity.MaxUsers > 0 && userCount > l.Claims.Capacity.MaxUsers {
		return fmt.Errorf("%w: active users (%d) exceeds licensed limit (%d)",
			ErrCapacityExceeded, userCount, l.Claims.Capacity.MaxUsers)
	}
	return nil
}

// SetActiveLicense installs a verified license into process memory.
func SetActiveLicense(l *License) {
	activeKeyMu.Lock()
	defer activeKeyMu.Unlock()
	activeLicense = l
}

// GetActiveLicense retrieves the currently installed commercial license.
func GetActiveLicense() *License {
	activeKeyMu.RLock()
	defer activeKeyMu.RUnlock()
	return activeLicense
}

// CheckFeature checks the currently active license for the specified feature.
func CheckFeature(feature string) error {
	activeKeyMu.RLock()
	lic := activeLicense
	activeKeyMu.RUnlock()

	if lic == nil {
		return fmt.Errorf("%w: %s requires a valid commercial license", ErrLicenseMissing, feature)
	}
	return lic.CheckFeature(feature)
}

// LoadLicenseFromEnv reads a license key from DATATUG_LICENSE_KEY or DATATUG_LICENSE_FILE,
// verifies it, and registers it as the active license.
func LoadLicenseFromEnv() (*License, error) {
	key := os.Getenv("DATATUG_LICENSE_KEY")
	if key == "" {
		filePath := os.Getenv("DATATUG_LICENSE_FILE")
		if filePath != "" {
			data, err := os.ReadFile(filePath)
			if err != nil {
				return nil, fmt.Errorf("failed to read license file %s: %w", filePath, err)
			}
			key = string(data)
		}
	}

	if strings.TrimSpace(key) == "" {
		return nil, ErrLicenseMissing
	}

	lic, err := VerifyLicense(key)
	if err != nil {
		return nil, err
	}

	SetActiveLicense(lic)
	return lic, nil
}

// SignLicense creates a signed license key from claims and an Ed25519 private key.
// Primarily used by tests and the license generation utility.
func SignLicense(claims Claims, privKey ed25519.PrivateKey) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(privKey, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
