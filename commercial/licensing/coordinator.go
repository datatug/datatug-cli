package licensing

import (
	"fmt"
	"os"
	"strings"
)

// CoordinatorEntitlementChecker validates commercial licensing for the centralized watch coordinator.
type CoordinatorEntitlementChecker struct {
	LicenseKey string
}

// NewCoordinatorEntitlementChecker creates a new entitlement checker for watch coordinator deployments.
func NewCoordinatorEntitlementChecker(licenseKey string) *CoordinatorEntitlementChecker {
	return &CoordinatorEntitlementChecker{LicenseKey: licenseKey}
}

// CheckEntitlement verifies that the coordinator is entitled under an active commercial license.
// In evaluation/unlicensed standalone mode, it logs an advisory message to stderr.
func (c *CoordinatorEntitlementChecker) CheckEntitlement() error {
	key := strings.TrimSpace(c.LicenseKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("DATATUG_LICENSE_KEY"))
	}
	if key != "" {
		lic, err := VerifyLicense(key)
		if err != nil {
			return fmt.Errorf("invalid commercial license for watch coordinator: %w", err)
		}
		if err := lic.CheckFeature(FeatureCentralWatchCoordinator); err != nil {
			return fmt.Errorf("commercial license does not entitle centralized watch coordinator: %w", err)
		}
		return nil
	}

	if lic, err := LoadLicenseFromEnv(); err == nil {
		if err := lic.CheckFeature(FeatureCentralWatchCoordinator); err != nil {
			return fmt.Errorf("commercial license does not entitle centralized watch coordinator: %w", err)
		}
		return nil
	}

	// Standalone/evaluation mode notice:
	fmt.Fprintf(os.Stderr, "NOTICE: DataTug Watch Coordinator running in community evaluation mode.\n"+
		"Centralized shared multi-runner deployments require a commercial license key.\n"+
		"See https://datatug.app/pricing or configure DATATUG_LICENSE_KEY for organizational self-hosted deployments.\n")
	return nil
}
