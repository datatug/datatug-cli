package accesspolicies

import (
	"os"
	"path/filepath"
)

// ServePoliciesDir is the explicit policies folder `datatug serve` uses: "<projectDir>/policies"
// when projectDir is set and that folder exists, else "" (the usual default resolution:
// $DATATUG_POLICIES_DIR, then ~/.datatug/policies). `serve` and the daemon's pre-check both
// call it, so the two cannot drift.
func ServePoliciesDir(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	candidate := filepath.Join(projectDir, "policies")
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	return ""
}

// ServePolicySetPresent says whether `datatug serve` started without a project would find a
// valid policy set (and so require a named principal). A missing or invalid set is "no": serve
// itself reports an invalid one.
func ServePolicySetPresent() bool {
	_, err := Load(LoadOptions{Dir: ServePoliciesDir("")})
	return err == nil
}
