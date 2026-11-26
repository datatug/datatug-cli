package sshdemo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Default configuration values matching spec/features/ssh-demo/README.md.
const (
	DefaultListen             = ":2222"
	DefaultStateDir           = "/var/lib/datatug-demo"
	DefaultSessionsDir        = "/run/datatug-demo/sessions"
	DefaultRepo               = "https://github.com/datatug/datatug-demo-project"
	DefaultProjectDir         = "demo-project-1"
	DefaultMaxSessions        = 6
	DefaultMaxSessionsPerIP   = 2
	DefaultIdleTimeout        = 15 * time.Minute
	DefaultMaxSessionDuration = 60 * time.Minute
	DefaultHandshakeTimeout   = 20 * time.Second
	DefaultShellStartTimeout  = 30 * time.Second
	DefaultSessionMemQuota    = 384 << 20
	DefaultSessionDiskQuota   = 160 << 20
	DefaultWatchdogInterval   = time.Second
	DefaultUIDBase            = 20000
	DefaultGitHubClientID     = "Ov23liAIKfguW2oYiore"
	DefaultSaveBranch         = "datatug-demo"

	IsolationUID     = "uid"
	IsolationProcess = "process"

	DefaultShutdownGrace  = 10 * time.Second
	DefaultCleanupTimeout = 30 * time.Second

	// Child rlimit defaults (spec D3).
	RlimitAS     = 8589934592 // 8 GiB of address space for the cgo binary (Go + DuckDB)
	RlimitCPU    = 900        // 900 s
	RlimitFSize  = 67108864   // 64 MiB
	RlimitNoFile = 512
	// ChildGoMemLimit is the GOMEMLIMIT of every session child.
	ChildGoMemLimit = "256MiB"
	RlimitNProc     = 256 // per uid; Linux counts threads too (Go runtime + DuckDB)

	// PTY initial dimension bounds (spec D3).
	MinPTYWidth  = 1
	MinPTYHeight = 1
	MaxPTYWidth  = 500
	MaxPTYHeight = 200

	// Health check defaults.
	DefaultHealthAddr    = "127.0.0.1:2222"
	DefaultHealthTimeout = 5 * time.Second

	// Edge forwarder and proxy defaults.
	DefaultEdgeListen          = ":2222"
	DefaultEdgeUpstream        = "demo:2222"
	DefaultEdgeProxyListen     = ":3128"
	DefaultEdgeMaxConns        = 256
	DefaultEdgeMaxConnsPerIP   = 8
	DefaultEdgeMaxTunnels      = 64
	DefaultEdgeMaxTunnelsPerIP = DefaultEdgeMaxTunnels
	DefaultProxyMaxConns       = 128
	DefaultSessionMaxTunnels   = 8
	edgeProxyGroup             = 10001
	DefaultEdgeIdleTimeout     = 5 * time.Minute
)

// DefaultEdgeAllow is the default allow-list of hosts reachable via the edge
// HTTP CONNECT proxy (spec D4).
var DefaultEdgeAllow = []string{
	"github.com",
	"api.github.com",
	"codeload.github.com",
	"cloud.openvaultdb.com",
}

// Config configures the SSH demo supervisor server (`datatug demo ssh-server`).
type Config struct {
	Listen                 string
	ProxyProtocol          bool
	ProxyProtocolTrust     []string
	HostKey                string
	StateDir               string
	SessionsDir            string
	Repo                   string
	Ref                    string
	ProjectDir             string
	MaxSessions            int
	MaxSessionsPerIP       int
	MaxConnections         int // zero defaults to twice MaxSessions
	IdleTimeout            time.Duration
	MaxSessionDuration     time.Duration
	HandshakeTimeout       time.Duration
	ShellStartTimeout      time.Duration
	SessionMemQuota        uint64
	SessionDiskQuota       uint64
	Isolation              string
	UIDBase                int
	AllowInsecureIsolation bool
	GitHubClientID         string
	SaveBranch             string
	EgressProxy            string
	EgressProxyAuthFile    string
	MaxSessionTunnels      int
	MaxEgressTunnels       int

	// Optional overrides for logging, lifecycle tuning and tests.
	Logger           *slog.Logger
	Executable       string
	ExecPrefixArgs   []string
	ShutdownGrace    time.Duration
	CleanupTimeout   time.Duration
	WatchdogInterval time.Duration
	SkipTemplate     bool
}

