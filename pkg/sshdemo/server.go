package sshdemo

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"charm.land/ssh"
	"charm.land/wish/v2"
	gossh "golang.org/x/crypto/ssh"
)

const (
	noPTYMessage    = "This demo needs an interactive terminal: ssh -t -p 2222 demo.datatug.app\n"
	demoFullMessage = "The DataTug demo is full, please try again in a few minutes.\r\n"

	// Session end reasons logged in structured JSON output.
	EndReasonClientExit  = "client-exit"
	EndReasonClientDrop  = "client-drop"
	EndReasonIdle        = "idle"
	EndReasonMaxDuration = "max-duration"
	EndReasonFull        = "full"
	EndReasonShutdown    = "shutdown"
	EndReasonChildCrash  = "child-crash"
)

// ServerOption customizes Server construction (primarily for tests).
type ServerOption func(*Server)

// WithSysOps overrides the OS-level seams used for uid isolation.
func WithSysOps(ops SysOps) ServerOption {
	return func(s *Server) {
		s.ops = ops
	}
}

// WithChinookBytes overrides the Chinook bytes written into the template.
func WithChinookBytes(b []byte) ServerOption {
	return func(s *Server) {
		s.chinookBytes = b
	}
}

// WithPanicHookForTest injects a callback invoked inside handleSession right
// after workspace allocation so tests can verify panic recovery & cleanup.
func WithPanicHookForTest(fn func()) ServerOption {
	return func(s *Server) {
		s.testPanicHook = fn
	}
}

// Server is the Wish-backed SSH demo supervisor.
type Server struct {
	cfg           Config
	logger        *slog.Logger
	ops           SysOps
	slots         *SlotManager
	hostSigner    gossh.Signer
	executable    string
	templateDir   string
	chinookBytes  []byte
	sshServer     *ssh.Server
	shutdownOnce  sync.Once
	shutdownCh    chan struct{}
	sessionWg     sync.WaitGroup
	testPanicHook func()
}

