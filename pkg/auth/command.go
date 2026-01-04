package auth

import (
	"github.com/datatug/datatug-cli/pkg/auth/gauth"
	"github.com/urfave/cli/v3"
)

func Command() *cli.Command {
	return &cli.Command{
		Name: "auth",
		Commands: []*cli.Command{
			gauth.GoogleAuthCommand(),
		},
	}
}
