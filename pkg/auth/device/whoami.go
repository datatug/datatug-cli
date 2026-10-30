// Package device provides DataTug's shared auth.sneat.co device-login command.
package device

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/spf13/cobra"
	"github.com/strongo/buildinfo"
	"github.com/strongo/deviceauth"
	"golang.org/x/oauth2"
)

// WhoamiCommand returns the top-level `datatug whoami` command. It reports the
// signed-in DataTug account (auth.sneat.co) in the same human-facing style as
// `wrangler whoami`. It is mounted at the root because it is the everyday
// "am I signed in, and as whom?" command; it is not a credential-management
// verb the way login/status/logout are.
func WhoamiCommand() *cobra.Command { return directCommand(whoamiCommand) }

func whoamiCommand(rawIssuer *string, insecure *bool, deps dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in DataTug account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, store, description, err := deps.newClient(*rawIssuer, *insecure)
			if err != nil {
				return fmt.Errorf("configure credential storage: %w", err)
			}
			token, err := newTokenSource(cmd.Context(), client, store, deps.refresh).Token()
			if errors.Is(err, deviceauth.ErrCredentialNotFound) {
				return fmt.Errorf("not signed in to %s; run 'datatug auth login'", client.Issuer())
			}
			if err != nil {
				return fmt.Errorf("load Firebase session: %w", err)
			}
			identity, err := client.UserInfo(context.WithValue(cmd.Context(), oauth2.HTTPClient, deps.httpClient), token)
			if err != nil {
				return fmt.Errorf("validate Firebase session: %w", err)
			}
			return writeWhoami(cmd.OutOrStdout(), client.Issuer(), description, identity, token)
		},
	}
}

func writeWhoami(w io.Writer, issuer, storage string, identity deviceauth.Identity, token *oauth2.Token) error {
	subject := identity.Subject
	email := identity.Email
	greeting := email
	if greeting == "" {
		greeting = subject
	}

	_, _ = fmt.Fprintf(w, " 🧭 datatug %s\n", datatugVersion())
	_, _ = fmt.Fprintln(w, strings.Repeat("─", 20))
	_, _ = fmt.Fprintf(w, "👋 You are signed in as %s.\n\n", greeting)

	var rows [][]string
	if email != "" {
		rows = append(rows, []string{"Email", email})
	}
	if subject != "" {
		rows = append(rows, []string{"UID", subject})
	}
	if identity.Name != "" {
		rows = append(rows, []string{"Name", identity.Name})
	}
	rows = append(rows, []string{"Issuer", issuer})
	if len(identity.Scopes) > 0 {
		rows = append(rows, []string{"Scopes", strings.Join(identity.Scopes, " ")})
	}
	if !token.Expiry.IsZero() {
		rows = append(rows, []string{"Expires", token.Expiry.UTC().Format(time.RFC3339)})
	}
	rows = append(rows, []string{"Token storage", storage})

	if err := writeIdentityTable(w, rows); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, "\n🔓 To sign out, run: datatug auth logout")
	return nil
}

// writeIdentityTable renders Field/Value rows with the same lipgloss styling
// as the rest of the fleet. Rendering is pipe-safe: lipgloss.Fprintln strips
// ANSI when w is not a terminal.
func writeIdentityTable(w io.Writer, rows [][]string) error {
	header := lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(lipgloss.Color("6")) // cyan
	cell := lipgloss.NewStyle().Padding(0, 1)
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Faint(true)). // dimmed border
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return header
			}
			return cell
		}).
		Headers("Field", "Value").
		Rows(rows...)
	_, err := lipgloss.Fprintln(w, t.Render())
	return err
}

// datatugVersion resolves this build's version for the banner. buildinfo.Get
// already falls back to "dev" for `go run`/`go test`/an unstamped build.
func datatugVersion() string {
	return buildinfo.Get("datatug").Version
}
