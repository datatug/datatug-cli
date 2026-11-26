package commands

// The public SSH demo's supervisor side: `datatug demo ssh-server`,
// `datatug demo ssh-health` and `datatug demo ssh-edge`. The SSH machinery
// lives in pkg/sshdemo; these commands only parse flags and run it.
// Design: spec/features/ssh-demo/README.md.

import (
	"context"
	"errors"
	"io"
	"os/signal"
	"syscall"

	"github.com/datatug/datatug-cli/pkg/sshdemo"
	"github.com/spf13/cobra"
)

// Retained while sibling stub cmd_demo_ssh_session.go references it.
var errSSHDemoNotImplemented = errors.New("not implemented yet")

// Package seams so unit tests can verify flag binding without binding real sockets.
var (
	sshDemoRunServer    = func(ctx context.Context, cfg sshdemo.Config) error { return sshdemo.RunServer(ctx, cfg) }
	sshDemoPrintHostKey = func(path string, w io.Writer) error { return sshdemo.PrintHostKey(path, w) }
	sshDemoCheckHealth  = sshdemo.CheckHealth
	sshDemoRunEdge      = sshdemo.RunEdge
)

func demoSSHServerCommand() *cobra.Command {
	cfg := sshdemo.DefaultConfig()
	var printHostKey bool

	cmd := &cobra.Command{
		Use:   "ssh-server",
		Short: "Serve the DataTug workspace over SSH to anonymous visitors (public demo)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printHostKey {
				return sshDemoPrintHostKey(cfg.ResolvedHostKey(), cmd.OutOrStdout())
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			return sshDemoRunServer(ctx, cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", cfg.Listen, "TCP address to listen on")
	f.BoolVar(&cfg.ProxyProtocol, "proxy-protocol", cfg.ProxyProtocol, "Expect HAProxy PROXY protocol headers on incoming connections")
	f.StringVar(&cfg.HostKey, "host-key", "", "Path to SSH host private key (default <state-dir>/ssh_host_ed25519_key)")
	f.BoolVar(&printHostKey, "print-host-key", false, "Print the public host key and SHA256 fingerprint, then exit")
	f.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "Directory for persistent supervisor state (host key, template)")
	f.StringVar(&cfg.SessionsDir, "sessions-dir", cfg.SessionsDir, "Directory for per-session workspaces")
	f.StringVar(&cfg.Repo, "repo", cfg.Repo, "Git URL or path of the canonical demo repository")
	f.StringVar(&cfg.Ref, "ref", cfg.Ref, "Git branch or ref of the demo repository (empty = default branch)")
	f.StringVar(&cfg.ProjectDir, "project-dir", cfg.ProjectDir, "Project sub-directory inside the demo repository")
	f.IntVar(&cfg.MaxSessions, "max-sessions", cfg.MaxSessions, "Maximum concurrent SSH sessions")
	f.IntVar(&cfg.MaxSessionsPerIP, "max-sessions-per-ip", cfg.MaxSessionsPerIP, "Maximum concurrent SSH sessions per client IP")
	f.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "Idle timeout with no client input")
	f.DurationVar(&cfg.MaxSessionDuration, "max-session-duration", cfg.MaxSessionDuration, "Maximum lifetime of a single session")
	f.DurationVar(&cfg.HandshakeTimeout, "handshake-timeout", cfg.HandshakeTimeout, "Maximum duration allowed for SSH handshake")
	f.StringVar(&cfg.Isolation, "isolation", cfg.Isolation, "Session isolation mode: uid or process")
	f.IntVar(&cfg.UIDBase, "uid-base", cfg.UIDBase, "Base Unix UID for slot allocation in uid isolation mode")
	f.BoolVar(&cfg.AllowInsecureIsolation, "allow-insecure-isolation", cfg.AllowInsecureIsolation, "Allow --isolation=process on non-loopback listen addresses")
	f.StringVar(&cfg.GitHubClientID, "github-client-id", cfg.GitHubClientID, "GitHub OAuth App client ID for device flow")
	f.StringVar(&cfg.SaveBranch, "save-branch", cfg.SaveBranch, "Branch name used when saving visitor changes to their fork")
	f.StringVar(&cfg.EgressProxy, "egress-proxy", cfg.EgressProxy, "HTTP/HTTPS egress proxy URL passed to session children")
	return cmd
}

func demoSSHHealthCommand() *cobra.Command {
	addr := sshdemo.DefaultHealthAddr
	timeout := sshdemo.DefaultHealthTimeout

	cmd := &cobra.Command{
		Use:   "ssh-health",
		Short: "Exit 0 when the SSH demo server answers (container health check)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return sshDemoCheckHealth(cmd.Context(), addr, timeout)
		},
	}

	f := cmd.Flags()
	f.StringVar(&addr, "addr", addr, "TCP address of the SSH server to probe")
	f.DurationVar(&timeout, "timeout", timeout, "Maximum time to wait for SSH banner")
	return cmd
}

func demoSSHEdgeCommand() *cobra.Command {
	cfg := sshdemo.DefaultEdgeConfig()

	cmd := &cobra.Command{
		Use:   "ssh-edge",
		Short: "Forward public SSH to the demo server and proxy its allow-listed egress",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			return sshDemoRunEdge(ctx, cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", cfg.Listen, "Public TCP address for incoming SSH traffic")
	f.StringVar(&cfg.Upstream, "upstream", cfg.Upstream, "Internal SSH server address receiving PROXY v2 traffic")
	f.StringVar(&cfg.ProxyListen, "proxy-listen", cfg.ProxyListen, "Internal address for the allow-listing HTTP CONNECT proxy")
	f.StringSliceVar(&cfg.Allow, "allow", cfg.Allow, "Comma-separated list of hostnames allowed through the CONNECT proxy")
	f.IntVar(&cfg.MaxConns, "max-conns", cfg.MaxConns, "Maximum concurrent forwarded TCP connections")
	f.IntVar(&cfg.MaxConnsPerIP, "max-conns-per-ip", cfg.MaxConnsPerIP, "Maximum concurrent forwarded TCP connections per client IP")
	f.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "Idle timeout on CONNECT tunnels")
	return cmd
}