// NewServer prepares the host key, sessions directory, template clone, and Wish
// SSH server according to cfg.
func NewServer(ctx context.Context, cfg Config, opts ...ServerOption) (*Server, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:        cfg,
		logger:     cfg.Logger,
		ops:        DefaultSysOps(),
		slots:      NewSlotManager(cfg.MaxSessions, cfg.MaxSessionsPerIP, cfg.UIDBase, cfg.Isolation),
		shutdownCh: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.ops = s.ops.withDefaults()

	if cfg.Isolation == IsolationProcess {
		s.logger.Warn("running with process isolation (no per-session uid boundary); do not expose publicly",
			"listen", cfg.Listen,
		)
	}

	if err := checkSessionsDir(cfg.SessionsDir, cfg.Isolation, s.ops); err != nil {
		return nil, err
	}

	signer, err := LoadOrGenerateHostKey(cfg.HostKey)
	if err != nil {
		return nil, err
	}
	s.hostSigner = signer

	execPath := cfg.Executable
	if execPath == "" {
		execPath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("sshdemo: resolve executable: %w", err)
		}
	}
	s.executable = execPath

	if !cfg.SkipTemplate {
		tmplDir, err := PrepareTemplate(ctx, TemplateConfig{
			StateDir:     cfg.StateDir,
			Repo:         cfg.Repo,
			Ref:          cfg.Ref,
			ProjectDir:   cfg.ProjectDir,
			EgressProxy:  cfg.EgressProxy,
			ChinookBytes: s.chinookBytes,
		})
		if err != nil {
			return nil, err
		}
		s.templateDir = tmplDir
	}

	s.cleanStaleSessions(ctx)

	wishOpts := []ssh.Option{
		wish.WithAddress(cfg.Listen),
		ssh.AllocatePty(),
		func(srv *ssh.Server) error {
			srv.AddHostKey(signer)
			return nil
		},
	}
	if cfg.ProxyProtocol {
		wishOpts = append(wishOpts, ssh.EnableProxyProtocol())
	}

	sshSrv, err := wish.NewServer(wishOpts...)
	if err != nil {
		return nil, fmt.Errorf("sshdemo: create wish server: %w", err)
	}

	// Clamp PTY size before allocation (belt-and-suspenders alongside
	// filteringNewChannel clamping the wire pty-req before DefaultSessionHandler).
	origPtyHandler := sshSrv.PtyHandler
	sshSrv.PtyHandler = func(ctx ssh.Context, sess ssh.Session, pty ssh.Pty) (func() error, error) {
		pty.Window = ClampWindow(pty.Window)
		return origPtyHandler(ctx, sess, pty)
	}

	// Anonymous access: PasswordHandler, PublicKeyHandler, and
	// KeyboardInteractiveHandler remain nil -> charm.land/ssh sets NoClientAuth=true.
	sshSrv.Version = "datatug-demo"
	sshSrv.HandshakeTimeout = cfg.HandshakeTimeout
	if cfg.IdleTimeout > 0 {
		// Safety net for connections that never open a session channel; active
		// sessions enforce exact input-based idle timeout first.
		sshSrv.IdleTimeout = cfg.IdleTimeout + safetyMargin(cfg.IdleTimeout)
	}
	if cfg.MaxSessionDuration > 0 {
		sshSrv.MaxTimeout = cfg.MaxSessionDuration + safetyMargin(cfg.MaxSessionDuration)
	}

	// Serve ONLY session channels; refuse forwarding, subsystems, exec, and global requests.
	sshSrv.ChannelHandlers = map[string]ssh.ChannelHandler{
		"session": s.sessionChannelHandler,
	}
	sshSrv.RequestHandlers = map[string]ssh.RequestHandler{}
	sshSrv.SubsystemHandlers = map[string]ssh.SubsystemHandler{}
	sshSrv.LocalPortForwardingCallback = func(ssh.Context, string, uint32) bool { return false }
	sshSrv.ReversePortForwardingCallback = func(ssh.Context, string, uint32) bool { return false }
	sshSrv.SessionRequestCallback = func(_ ssh.Session, reqType string) bool {
		return reqType == "shell"
	}
	sshSrv.Handler = s.handleSession

	s.sshServer = sshSrv
	return s, nil
}

func safetyMargin(d time.Duration) time.Duration {
	m := d / 2
	if m < 50*time.Millisecond {
		m = 50 * time.Millisecond
	}
	if m > 5*time.Second {
		m = 5 * time.Second
	}
	return m
}

// HostSigner returns the loaded or generated SSH host signer.
func (s *Server) HostSigner() gossh.Signer {
	return s.hostSigner
}

// TemplateDir returns the prepared template directory path.
func (s *Server) TemplateDir() string {
	return s.templateDir
}

// Serve accepts SSH connections on ln until Shutdown or Close is called.
func (s *Server) Serve(ln net.Listener) error {
	err := s.sshServer.Serve(ln)
	if errors.Is(err, ssh.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepting new connections, signals active sessions to finish
// (waiting up to ShutdownGrace before killing and cleaning up every session),
// and waits for all session cleanups to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })

	// Stop accepting new connections without immediately tearing down sockets so
	// sessions get their grace period and can finish cleanup cleanly.
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- s.sshServer.Shutdown(ctx)
	}()

	sessionsDone := make(chan struct{})
	go func() {
		s.sessionWg.Wait()
		close(sessionsDone)
	}()

	select {
	case <-sessionsDone:
	case <-ctx.Done():
	}

	_ = s.sshServer.Close()
	select {
	case err := <-shutdownDone:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	case <-time.After(2 * time.Second):
		return nil
	}
}

