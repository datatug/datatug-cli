package commands

// The public SSH demo's supervisor side: `datatug demo ssh-server`,
// `datatug demo ssh-health` and `datatug demo ssh-edge`. The SSH machinery
// lives in pkg/sshdemo; these commands only parse flags and run it.
// Design: spec/features/ssh-demo/README.md.

import (
	"context"
	"fmt"
	"math/big"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/datatug/datatug-cli/pkg/sshdemo"
	"github.com/spf13/cobra"
)

// Package seams so unit tests can verify flag binding without binding real sockets.
var (
	sshDemoRunServer             = func(ctx context.Context, cfg sshdemo.Config) error { return sshdemo.RunServer(ctx, cfg) }
	sshDemoPrintHostKey          = sshdemo.PrintHostKey
	sshDemoCheckHealth           = sshdemo.CheckHealth
	sshDemoCheckHealthProxyLocal = sshdemo.CheckHealthProxyLocal
	sshDemoRunEdge               = sshdemo.RunEdge
)

func demoSSHServerCommand() *cobra.Command {
	cfg := sshdemo.DefaultConfig()
	var printHostKey bool
	var memQuota, diskQuota, cleanupMemLimit, slotReuseMemoryThreshold string

	cmd := &cobra.Command{
		Use:   "ssh-server",
		Short: "Serve the DataTug workspace over SSH to anonymous visitors (public demo)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if printHostKey {
				return sshDemoPrintHostKey(cfg.ResolvedHostKey(), cmd.OutOrStdout())
			}
			if cfg.MaxQueueWait <= 0 {
				return fmt.Errorf("--max-queue-wait must be positive, got %s", cfg.MaxQueueWait)
			}
			for _, quota := range []struct {
				flag, value string
				target      *uint64
			}{
				{"session-mem-quota", memQuota, &cfg.SessionMemQuota},
				{"session-disk-quota", diskQuota, &cfg.SessionDiskQuota},
				{"cleanup-mem-limit", cleanupMemLimit, &cfg.CleanupMemLimit},
				{"slot-reuse-memory-threshold", slotReuseMemoryThreshold, &cfg.SlotReuseMemoryThreshold},
			} {
				size, ok := parseSSHSessionQuota(quota.value)
				if !ok {
					return fmt.Errorf("--%s requires a positive byte size, got %q", quota.flag, quota.value)
				}
				*quota.target = size
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			return sshDemoRunServer(ctx, cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Listen, "listen", cfg.Listen, "TCP address to listen on")
	f.BoolVar(&cfg.ProxyProtocol, "proxy-protocol", cfg.ProxyProtocol, "Expect HAProxy PROXY protocol headers on incoming connections")
	f.StringSliceVar(&cfg.ProxyProtocolTrust, "proxy-protocol-trust", nil, "Trusted PROXY protocol peer CIDRs (required when PROXY protocol is enabled)")
	f.StringVar(&cfg.HostKey, "host-key", "", "Path to SSH host private key (default <state-dir>/ssh_host_ed25519_key)")
	f.BoolVar(&printHostKey, "print-host-key", false, "Print the public host key and SHA256 fingerprint, then exit")
	f.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "Directory for persistent supervisor state (host key, template)")
	f.StringVar(&cfg.SessionsDir, "sessions-dir", cfg.SessionsDir, "Directory for per-session workspaces")
	f.StringVar(&cfg.Repo, "repo", cfg.Repo, "Git URL or path of the canonical demo repository")
	f.StringVar(&cfg.Ref, "ref", cfg.Ref, "Git branch or ref of the demo repository (empty = default branch)")
	f.StringVar(&cfg.ProjectDir, "project-dir", cfg.ProjectDir, "Project sub-directory inside the demo repository")
	f.IntVar(&cfg.MaxSessions, "max-sessions", cfg.MaxSessions, "Maximum concurrent SSH sessions")
	f.IntVar(&cfg.MaxQueue, "max-queue", cfg.MaxQueue, "Maximum visitors waiting for a session (zero disables the queue)")
	f.DurationVar(&cfg.MaxQueueWait, "max-queue-wait", cfg.MaxQueueWait, "Maximum time a visitor can wait for a session")
	f.IntVar(&cfg.MaxConnections, "max-connections", cfg.MaxConnections, "Maximum authenticated SSH connections (default max-sessions + max-queue, at least twice max-sessions)")
	f.IntVar(&cfg.MaxSessionsPerIP, "max-sessions-per-ip", cfg.MaxSessionsPerIP, "Maximum concurrent authenticated SSH connections per client IP")
	f.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "Idle timeout with no client input")
	f.DurationVar(&cfg.MaxSessionDuration, "max-session-duration", cfg.MaxSessionDuration, "Maximum lifetime of a single session")
	f.DurationVar(&cfg.HandshakeTimeout, "handshake-timeout", cfg.HandshakeTimeout, "Maximum duration allowed for SSH handshake")
	f.DurationVar(&cfg.ShellStartTimeout, "shell-start-timeout", cfg.ShellStartTimeout, "Maximum time from SSH authentication to child startup")
	f.StringVar(&memQuota, "session-mem-quota", strconv.FormatUint(cfg.SessionMemQuota, 10), "Memory contribution to the per-slot cgroup cap (e.g. 384MiB)")
	f.StringVar(&diskQuota, "session-disk-quota", strconv.FormatUint(cfg.SessionDiskQuota, 10), "Tmpfs contribution to the per-slot cgroup cap (e.g. 160MiB)")
	f.StringVar(&cfg.Isolation, "isolation", cfg.Isolation, "Session isolation mode: uid or process")
	f.StringVar(&cfg.CgroupRoot, "cgroup-root", cfg.CgroupRoot, "Writable cgroup v2 mount rooted at the container scope (uid isolation)")
	f.StringVar(&cleanupMemLimit, "cleanup-mem-limit", strconv.FormatUint(cfg.CleanupMemLimit, 10), "Memory cap for the cleanup cgroup (e.g. 96MiB)")
	f.IntVar(&cfg.CleanupPIDsLimit, "cleanup-pids-limit", cfg.CleanupPIDsLimit, "PID and thread cap for the cleanup cgroup")
	f.StringVar(&slotReuseMemoryThreshold, "slot-reuse-memory-threshold", strconv.FormatUint(cfg.SlotReuseMemoryThreshold, 10), "Slot memory.current must fall below this size before reuse (e.g. 16MiB)")
	f.IntVar(&cfg.UIDBase, "uid-base", cfg.UIDBase, "Base Unix UID for slot allocation in uid isolation mode")
	f.BoolVar(&cfg.AllowInsecureIsolation, "allow-insecure-isolation", cfg.AllowInsecureIsolation, "Allow --isolation=process on non-loopback listen addresses")
	f.StringVar(&cfg.GitHubClientID, "github-client-id", cfg.GitHubClientID, "GitHub OAuth App client ID for device flow")
	f.StringVar(&cfg.SaveBranch, "save-branch", cfg.SaveBranch, "Branch name used when saving visitor changes to their fork")
	f.StringVar(&cfg.EgressProxy, "egress-proxy", cfg.EgressProxy, "Upstream CONNECT proxy URL (unix:///absolute/path, http, or https)")
	f.IntVar(&cfg.MaxRelaySocketsPerUID, "max-relay-sockets-per-uid", cfg.MaxRelaySocketsPerUID, "Maximum pending and active relay sockets per slot UID")
	f.IntVar(&cfg.MaxSessionTunnels, "max-session-tunnels", cfg.MaxSessionTunnels, "Maximum concurrent CONNECT tunnels per demo session")
	f.IntVar(&cfg.MaxEgressTunnels, "max-egress-tunnels", cfg.MaxEgressTunnels, "Maximum concurrent relay CONNECT tunnels (match the edge limit)")
	return cmd
}

