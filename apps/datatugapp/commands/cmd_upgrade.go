package commands

// specscore: feature/cli/install

import (
	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
)

// upgradeErrors extends installErrors with the upgrades-available method
// cli-install#req:upgrade-check requires
// (cliinstall/cobracmd.UpgradeErrorMapper), reusing installErrors.Failure
// unchanged so `upgrade`'s failure mapping is identical to `install`'s —
// every kind, including the three cli-install-only kinds, maps to exit 1 —
// and therefore identical to `self-update`'s shared kinds too
// (selfUpdateErrors maps every one of them the same way).
type upgradeErrors struct{ installErrors }

// UpgradesAvailable reports success (exit 0), mirroring selfUpdateErrors.
// UpdateAvailable: datatug reserves no dedicated exit code for "an upgrade
// is available" for either command — the printed verdict lines are the
// only signal (cli-install#req:upgrade-check; plan task-15 exit mapping:
// "--check with an update available → 0 for both self-update --check and
// upgrade --check").
func (upgradeErrors) UpgradesAvailable(_ []cliinstall.UpgradeResult) error {
	return nil
}

// UpgradeCommand returns the "upgrade" command, built from
// github.com/strongo/cli-helpers/cliinstall/cobracmd against datatug's own
// catalog id. HostConfig comes from the exact same datatugSelfUpdateConfig
// helper SelfUpdateCommand uses, and datatug's self-update has no
// after-update hook to pass as HostAfterUpdate, so `datatug self-update`
// and `datatug upgrade datatug` reach the exact same library call
// (cli-install#req:self-update-equals-upgrade-self). ver is the running
// build's own version, matching SelfUpdateCommand's own parameter.
func UpgradeCommand(ver string) *cobra.Command {
	return cobracmd.NewUpgrade(cobracmd.UpgradeCommandOptions{
		Short:      "Upgrade installed fleet CLIs, including datatug itself",
		Errors:     upgradeErrors{},
		HostID:     "datatug",
		HostConfig: datatugSelfUpdateConfig(ver),
		// No HostAfterUpdate: datatug's self-update has no after-update
		// hook (see SelfUpdateCommand), so neither does upgrade datatug.
	})
}
