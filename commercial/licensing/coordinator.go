package licensing

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// CoordinatorEntitlementChecker verifies legacy coordinator keys at startup.
// It does not resolve project sharing, current plan access or Business activity.
type CoordinatorEntitlementChecker struct {
	LicenseKey string
}

// NewCoordinatorEntitlementChecker creates a new entitlement checker for watch coordinator deployments.
func NewCoordinatorEntitlementChecker(licenseKey string) *CoordinatorEntitlementChecker {
	return &CoordinatorEntitlementChecker{LicenseKey: licenseKey}
}

// CheckEntitlement verifies a supplied key's legacy coordinator entitlement.
// With no key it logs product guidance, not a project-plan authorization decision.
func (c *CoordinatorEntitlementChecker) CheckEntitlement() error {
	var lic *License
	var err error

	key := strings.TrimSpace(c.LicenseKey)
	if key != "" {
		lic, err = VerifyLicense(key)
		if err != nil {
			return fmt.Errorf("invalid commercial license for watch coordinator: %w", err)
		}
	} else {
		lic, err = LoadLicenseFromEnv()
		if err != nil {
			if errors.Is(err, ErrLicenseMissing) {
				fmt.Fprintf(os.Stderr, "NOTICE: DataTug uses one CLI for free and paid project plans.\n"+
					"Personal owner-only private projects and shared public projects are free.\n"+
					"Using shared private projects requires Pro, Business or an agreed Enterprise plan.\n"+
					"Starting this coordinator does not establish project-plan entitlement.\n"+
					"See https://datatug.app/en/pricing/ for project-sharing and activity rules.\n")
				return nil
			}
			return fmt.Errorf("invalid commercial license for watch coordinator: %w", err)
		}
	}

	if err := lic.CheckFeature(FeatureCentralWatchCoordinator); err != nil {
		return fmt.Errorf("commercial license does not entitle centralized watch coordinator: %w", err)
	}
	return nil
}
