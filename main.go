package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/apps/datatugapp/commands"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	_ "github.com/denisenkom/go-mssqldb"
	"github.com/posthog/posthog-go"
	"github.com/strongo/logus"

	//_ "github.com/jackc/pgx/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/urfave/cli/v3"
)

func main() {

	// Enqueue an event
	dtlog.Enqueue(posthog.Capture{Event: "DataTug CLI started"})

	defer func() {
		r := recover()
		if r != nil {
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
		}
		dtlog.Enqueue(posthog.Capture{Event: "DataTug CLI exited"})
		dtlog.Close()
		time.Sleep(time.Millisecond) // Do we really need this?
		if r != nil {
			panic(r)
		}
	}()

	cmd := getCommand()
	args := os.Args
	// When running under `go test`, os.Args contains testing flags that urfave/cli doesn't recognize.
	// Detect test binary by suffix and strip args to avoid parsing test flags.
	if len(args) > 0 && strings.HasSuffix(args[0], ".test") {
		args = args[:1]
	}
	if err := cmd.Run(context.Background(), args); err != nil {
		log.Fatal(err)
	}
	//var p = getParser()
	//if _, err := p.Parse(); err != nil {
	//	var flagsErr *flags.Error
	//	switch {
	//	case errors.As(err, &flagsErr):
	//		if errors.Is(flagsErr.CollectionType, flags.ErrHelp) {
	//			os.Exit(0)
	//		}
	//		os.Exit(1)
	//	default:
	//		_, _ = fmt.Fprintf(os.Stderr, "failed to execute command: %s", err)
	//		os.Exit(1)
	//	}
	//}
}

var getCommand = func() *cli.Command {
	return commands.DatatugCommand()
}
