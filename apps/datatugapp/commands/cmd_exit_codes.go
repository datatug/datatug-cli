package commands

// specscore: feature/cli/self-update
// specscore: feature/cli/install

import (
	"errors"

	installcobracmd "github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
	selfupdatecobracmd "github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

// failureExitCode maps a self-update/cli-install failure — a usage error
// from either Cobra adapter, or any selfupdate.FailureKind, including the
// three cli-install-only kinds appended after KindManagedCommand — onto
// datatug's own parent CLI exit-code contract
// (spec/features/cli/README.md's "Shared exit-code contract": 2 invalid
// arguments, 3 not found, 4 connection/I/O failure, 1 generic catch-all).
// selfUpdateErrors, installErrors and upgradeErrors (which embeds
// installErrors) all route through this ONE function
// (cli-install#req:host-owned-exit-codes) so self-update, install and
// upgrade can never disagree about what a given failure kind costs.
//
//   - KindUnknownTarget, KindDowngrade, KindNonInteractive, and either
//     Cobra adapter's own UsageError are all "fixed by passing a different
//     flag or argument" — exactly REQ: standard-exit-codes' code 2
//     ("missing required flag, bad flag value, malformed input").
//   - KindUnknownTag (a pinned --version tag matching no published release,
//     or no asset for this platform within that release) and
//     KindUnsupportedPlatform (no release asset configured for this host's
//     platform at all) both mean the release asset the operation needs
//     does not exist — code 3 ("resource not found").
//   - KindReleaseLookup, KindDownload, KindPermission, KindNoInstallDir and
//     KindDestinationExists are all a connection/I/O failure against an
//     external system — a GitHub API/network failure, a filesystem
//     permission or destination problem — code 4.
//   - Everything else (KindAmbiguous, KindChecksum, KindManagedCommand,
//     KindManagedVersion, KindUnexpected, and any plain error) falls to the
//     generic catch-all, code 1.
func failureExitCode(err error) int {
	var selfUsage *selfupdatecobracmd.UsageError
	if errors.As(err, &selfUsage) {
		return 2
	}
	var installUsage *installcobracmd.UsageError
	if errors.As(err, &installUsage) {
		return 2
	}
	switch selfupdate.KindOf(err) {
	case selfupdate.KindUnknownTarget, selfupdate.KindDowngrade, selfupdate.KindNonInteractive:
		return 2
	case selfupdate.KindUnknownTag, selfupdate.KindUnsupportedPlatform:
		return 3
	case selfupdate.KindReleaseLookup, selfupdate.KindDownload, selfupdate.KindPermission, selfupdate.KindNoInstallDir, selfupdate.KindDestinationExists:
		return 4
	default:
		return 1
	}
}
