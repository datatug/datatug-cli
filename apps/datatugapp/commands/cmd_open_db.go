package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/xo/dburl"
)

func dbCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Opens database viewer",
		RunE: func(_ *cobra.Command, args []string) error {
			u, err := dburl.Parse(argAt(args, 0))
			if err != nil {
				fmt.Printf("db url parse error: %v\nArgs:\n\t%s", err, strings.Join(args, "\n\t"))
			}
			fmt.Printf("Opening database at %s", u.String())
			return nil
		},
	}
	cmd.AddCommand(dbCopyCommand())
	return cmd
}