// DefaultConfig returns a Config populated with the specification defaults.
func DefaultConfig() Config {
	return Config{
		Listen:             DefaultListen,
		StateDir:           DefaultStateDir,
		SessionsDir:        DefaultSessionsDir,
		Repo:               DefaultRepo,
		ProjectDir:         DefaultProjectDir,
		MaxSessions:        DefaultMaxSessions,
		MaxSessionsPerIP:   DefaultMaxSessionsPerIP,
		IdleTimeout:        DefaultIdleTimeout,
		MaxSessionDuration: DefaultMaxSessionDuration,
		HandshakeTimeout:   DefaultHandshakeTimeout,
		ShellStartTimeout:  DefaultShellStartTimeout,
		SessionMemQuota:    DefaultSessionMemQuota,
		SessionDiskQuota:   DefaultSessionDiskQuota,
		WatchdogInterval:   DefaultWatchdogInterval,
		MaxSessionTunnels:  DefaultSessionMaxTunnels,
		MaxEgressTunnels:   DefaultEdgeMaxTunnels,
		Isolation:          IsolationUID,
		UIDBase:            DefaultUIDBase,
		GitHubClientID:     DefaultGitHubClientID,
		SaveBranch:         DefaultSaveBranch,
		ShutdownGrace:      DefaultShutdownGrace,
		CleanupTimeout:     DefaultCleanupTimeout,
	}
}