func parseSSHSessionQuota(value string) (uint64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	unitStart := strings.IndexFunc(value, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.'
	})
	if unitStart < 0 {
		unitStart = len(value)
	}
	multiplier := map[string]uint64{
		"": 1, "b": 1,
		"kb": 1_000, "mb": 1_000_000, "gb": 1_000_000_000, "tb": 1_000_000_000_000,
		"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
	}[strings.TrimSpace(value[unitStart:])]
	number, ok := new(big.Rat).SetString(value[:unitStart])
	if !ok || multiplier == 0 {
		return 0, false
	}
	bytes := number.Mul(number, new(big.Rat).SetUint64(multiplier))
	if bytes.Sign() <= 0 || !bytes.IsInt() || !bytes.Num().IsUint64() {
		return 0, false
	}
	return bytes.Num().Uint64(), true
}

func demoSSHHealthCommand() *cobra.Command {
	addr := sshdemo.DefaultHealthAddr
	timeout := sshdemo.DefaultHealthTimeout
	proxyProtocol := false

	cmd := &cobra.Command{
		Use:   "ssh-health",
		Short: "Exit 0 when the SSH demo server answers (container health check)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if proxyProtocol {
				return sshDemoCheckHealthProxyLocal(cmd.Context(), addr, timeout)
			}
			return sshDemoCheckHealth(cmd.Context(), addr, timeout)
		},
	}

	f := cmd.Flags()
	f.StringVar(&addr, "addr", addr, "TCP address of the SSH server to probe")
	f.DurationVar(&timeout, "timeout", timeout, "Maximum time to wait for SSH banner")
	f.BoolVar(&proxyProtocol, "proxy-protocol", false, "Send a PROXY v2 LOCAL header first (for a server started with --proxy-protocol)")
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
	f.StringVar(&cfg.ProxyListen, "proxy-listen", cfg.ProxyListen, "Absolute Unix socket path for the allow-listing CONNECT proxy")
	f.StringSliceVar(&cfg.Allow, "allow", cfg.Allow, "Comma-separated list of hostnames allowed through the CONNECT proxy")
	f.IntVar(&cfg.MaxConns, "max-conns", cfg.MaxConns, "Maximum concurrent forwarded TCP connections")
	f.IntVar(&cfg.MaxConnsPerIP, "max-conns-per-ip", cfg.MaxConnsPerIP, "Maximum concurrent forwarded TCP connections per client IP")
	f.IntVar(&cfg.MaxTunnels, "max-tunnels", cfg.MaxTunnels, "Maximum concurrent CONNECT tunnels")
	f.IntVar(&cfg.MaxTunnelsPerIP, "max-tunnels-per-ip", cfg.MaxTunnelsPerIP, "Maximum concurrent CONNECT tunnels per client IP (default equals max-tunnels)")
	f.IntVar(&cfg.MaxProxyConns, "max-proxy-conns", cfg.MaxProxyConns, "Maximum accepted HTTP proxy connections")
	f.DurationVar(&cfg.IdleTimeout, "idle-timeout", cfg.IdleTimeout, "Idle timeout on CONNECT tunnels")
	return cmd
}
