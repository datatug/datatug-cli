package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"

	"github.com/datatug/datatug-cli/pkg/mcpserver"
	"github.com/spf13/cobra"
	"github.com/strongo/buildinfo"
)

func mcpCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "mcp", Short: "Serve read-only MCP tools over stdin/stdout using a local DataTug agent",
		Long: "Serve MCP over stdin/stdout. Start datatug serve separately with your project and policy session. Discover project/schema/saved SQL queries and execute them through the agent's existing read-only SQL path. No project writes or raw SQL tools are exposed. Connection credentials stay with the agent. See docs/mcp.md.",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			address, _ := cmd.Flags().GetString("agent-url")
			client, err := mcpserver.NewClient(address)
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			in, ok := cmd.InOrStdin().(io.ReadCloser)
			if !ok {
				in = io.NopCloser(cmd.InOrStdin())
			}
			err = mcpserver.Run(ctx, client, buildinfo.Get("datatug").Version, in, mcpOutput{cmd.OutOrStdout()})
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().String("agent-url", mcpserver.DefaultAgentURL, "Local DataTug agent origin (http://localhost, 127.0.0.1 or [::1] with an explicit port)")
	return cmd
}

// Closing an SDK session must not close Cobra's process-owned stdout.
type mcpOutput struct{ io.Writer }

func (mcpOutput) Close() error { return nil }