// WithDefaults returns a copy of c with zero-valued fields replaced by defaults.
func (c Config) WithDefaults() Config {
	if c.Listen == "" {
		c.Listen = DefaultListen
	}
	if c.StateDir == "" {
		c.StateDir = DefaultStateDir
	}
	if c.SessionsDir == "" {
		c.SessionsDir = DefaultSessionsDir
	}
	if c.HostKey == "" {
		c.HostKey = filepath.Join(c.StateDir, "ssh_host_ed25519_key")
	}
	if c.Repo == "" {
		c.Repo = DefaultRepo
	}
	if c.ProjectDir == "" {
		c.ProjectDir = DefaultProjectDir
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = DefaultMaxSessions
	}
	if c.MaxSessionsPerIP == 0 {
		c.MaxSessionsPerIP = DefaultMaxSessionsPerIP
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 2 * min(max(c.MaxSessions, 0), math.MaxInt/2)
	}
	if c.MaxSessionTunnels == 0 {
		c.MaxSessionTunnels = DefaultSessionMaxTunnels
	}
	if c.MaxEgressTunnels == 0 {
		c.MaxEgressTunnels = DefaultEdgeMaxTunnels
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.MaxSessionDuration == 0 {
		c.MaxSessionDuration = DefaultMaxSessionDuration
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.ShellStartTimeout == 0 {
		c.ShellStartTimeout = DefaultShellStartTimeout
	}
	if c.SessionMemQuota == 0 {
		c.SessionMemQuota = DefaultSessionMemQuota
	}
	if c.SessionDiskQuota == 0 {
		c.SessionDiskQuota = DefaultSessionDiskQuota
	}
	if c.WatchdogInterval == 0 {
		c.WatchdogInterval = DefaultWatchdogInterval
	}
	if c.Isolation == "" {
		c.Isolation = IsolationUID
	}
	if c.UIDBase == 0 {
		c.UIDBase = DefaultUIDBase
	}
	if c.GitHubClientID == "" {
		c.GitHubClientID = DefaultGitHubClientID
	}
	if c.SaveBranch == "" {
		c.SaveBranch = DefaultSaveBranch
	}
	if c.ShutdownGrace == 0 {
		c.ShutdownGrace = DefaultShutdownGrace
	}
	if c.CleanupTimeout == 0 {
		c.CleanupTimeout = DefaultCleanupTimeout
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	return c
}

// ResolvedHostKey returns the effective host key path after applying defaults.
func (c Config) ResolvedHostKey() string {
	return c.WithDefaults().HostKey
}

// Validate checks that the configuration satisfies all safety and sanity rules.
func (c Config) Validate() error {
	if c.MaxSessions < 1 {
		return fmt.Errorf("sshdemo: max-sessions must be >= 1, got %d", c.MaxSessions)
	}
	if c.MaxSessionsPerIP < 1 {
		return fmt.Errorf("sshdemo: max-sessions-per-ip must be >= 1, got %d", c.MaxSessionsPerIP)
	}
	if c.MaxConnections < 0 {
		return fmt.Errorf("sshdemo: max-connections must be >= 0, got %d", c.MaxConnections)
	}
	if c.MaxSessionTunnels < 1 || c.MaxEgressTunnels < 1 {
		return errors.New("sshdemo: egress tunnel limits must be positive")
	}
	if c.ProxyProtocol && len(c.ProxyProtocolTrust) == 0 {
		return errors.New("sshdemo: --proxy-protocol requires --proxy-protocol-trust")
	}
	if c.IdleTimeout < 0 {
		return fmt.Errorf("sshdemo: idle-timeout must be >= 0, got %s", c.IdleTimeout)
	}
	if c.MaxSessionDuration < 0 {
		return fmt.Errorf("sshdemo: max-session-duration must be >= 0, got %s", c.MaxSessionDuration)
	}
	if c.HandshakeTimeout < 0 {
		return fmt.Errorf("sshdemo: handshake-timeout must be >= 0, got %s", c.HandshakeTimeout)
	}
	if c.ShellStartTimeout <= 0 {
		return fmt.Errorf("sshdemo: shell-start-timeout must be positive, got %s", c.ShellStartTimeout)
	}
	if c.WatchdogInterval <= 0 {
		return fmt.Errorf("sshdemo: watchdog interval must be positive, got %s", c.WatchdogInterval)
	}
	if err := validateRelProjectDir(c.ProjectDir); err != nil {
		return err
	}
	switch c.Isolation {
	case IsolationUID:
		return c.validateSlotUIDs(os.Getuid(), os.Geteuid())
	case IsolationProcess:
		if !isLoopbackListen(c.Listen) && !c.AllowInsecureIsolation {
			return fmt.Errorf("sshdemo: --isolation=process on non-loopback address %q requires --allow-insecure-isolation", c.Listen)
		}
	default:
		return fmt.Errorf("sshdemo: invalid isolation mode %q (want %q or %q)", c.Isolation, IsolationUID, IsolationProcess)
	}
	return nil
}

func (c Config) validateSlotUIDs(realUID, effectiveUID int) error {
	maxUID := min(uint64(math.MaxUint32-1), uint64(math.MaxInt))
	if c.UIDBase < 1 || uint64(c.UIDBase)+uint64(c.MaxSessions)-1 > maxUID {
		return fmt.Errorf("sshdemo: invalid slot UID range (uid-base %d, max-sessions %d)", c.UIDBase, c.MaxSessions)
	}
	if uidInRange(realUID, c.UIDBase, c.MaxSessions) || uidInRange(effectiveUID, c.UIDBase, c.MaxSessions) {
		return errors.New("sshdemo: slot UID range includes the supervisor UID")
	}
	if c.EgressProxyAuthFile != "" && uidInRange(edgeProxyGroup, c.UIDBase, c.MaxSessions) {
		return errors.New("sshdemo: slot UID range includes the edge proxy credential group")
	}
	return nil
}

func validateRelProjectDir(rel string) error {
	if rel == "" {
		return errors.New("sshdemo: project-dir must not be empty")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("sshdemo: project-dir %q must be relative", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("sshdemo: project-dir %q escapes template directory", rel)
	}
	return nil
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func uidInRange(uid, base, count int) bool {
	return uid >= base && uid-base < count
}

// EdgeConfig configures `datatug demo ssh-edge` (spec D4).
type EdgeConfig struct {
	Listen          string
	Upstream        string
	ProxyListen     string
	Allow           []string
	MaxConns        int
	MaxConnsPerIP   int
	MaxTunnels      int
	MaxTunnelsPerIP int // zero defaults to MaxTunnels
	MaxProxyConns   int
	ProxyAuthIP     string
	ProxyAuthFile   string
	IdleTimeout     time.Duration
	DialTimeout     time.Duration

	Logger       *slog.Logger
	LookupIPAddr func(ctx context.Context, host string) ([]net.IPAddr, error)
	DialContext  func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DefaultEdgeConfig returns an EdgeConfig populated with specification defaults.
// WithDefaults resolves the per-IP tunnel cap from the configured global cap.
func DefaultEdgeConfig() EdgeConfig {
	allow := make([]string, len(DefaultEdgeAllow))
	copy(allow, DefaultEdgeAllow)
	return EdgeConfig{
		Listen:        DefaultEdgeListen,
		Upstream:      DefaultEdgeUpstream,
		ProxyListen:   DefaultEdgeProxyListen,
		Allow:         allow,
		MaxConns:      DefaultEdgeMaxConns,
		MaxConnsPerIP: DefaultEdgeMaxConnsPerIP,
		MaxTunnels:    DefaultEdgeMaxTunnels,
		MaxProxyConns: DefaultProxyMaxConns,
		IdleTimeout:   DefaultEdgeIdleTimeout,
		DialTimeout:   10 * time.Second,
	}
}

// WithDefaults returns a copy of c with zero-valued fields replaced by defaults.
func (c EdgeConfig) WithDefaults() EdgeConfig {
	if c.Listen == "" {
		c.Listen = DefaultEdgeListen
	}
	if c.Upstream == "" {
		c.Upstream = DefaultEdgeUpstream
	}
	if c.ProxyListen == "" {
		c.ProxyListen = DefaultEdgeProxyListen
	}
	if c.Allow == nil {
		c.Allow = append([]string(nil), DefaultEdgeAllow...)
	}
	if c.MaxConns <= 0 {
		c.MaxConns = DefaultEdgeMaxConns
	}
	if c.MaxConnsPerIP <= 0 {
		c.MaxConnsPerIP = DefaultEdgeMaxConnsPerIP
	}
	if c.MaxTunnels <= 0 {
		c.MaxTunnels = DefaultEdgeMaxTunnels
	}
	if c.MaxTunnelsPerIP <= 0 {
		c.MaxTunnelsPerIP = c.MaxTunnels
	}
	if c.MaxProxyConns <= 0 {
		c.MaxProxyConns = DefaultProxyMaxConns
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultEdgeIdleTimeout
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if c.LookupIPAddr == nil {
		c.LookupIPAddr = net.DefaultResolver.LookupIPAddr
	}
	if c.DialContext == nil {
		d := &net.Dialer{Timeout: c.DialTimeout}
		c.DialContext = d.DialContext
	}
	return c
}
