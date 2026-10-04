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
			u, err := dburl.Parse(argAt(args, 0))
			if err != nil {
				return errDBURLParse // u is nil here; falling through would dereference it
			}
			fmt.Printf("Opening database at %s", dbcopy.RedactSourceURL(u.String()))
			return nil
		},
	}
	cmd.AddCommand(dbCopyCommand())
	return cmd
}
