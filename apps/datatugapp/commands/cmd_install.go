package commands

// specscore: feature/cli/install

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

// installErrors implements cobracmd.ErrorMapper for datatug's own,
// intentionally simple exit-code contract
// (cli-install#req:host-owned-exit-codes), mirroring selfUpdateErrors: every
// failure — a *cobracmd.UsageError (an invalid --format, or --all combined
// with names), an unknown target name (selfupdate.KindUnknownTarget), a
// missing per-user bin directory or an already-occupied destination
// (selfupdate.KindNoInstallDir, selfupdate.KindDestinationExists), or any
// failure kind shared with self-update — exits 1 via the same
// commands.Exit/ExitCoder mechanism every other datatug command uses.
//
// Every new kind cli-install added is mapped in its own switch case, per
// that REQ ("MUST map the three new kinds explicitly... MUST NOT let them
// fall into a self-update default branch"), even though datatug's own exit
// code for all of them is the same 1 the self-update default branch already
// used: KindUnknownTarget's underlying error already names the unknown
// target and lists valid catalog ids
// (cli-install#req:unknown-target-refused), which is the "usage message"
// datatug reports for it.
type installErrors struct{}

// Failure maps every install failure onto datatug's generic error exit
// code, 1.
//
// cliinstall/cobracmd v0.21.0's own mapFailure short-circuits a nil error
// before ever calling opts.Errors.Failure (see that package's doc comment
// on mapFailure and its TestMapFailure_NeverCallsMapperWithNil), so this
// method can now rely on cobracmd.ErrorMapper's documented "maps a
// non-nil command error" contract like selfUpdateErrors already does; the
// v0.19.0-era nil-guard workaround this method used to carry is gone.
func (installErrors) Failure(err error) error {
	var usage *cobracmd.UsageError
	if errors.As(err, &usage) {
		return Exit(err.Error(), 1)
	}
	switch selfupdate.KindOf(err) {
	case selfupdate.KindUnknownTarget:
		// The underlying error already lists valid ids — see the type doc
		// comment above — so no extra usage text is added here.
		return Exit(err.Error(), 1)
	case selfupdate.KindNoInstallDir, selfupdate.KindDestinationExists:
		return Exit(err.Error(), 1)
	default:
		return Exit(err.Error(), 1)
	}
}

// InstallCommand returns the "install" command, built from
// github.com/strongo/cli-helpers/cliinstall/cobracmd against datatug's own
// catalog id (cli-install#req:host-identity-from-catalog). `install` lists
// the fleet CLIs relevant to datatug (`ingitdb`, `ovdb`, `specscore`) with
// their live status, and `install <name>...` installs them the same way
// datatug itself was installed. cobracmd.New panics if "datatug" is absent
// from the compiled catalog — a programming error this package's own tests
// catch (TestInstallCommand_Shape proves the real entry resolves), never a
// runtime state a user sees.
func InstallCommand() *cobra.Command {
	return cobracmd.New(cobracmd.CommandOptions{
		Short:  "List and install fleet CLIs relevant to datatug",
		Errors: installErrors{},
		HostID: "datatug",
	})
}
