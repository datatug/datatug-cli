package commands

// specscore: feature/cli/web-ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/datatug/datatug-cli/pkg/webui"
	"github.com/spf13/cobra"
)

// Test seams.
var (
	webUIStore         = webui.DefaultStore
	webUIReleaseSource = func() webui.ReleaseSource {
		return webui.ReleaseSource{Repo: webui.DefaultReleaseRepo, APIBase: "https://api.github.com", Client: &http.Client{Timeout: 10 * time.Minute}}
	}
)

const webUIOfflineHelp = "Install it from a release archive (works offline):\n" +
	"  datatug web-ui install ./datatug-web-ui-<version>.tar.gz\n" +
	"or download the newest compatible release (needs internet):\n" +
	"  datatug web-ui install --latest\n" +
	"Releases: https://github.com/" + webui.DefaultReleaseRepo + "/releases"

// webUICommand wires `datatug web-ui install|update|list|use|unpin|rollback|remove`.
func webUICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "web-ui",
		Short: "Install and manage the local DataTug web UI that `datatug serve --web-ui` serves",
		Long: "Manage locally installed versions of the compiled DataTug web UI.\n\n" +
			"Installed versions live in ~/datatug/web-ui (override with $" + webui.DirEnv + ").\n" +
			"Nothing here downloads anything unless you pass --latest or run `update`.",
	}
	cmd.AddCommand(webUIInstallCommand(), webUIUpdateCommand(), webUIListCommand(),
		webUIUseCommand(), webUIUnpinCommand(), webUIRollbackCommand(), webUIRemoveCommand())
	return cmd
}

func webUIInstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install [archive.tar.gz]",
		Short: "Install a web UI from a local archive (offline), or the newest compatible release with --latest",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			latest, _ := cmd.Flags().GetBool("latest")
			digest, _ := cmd.Flags().GetString("sha256")
			out := cmd.OutOrStdout()
			switch {
			case latest && len(args) > 0:
				return Exit("pass an archive or --latest, not both", exitCodeUsage)
			case latest:
				return webUIInstallLatest(cmd.Context(), out)
			case len(args) == 0:
				return Exit("name an archive to install, or pass --latest to download one\n"+webUIOfflineHelp, exitCodeUsage)
			}
			return webUIInstallFile(out, args[0], digest)
		},
	}
	cmd.Flags().Bool("latest", false, "Download and install the newest release this CLI can serve (needs internet)")
	cmd.Flags().String("sha256", "", "Expected SHA-256 of the archive (defaults to <archive>.sha256 when present)")
	return cmd
}

func webUIUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Download and install the newest compatible web UI release (needs internet); does not change a pin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return webUIInstallLatest(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func webUIInstallFile(out io.Writer, archive, digestFlag string) error {
	opts := webui.InstallOptions{}
	if digestFlag != "" {
		d, err := webui.ParseDigest(digestFlag)
		if err != nil {
			return Exit("--sha256: "+err.Error(), exitCodeUsage)
		}
		opts = webui.InstallOptions{ExpectedSHA256: d, Source: webui.DigestFromFlag}
	} else {
		d, ok, err := webui.SidecarDigest(archive)
		if err != nil {
			return Exit(err.Error(), exitCodeUsage)
		}
		if ok {
			opts = webui.InstallOptions{ExpectedSHA256: d, Source: webui.DigestFromSidecar}
		}
	}
	installed, err := webUIStore().InstallArchive(archive, opts)
	if err != nil {
		return Exit("web UI not installed: "+err.Error(), exitCodeUsage)
	}
	printInstalled(out, installed)
	return nil
}

func webUIInstallLatest(ctx context.Context, out io.Writer) error {
	source := webUIReleaseSource()
	_, _ = fmt.Fprintf(out, "Looking for the newest compatible web UI in github.com/%s ...\n", source.Repo)
	candidate, err := source.Latest(ctx)
	if err != nil {
		return Exit(err.Error()+"\n"+webUIOfflineHelp, exitCodeDatabase)
	}
	tmp, err := os.MkdirTemp("", "datatug-web-ui-download-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	_, _ = fmt.Fprintf(out, "Downloading web UI %s ...\n", candidate.Manifest.Version)
	archive, digest, err := source.Download(ctx, candidate, tmp)
	if err != nil {
		return Exit("download failed: "+err.Error(), exitCodeDatabase)
	}
	installed, err := webUIStore().InstallArchive(archive, webui.InstallOptions{ExpectedSHA256: digest, Source: webui.DigestFromRelease})
	if err != nil {
		return Exit("web UI not installed: "+err.Error(), exitCodeDatabase)
	}
	printInstalled(out, installed)
	return nil
}

func printInstalled(out io.Writer, in webui.Installed) {
	verb := "installed"
	if in.Reused {
		verb = "already installed and verified"
	}
	compat := "compatible with this CLI"
	if err := webui.Compatible(in.Manifest); err != nil {
		compat = "NOT servable by this CLI: " + err.Error()
	}
	_, _ = fmt.Fprintf(out, "web UI %s %s (%s)\n  archive sha256: %s\n  digest checked against: %s\n  files verified: %d\n  location: %s\n",
		in.Manifest.Version, verb, compat, in.ArchiveSHA256, in.Source, len(in.Manifest.Files), in.Dir)
	if in.Source == webui.DigestNone {
		_, _ = fmt.Fprintln(out, "  note: no expected digest was given; pass --sha256 <digest from a trusted channel> to check the archive itself")
	}
}

func webUIListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed web UI versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := webUIStore()
			entries, err := store.List()
			if err != nil {
				return err
			}
			st, err := store.LoadState()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(entries) == 0 {
				_, _ = fmt.Fprintln(out, webui.ErrNotInstalled.Error()+".\n"+webUIOfflineHelp)
				return nil
			}
			for _, e := range entries {
				marks := ""
				if e.Version == st.Pinned {
					marks += " [pinned]"
				}
				if e.Version == st.Active {
					marks += " [last served]"
				}
				status := "compatible"
				if e.Incompatible != nil {
					status = e.Incompatible.Error()
				}
				_, _ = fmt.Fprintf(out, "%s%s  needs local API %s  %s\n", e.Version, marks, e.Manifest.Requires.LocalAPI, status)
			}
			_, _ = fmt.Fprintf(out, "this CLI serves local API %s and web UI >= %s\n", webui.LocalAPIVersion, webui.MinFrontendVersion)
			return nil
		},
	}
}

func webUIUseCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "use <version>",
		Short: "Pin the web UI version `serve --web-ui` serves",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := webUIStore().Use(args[0])
			if err != nil {
				return Exit(err.Error(), exitCodeUsage)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "web UI %s pinned (previous: %s)\n", st.Pinned, orNone(st.Previous))
			return nil
		},
	}
}

func webUIUnpinCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unpin",
		Short: "Serve the newest compatible installed web UI again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := webUIStore().Unpin(); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "web UI unpinned: serve --web-ui uses the newest compatible installed version")
			return nil
		},
	}
}

func webUIRollbackCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Pin the previously used web UI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := webUIStore().Rollback()
			if err != nil {
				return Exit(err.Error(), exitCodeUsage)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "web UI rolled back: %s pinned\n", st.Pinned)
			return nil
		},
	}
}

func webUIRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <version>",
		Short: "Delete an installed web UI version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := webUIStore().Remove(args[0]); err != nil {
				return Exit(err.Error(), exitCodeUsage)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "web UI %s removed\n", args[0])
			return nil
		},
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// errWebUINotInstalled is what serve --web-ui says with nothing installed.
func errWebUINotInstalled(err error) error {
	if errors.Is(err, webui.ErrNotInstalled) {
		return Exit("serve --web-ui: "+err.Error()+".\n"+webUIOfflineHelp, exitCodeUsage)
	}
	return Exit("serve --web-ui: "+err.Error(), exitCodeUsage)
}
