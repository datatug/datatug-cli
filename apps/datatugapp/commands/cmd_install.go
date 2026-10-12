package commands

// specscore: feature/cli/install

import (
	"github.com/spf13/cobra"

	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
)

// installErrors implements cobracmd.ErrorMapper for datatug's own exit
// codes, mirroring selfUpdateErrors: both route every failure through the
// same failureExitCode (cmd_exit_codes.go), which follows the parent CLI
// spec's shared exit-code contract
// (spec/features/cli/README.md's "Shared exit-code contract") —
// (cli-install#req:host-owned-exit-codes). A *cobracmd.UsageError (an
// invalid --format, or --all combined with names) and
// selfupdate.KindUnknownTarget both map to code 2 (invalid arguments);
// selfupdate.KindNoInstallDir and KindDestinationExists both map to code 4
// (connection/I/O failure); every kind shared with self-update maps
// identically to how self-update maps it.
//
// Every new kind cli-install added is mapped in failureExitCode's own
// switch case, per that REQ ("MUST map the three new kinds explicitly...
// MUST NOT let them fall into a self-update default branch"):
// KindUnknownTarget's underlying error already names the unknown target and
// lists valid catalog ids (cli-install#req:unknown-target-refused), which
// is the "usage message" datatug reports for it.
type installErrors struct{}

// Failure maps every install failure onto datatug's parent-spec exit code
// via failureExitCode (cmd_exit_codes.go).
//
// cliinstall/cobracmd v0.21.0's own mapFailure short-circuits a nil error
// before ever calling opts.Errors.Failure (see that package's doc comment
// on mapFailure and its TestMapFailure_NeverCallsMapperWithNil), so this
// method can now rely on cobracmd.ErrorMapper's documented "maps a
// non-nil command error" contract like selfUpdateErrors already does; the
// v0.19.0-era nil-guard workaround this method used to carry is gone.
func (installErrors) Failure(err error) error {
	return Exit(err.Error(), failureExitCode(err))
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
