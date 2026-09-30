package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func consoleCommandArgs() *cobra.Command {
	return &cobra.Command{
		Use:   "console",
		Short: "Starts interactive console",
		Long:  "Starts interactive console with autocomplete",
		RunE: func(_ *cobra.Command, _ []string) error {
			v := &consoleCommand{}
			return v.Execute(nil)
		},
	}
}

// consoleCommand defines parameters for console consoleCommand
type consoleCommand struct {
}

// consoleSetenv is a seam over os.Setenv so tests can drive its failure
// branch. Always os.Setenv in production.
var consoleSetenv = os.Setenv

// Execute executes serve consoleCommand
func (v *consoleCommand) Execute(_ []string) (err error) {
	if err = consoleSetenv("GO_FLAGS_COMPLETION", "1"); err != nil {
		return err
	}
	_, _ = fmt.Println("To be implemented")
	return nil
}
