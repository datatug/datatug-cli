package commands

// specscore: feature/cli/self-update

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

// catalogByID is a test seam over cliinstall.ByID so the defensive panic
// below (a host id absent from the compiled-in catalog, which never
// happens in production — datatug's own catalog entry always exists) is
// exercisable. Tests that replace it must not run in parallel.
var catalogByID = cliinstall.ByID

// selfUpdateErrors implements cobracmd.ErrorMapper for datatug's own,
// intentionally simple exit-code contract
// (cli-install#req:host-owned-exit-codes): datatug had no self-update
// command before this one, so there is no pre-existing contract to
// preserve. Every operational failure — ambiguous detection, release
// lookup, download, checksum, permission, non-interactive refusal, a
// managed-command failure, or an invalid usage — exits 1. `--check`
// reporting an available (or undetermined) update is NOT treated as a
// failure and exits 0: UpdateAvailable returns nil, the branch
// cobracmd.ErrorMapper documents for a consumer that reserves no distinct
// exit code for "update available" — the human-readable verdict line was
// already printed before this mapper runs.
//
// cliinstall's three install-only failure kinds (KindUnknownTarget,
// KindNoInstallDir, KindDestinationExists) can never reach Failure through
// self-update: they are only produced by the `install` command, which maps
// them explicitly in its own installErrors (cmd_install.go).
type selfUpdateErrors struct{}

// Failure maps every self-update failure onto datatug's generic error exit
// code, 1, via the same commands.Exit/ExitCoder mechanism every other
// datatug command uses to signal a specific process exit code to main.go.
func (selfUpdateErrors) Failure(err error) error {
	return Exit(err.Error(), 1)
}

// UpdateAvailable reports success (exit 0) even though a newer release
// exists or the running build's version is undetermined: see the
// selfUpdateErrors doc comment for why datatug reserves no distinct exit
// code for this case.
func (selfUpdateErrors) UpdateAvailable(_ selfupdate.CheckResult) error {
	return nil
}

// datatugSelfUpdateConfig builds the selfupdate.Config both SelfUpdateCommand
// and UpgradeCommand (cmd_upgrade.go) use — datatug's own compiled-in
// catalog entry (cli-install#req:host-identity-from-catalog): the same
// identity — repository, the GoReleaser-default asset/checksums naming
// datatug's own .goreleaser.yaml already matches, and the
// HomebrewCask("datatug") executable upgrade steps — that any other fleet
// CLI's `install datatug` resolves against. ver is the running build's own
// version (GoReleaser-pinned at link time, "dev" otherwise), matching what
// `datatug version --json` reports for the same build. Sharing this one
// helper is what makes `datatug self-update` and `datatug upgrade datatug`
// reach the exact same library call by construction
// (cli-install#req:self-update-equals-upgrade-self), not by convention.
func datatugSelfUpdateConfig(ver string) selfupdate.Config {
	entry, ok := catalogByID("datatug")
	if !ok {
		// A host id absent from the catalog is a programming error caught by
		// this package's own tests, never a runtime state a user can trigger
		// (cli-install#req:host-identity-from-catalog).
		panic(fmt.Sprintf("cliinstall: no catalog entry for %q", "datatug"))
	}
	return entry.Config(ver)
}

// SelfUpdateCommand returns the "self-update" command, built from
// github.com/strongo/cli-helpers/selfupdate/cobracmd against
// datatugSelfUpdateConfig's identity.
func SelfUpdateCommand(ver string) *cobra.Command {
	// No "update" alias: it was never released
	// (cli-install#req:update-alias-policy — "datatug's planned `update`
	// alias, never released, MUST NOT ship").
	return cobracmd.New(datatugSelfUpdateConfig(ver), cobracmd.CommandOptions{
		Short:  "Update the installed datatug binary in place",
		Errors: selfUpdateErrors{},
	})
}
