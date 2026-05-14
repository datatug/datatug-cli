package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"
	"github.com/xo/dburl"
)

func dbCommand() *cli.Command {
	return &cli.Command{
		Name:        "db",
		Usage:       "Opens database viewer",
		Description: "",
		Commands: []*cli.Command{
			dbCopyCommand(),
		},
		Action: func(ctx context.Context, command *cli.Command) error {
			u, err := dburl.Parse(command.Args().First())
			if err != nil {
				fmt.Printf("db url parse error: %v\nArgs:\n\t%s", err, strings.Join(command.Args().Slice(), "\n\t"))
			}
			fmt.Printf("Opening database at %s", u.String())
			return nil
		},
	}
}