// RunServer creates a Server, listens on cfg.Listen, serves until ctx is
// canceled, then gracefully shuts down all sessions.
func RunServer(ctx context.Context, cfg Config, opts ...ServerOption) error {
	srv, err := NewServer(ctx, cfg, opts...)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", srv.cfg.Listen)
	if err != nil {
		return fmt.Errorf("sshdemo: listen %s: %w", srv.cfg.Listen, err)
	}
	srv.logger.Info("ssh demo server listening",
		"addr", ln.Addr().String(),
		"isolation", srv.cfg.Isolation,
		"proxy_protocol", srv.cfg.ProxyProtocol,
	)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		grace := srv.cfg.ShutdownGrace + srv.cfg.CleanupTimeout + 5*time.Second
		shutCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

type sessionMetaKey struct{}

type sessionMeta struct {
	activityCh chan struct{}
	chanDone   chan struct{}
}

// sessionChannelHandler wraps incoming "session" channels so we can:
//  1. Refuse exec, subsystem, agent forwarding, X11, and disallowed env vars
//     before charm.land/ssh's DefaultSessionHandler processes them.
//  2. Clamp pty-req and window-change sizes before PTY allocation.
//  3. Observe client input reads (for input-based idle timeout) and channel
//     close (for client-drop detection).
func (s *Server) sessionChannelHandler(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
	meta := &sessionMeta{
		activityCh: make(chan struct{}, 1),
		chanDone:   make(chan struct{}),
	}
	ctx.SetValue(sessionMetaKey{}, meta)
	wrapped := &filteringNewChannel{
		NewChannel: newChan,
		meta:       meta,
	}
	ssh.DefaultSessionHandler(srv, conn, wrapped, ctx)
}

type filteringNewChannel struct {
	gossh.NewChannel
	meta *sessionMeta
}

func (f *filteringNewChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	ch, rawReqs, err := f.NewChannel.Accept()
	if err != nil {
		return nil, nil, err
	}
	filteredReqs := make(chan *gossh.Request)
	go func() {
		defer close(filteredReqs)
		defer close(f.meta.chanDone)
		for req := range rawReqs {
			switch req.Type {
			case "pty-req":
				clamped, ok := clampPtyReqPayload(req.Payload)
				if !ok {
					_ = req.Reply(false, nil)
					continue
				}
				req.Payload = clamped
				filteredReqs <- req
			case "window-change":
				clamped, ok := clampWindowPayload(req.Payload)
				if !ok {
					_ = req.Reply(false, nil)
					continue
				}
				req.Payload = clamped
				filteredReqs <- req
			case "shell":
				filteredReqs <- req
			case "env":
				var kv struct{ Key, Value string }
				if err := gossh.Unmarshal(req.Payload, &kv); err != nil || !isAllowedEnvKey(kv.Key) {
					_ = req.Reply(false, nil)
					continue
				}
				filteredReqs <- req
			default:
				// Refuses exec, subsystem, auth-agent-req@openssh.com, x11-req, etc.
				_ = req.Reply(false, nil)
			}
		}
	}()
	return &activityChannel{Channel: ch, activityCh: f.meta.activityCh}, filteredReqs, nil
}

type activityChannel struct {
	gossh.Channel
	activityCh chan struct{}
}

func (a *activityChannel) Read(p []byte) (int, error) {
	n, err := a.Channel.Read(p)
	if n > 0 {
		select {
		case a.activityCh <- struct{}{}:
		default:
		}
	}
	return n, err
}

func isAllowedEnvKey(k string) bool {
	return k == "TERM" || k == "COLORTERM" || k == "LANG"
}

func clampPtyReqPayload(payload []byte) ([]byte, bool) {
	if len(payload) < 4 {
		return nil, false
	}
	termLen := binary.BigEndian.Uint32(payload[:4])
	if uint64(4)+uint64(termLen)+16 > uint64(len(payload)) {
		return nil, false
	}
	out := make([]byte, len(payload))
	copy(out, payload)
	off := 4 + int(termLen)
	w := ClampWindow(ssh.Window{
		Width:  int(binary.BigEndian.Uint32(out[off : off+4])),
		Height: int(binary.BigEndian.Uint32(out[off+4 : off+8])),
	})
	binary.BigEndian.PutUint32(out[off:off+4], uint32(w.Width))
	binary.BigEndian.PutUint32(out[off+4:off+8], uint32(w.Height))
	return out, true
}

func clampWindowPayload(payload []byte) ([]byte, bool) {
	if len(payload) < 16 {
		return nil, false
	}
	out := make([]byte, len(payload))
	copy(out, payload)
	w := ClampWindow(ssh.Window{
		Width:  int(binary.BigEndian.Uint32(out[0:4])),
		Height: int(binary.BigEndian.Uint32(out[4:8])),
	})
	binary.BigEndian.PutUint32(out[0:4], uint32(w.Width))
	binary.BigEndian.PutUint32(out[4:8], uint32(w.Height))
	return out, true
}

func clientIPFromAddr(addr net.Addr) string {
	if addr == nil {
		return "unknown"
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func (s *Server) handleSession(sess ssh.Session) {
	s.sessionWg.Add(1)
	defer s.sessionWg.Done()

	clientIP := clientIPFromAddr(sess.RemoteAddr())
	sshUser := sess.User()

	pty, _, hasPty := sess.Pty()
	if !hasPty {
		s.logger.Info("session rejected: no pty",
			"client_ip", clientIP,
			"ssh_user", sshUser,
		)
		_, _ = io.WriteString(sess, noPTYMessage)
		_ = sess.Exit(1)
		return
	}

	slot, err := s.slots.Acquire(clientIP)
	if err != nil {
		s.logger.Info("session end",
			"session_id", "",
			"client_ip", clientIP,
			"ssh_user", sshUser,
			"pty_width", pty.Window.Width,
			"pty_height", pty.Window.Height,
			"duration_ms", 0,
			"end_reason", EndReasonFull,
			"exit_status", 1,
		)
		_, _ = io.WriteString(sess, demoFullMessage)
		_ = sess.Exit(1)
		return
	}

	sessionID, err := generateSessionID()
	if err != nil {
		s.slots.Release(slot)
		s.logger.Error("generate session id failed", "err", err)
		_ = sess.Exit(1)
		return
	}

	dir, homeDir, _, err := createSessionWorkspace(s.cfg.SessionsDir, sessionID, slot, s.cfg.Isolation, s.ops)
	if err != nil {
		s.slots.Release(slot)
		s.logger.Error("create session workspace failed", "session_id", sessionID, "err", err)
		_ = sess.Exit(1)
		return
	}

	startTime := time.Now()
	endReason := EndReasonChildCrash
	exitStatus := -1
	pgid := 0
	var waitCh chan error
	reaped := false

	s.logger.Info("session start",
		"session_id", sessionID,
		"client_ip", clientIP,
		"ssh_user", sshUser,
		"pty_width", pty.Window.Width,
		"pty_height", pty.Window.Height,
	)

	// Cleanup runs on EVERY exit path, including a panic in the handler.
	defer func() {
		if r := recover(); r != nil {
			endReason = EndReasonChildCrash
			exitStatus = -1
			s.logger.Error("panic in ssh session handler",
				"session_id", sessionID,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}

		// 1. Close PTY.
		_ = pty.Close()

		// 2. Kill the child process group and reap the child.
		if pgid > 0 {
			_ = s.ops.Kill(-pgid, syscall.SIGKILL)
		}
		if waitCh != nil && !reaped {
			select {
			case <-waitCh:
			case <-time.After(5 * time.Second):
			}
		}

		// 3. Run cleanup as the slot uid, remove the empty dir, verify no uid procs remain.
		cleanCtx, cancel := context.WithTimeout(context.Background(), s.cfg.CleanupTimeout)
		cleaned := runCleanupCommand(cleanCtx, s.executable, s.cfg.ExecPrefixArgs, dir, slot.UID, slot.GID, s.cfg.Isolation, s.ops, s.logger)
		cancel()

		// 4. Release the slot ONLY when the dir is gone and no process of that uid remains.
		if cleaned {
			s.slots.Release(slot)
		} else {
			s.logger.Error("retaining slot because cleanup did not complete cleanly",
				"session_id", sessionID,
				"slot_index", slot.Index,
				"uid", slot.UID,
			)
		}

		s.logger.Info("session end",
			"session_id", sessionID,
			"client_ip", clientIP,
			"ssh_user", sshUser,
			"pty_width", pty.Window.Width,
			"pty_height", pty.Window.Height,
			"duration_ms", time.Since(startTime).Milliseconds(),
			"end_reason", endReason,
			"exit_status", exitStatus,
		)

		if exitStatus >= 0 {
			_ = sess.Exit(exitStatus)
		} else {
			_ = sess.Exit(1)
		}
	}()

	if s.testPanicHook != nil {
		s.testPanicHook()
	}

	if s.cfg.Isolation == IsolationUID && pty.Slave != nil {
		if name := pty.Name(); name != "" {
			if err := s.ops.Chown(name, slot.UID, slot.GID); err != nil {
				s.logger.Warn("chown pty slave failed", "session_id", sessionID, "pty", name, "err", err)
			}
		}
	}

	deadline := startTime.Add(s.cfg.MaxSessionDuration)
	term, colorTerm := extractClientTermAndColorTerm(pty.Term, sess.Environ())
	childArgs := append(append([]string(nil), s.cfg.ExecPrefixArgs...), BuildChildArgs(s.cfg, dir, s.templateDir, sessionID, deadline)...)
	cmd := exec.Command(s.executable, childArgs...)
	cmd.Dir = dir
	cmd.Env = BuildChildEnv(homeDir, term, colorTerm, s.cfg.EgressProxy)
	if s.cfg.Isolation == IsolationUID {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: s.ops.CredentialFor(slot.UID, slot.GID),
		}
	}

	if err := pty.Start(cmd, ssh.WithJobControl()); err != nil {
		s.logger.Error("start session child failed", "session_id", sessionID, "err", err)
		endReason = EndReasonChildCrash
		exitStatus = 1
		return
	}
	if pty.Slave != nil {
		_ = pty.Slave.Close()
	}
	pgid = cmd.Process.Pid

	waitCh = make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var activityCh <-chan struct{}
	var chanDone <-chan struct{}
	if meta, ok := sess.Context().Value(sessionMetaKey{}).(*sessionMeta); ok && meta != nil {
		activityCh = meta.activityCh
		chanDone = meta.chanDone
	}

	var idleTimer *time.Timer
	var idleC <-chan time.Time
	if s.cfg.IdleTimeout > 0 {
		idleTimer = time.NewTimer(s.cfg.IdleTimeout)
		defer idleTimer.Stop()
		idleC = idleTimer.C
	}

	var maxTimer *time.Timer
	var maxC <-chan time.Time
	if s.cfg.MaxSessionDuration > 0 {
		maxTimer = time.NewTimer(s.cfg.MaxSessionDuration)
		defer maxTimer.Stop()
		maxC = maxTimer.C
	}

	for {
		select {
		case waitErr := <-waitCh:
			reaped = true
			exitStatus = exitCodeFromWaitErr(waitErr)
			if waitErr == nil {
				endReason = EndReasonClientExit
			} else {
				endReason = EndReasonChildCrash
			}
			drainPTYMaster(pty.Master, 150*time.Millisecond)
			return

		case <-activityCh:
			if idleTimer != nil {
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(s.cfg.IdleTimeout)
			}

		case <-chanDone:
			endReason = EndReasonClientDrop
			exitStatus = -1
			return

		case <-sess.Context().Done():
			endReason = EndReasonClientDrop
			exitStatus = -1
			return

		case <-idleC:
			endReason = EndReasonIdle
			exitStatus = -1
			return

		case <-maxC:
			endReason = EndReasonMaxDuration
			exitStatus = -1
			return

		case <-s.shutdownCh:
			endReason = EndReasonShutdown
			exitStatus = -1
			if pgid > 0 {
				_ = s.ops.Kill(-pgid, syscall.SIGTERM)
			}
			graceTimer := time.NewTimer(s.cfg.ShutdownGrace)
			select {
			case waitErr := <-waitCh:
				graceTimer.Stop()
				reaped = true
				exitStatus = exitCodeFromWaitErr(waitErr)
			case <-graceTimer.C:
			}
			return
		}
	}
}
