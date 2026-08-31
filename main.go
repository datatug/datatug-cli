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

func main() {

	// Enqueue an event
	dtlog.Enqueue(posthog.Capture{Event: "DataTug CLI started"})

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
			dtlog.Enqueue(posthog.NewDefaultException(
				timestamp,
				distinctID,
				"panic",
				rText,
			))
			_, _ = fmt.Fprintln(os.Stderr, "panic:", r)
			debug.PrintStack()
		}
		dtlog.Enqueue(posthog.Capture{Event: "DataTug CLI exited"})
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
	fangOpts := fangcmd.Wire(root, info)
	return root, fangOpts
}
