package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"charm.land/fang/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/commands"
	"github.com/datatug/datatug-cli/apps/global"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	_ "github.com/denisenkom/go-mssqldb"
	"github.com/posthog/posthog-go"
	"github.com/spf13/cobra"
	"github.com/strongo/buildinfo"
	"github.com/strongo/buildinfo/fangcmd"
	"github.com/strongo/logus"

	//_ "github.com/jackc/pgx/v5"
	_ "github.com/mattn/go-sqlite3"
)

var osExit = os.Exit

// dtlogEnqueue is a seam over dtlog.Enqueue so tests can prove, without a
// real PostHog client, exactly which invocations do and do not enqueue a
// telemetry event — in particular that `version --json` enqueues none
// (cli-install#req:version-json-side-effect-free in strongo/cli-helpers).
var dtlogEnqueue = dtlog.Enqueue

func main() {

	// skipTelemetry is set below, before fang.Execute runs, but declared
	// here (and read by the deferred closure below) so the defer/recover
	// registration keeps its original position — first, before anything
	// that can panic (getCommand returning a nil root, in particular; see
	// the "getCommand_nil" test). A panic before skipTelemetry is assigned
	// leaves it false, which only means the unreached "started" event was
	// never sent either — never a false skip.
	var skipTelemetry bool

	defer func() {
		r := recover()
		if r != nil {
			if global.App != nil {
				global.App.Stop() // VERY IMPORTANT: restore terminal
			}
			rText := fmt.Sprintf("%v", r)
			ctx := context.Background()
			logus.Errorf(ctx, "panic: %s", rText)
			timestamp := time.Now()
			distinctID := dtlog.DistinctID()
			dtlogEnqueue(posthog.NewDefaultException(
				timestamp,
				distinctID,
				"panic",
				rText,
			))
			_, _ = fmt.Fprintln(os.Stderr, "panic:", r)
			debug.PrintStack()
		}
		if !skipTelemetry {
			dtlogEnqueue(posthog.Capture{Event: "DataTug CLI exited"})
		}
		dtlog.Close()
		//time.Sleep(10 * time.Millisecond) // Allow some time for event to be sent
		if r != nil {
			osExit(1)
		}
	}()

	root, fangOpts := getCommand()

	args := os.Args[1:]
	// When running under `go test`, os.Args contains testing flags that cobra
	// doesn't recognize. Detect test binary by suffix and drop them so the CLI
	// parses as a bare invocation instead.
	if len(os.Args) > 0 && strings.HasSuffix(os.Args[0], ".test") {
		args = nil
	}
	root.SetArgs(args)

	// `version --json` MUST NOT emit telemetry — including the start/exit
	// events every other invocation gets — so probing an installed datatug
	// build is safe to repeat
	// (cli-install#req:version-json-side-effect-free in
	// strongo/cli-helpers). Resolved through cobra's own command/flag
	// resolution (root.Find + the matched command's own flag set) rather
	// than hand-parsed argv matching, so this check can never disagree with
	// what fang.Execute actually runs below.
	skipTelemetry = isVersionJSONInvocation(root, args)

	if !skipTelemetry {
		dtlogEnqueue(posthog.Capture{Event: "DataTug CLI started"})
	}

	if err := fang.Execute(context.Background(), root, fangOpts...); err != nil {
		// fang.Execute has already printed err (styled, or bare on a
		// non-terminal stderr — see charm.land/fang/v2's DefaultErrorHandler).
		// Resolve only the process exit code here: an ExitCoder (this
		// package's replacement for github.com/urfave/cli/v3's cli.Exit)
		// carries a specific code; anything else exits 1, matching every
		// non-ExitCoder error's outcome before the cobra migration.
		var ec commands.ExitCoder
		if errors.As(err, &ec) {
			osExit(ec.ExitCode())
			return
		}
		osExit(1)
	}
}

var getCommand = func() (*cobra.Command, []fang.Option) {
	root := commands.DatatugCommand()
	info := buildinfo.Get("datatug")
	// self-update is wired here, where main.go builds the root, against
	// datatug's own compiled-in cliinstall catalog entry
	// (cli-install#req:host-identity-from-catalog); commands.DatatugCommand
	// itself stays version-agnostic so its existing zero-argument tests are
	// unaffected.
	root.AddCommand(commands.SelfUpdateCommand(info.Version))
	fangOpts := fangcmd.Wire(root, info)
	return root, fangOpts
}

// isVersionJSONInvocation reports whether args would dispatch root to the
// buildinfo "version" subcommand (added by fangcmd.Wire, via
// buildinfo/cobracmd.VersionCommand) with --json set. It uses cobra's own
// command/flag resolution (root.Find, then the matched command's own flag
// set) instead of scanning argv by hand, so it can never disagree with
// what fang.Execute resolves and runs a few lines below — both walk the
// exact same command tree with the exact same args.
//
// Calling cmd.ParseFlags here is safe to repeat: cobra's own Execute below
// parses the same args into the same flags again, and pflag.FlagSet.Parse
// is idempotent for identical input.
func isVersionJSONInvocation(root *cobra.Command, args []string) bool {
	cmd, flagArgs, err := root.Find(args)
	if err != nil || cmd == nil || cmd.Name() != "version" {
		return false
	}
	if err := cmd.ParseFlags(flagArgs); err != nil {
		return false
	}
	asJSON, _ := cmd.Flags().GetBool("json")
	return asJSON
}
