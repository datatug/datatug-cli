package commands

import (
	"errors"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/spf13/cobra"
	"github.com/xo/dburl"
)

// errDBURLParse is what `datatug db <url>` says about a URL it cannot parse.
// The argument is a database URL and can hold a password, and the parser's own
// error quotes it, so neither is shown.
var errDBURLParse = errors.New("db url parse error: the argument is not a valid database URL")

func dbCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Opens database viewer",
		RunE: func(_ *cobra.Command, args []string) error {
			arg := argAt(args, 0)
			if _, err := dburl.Parse(arg); err != nil {
				return errDBURLParse
			}
			fmt.Printf("Opening database at %s", dbArgumentDisplay(arg))
			return nil
		},
	}
	cmd.AddCommand(dbCopyCommand())
	return cmd
}

// dbArgumentDisplay is the text `datatug db` prints for its argument. What is
// printed is built from the scheme, host, port and path of the argument; the
// user name, the password and the query string are not in it. dburl also takes a
// bare file path (./chinook.sqlite), which is no URL: it is shown as a path.
func dbArgumentDisplay(arg string) string {
	if shown := dbcopy.SourceDisplay(arg); shown != dbcopy.UnparsableSource {
		return shown
	}
	return dbcopy.PathDisplay(arg)
}
