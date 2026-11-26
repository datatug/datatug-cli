---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Public SSH demo of the DataTug terminal workspace

**Status:** Implementing
**Source Ideas:** —

## Summary

`ssh -p 2222 demo.datatug.app` gives any visitor the real DataTug terminal workspace on the demo project, with no install, account or database setup. Each visitor works in a throwaway copy of the project. A visitor who wants to keep their changes signs in to GitHub with the device flow, and DataTug saves the workspace to a branch of their fork of the demo repository. On a later visit they can reopen that fork. The SSH server is a subcommand of the existing `datatug` executable (`datatug demo ssh-server`), built on Charmbracelet Wish.

Port 22 is used later. This VM's administrative sshd holds port 22 on its only IPv4 address. The founder chose to ship on port 2222 first (decision 2026-11-25). Moving to 22 needs only an operator change; see [Deployment](deployment.md).

## Problem

Trying DataTug today means installing a binary and preparing a project and databases. A hosted terminal demo removes all of that, but it also exposes a database client that runs arbitrary SQL to anonymous internet users. The design must keep visitors apart from each other and from the host. It must keep the canonical demo repository unchanged. It must also keep a visitor's GitHub credential inside their own session.

## Architecture decisions

### D1: one child process per SSH session, same executable

The supervisor (`datatug demo ssh-server`) runs no visitor-facing UI. It does three things:

- accepts SSH connections;
- re-executes its own binary as `datatug demo ssh-session` under a real PTY, one child per session;
- copies bytes between the PTY and the SSH channel.

The child runs the unmodified workspace (`runUnifiedApp`) on the session's project copy.

Why: the TUI keeps process-wide state. Settings live in `~/.datatug.yaml`, state in `dtstate`, and the GitHub token in the OS keyring. Chat stores, result caches and package-level test seams resolve from `$HOME` or global variables. Making all of that per-session in one process would mean refactoring dozens of call sites. It would still leave arbitrary SQL with the supervisor's file access. A child process per session isolates that state by construction, crashes stay inside one session, and resource limits can apply to each session.

### D2: one Unix uid per session slot (the security boundary)

Visitors can run arbitrary SQL. For example, SQLite `ATTACH DATABASE '<any path>'` reads or writes any SQLite file the process can open. Application read-only flags are therefore not a boundary. The boundary is the operating system:

- The supervisor allocates one of N slot uids (default base 20000, `--max-sessions` slots) to each session.
- Each session gets `<sessions-dir>/slot-N/<random-id>/`, mode 0700, owned by the slot uid, with `home/` (the child's `$HOME`) and `repo/` (the project copy). The supervisor verifies that the root-owned `slot-N/` parent has the slot GID and mode `0710`.
- The trusted child binary starts as root in `/supervisor`. As its first action, it writes `0` to its slot's `cgroup.procs`, verifies membership, clears supplementary groups, and drops to the slot uid and gid. It verifies the real, effective, and saved IDs on every thread, confirms it cannot regain uid 0, and rechecks cgroup membership. A thread-group seccomp filter denies hard-link creation, AF_UNIX socket and socketpair creation, the three `io_uring` syscalls, System V IPC, POSIX message queues, `listen`, `bind`, `setsockopt(SOL_IP, IP_LOCAL_PORT_RANGE)` and `clone(CLONE_PARENT)` with `EPERM`, and `sendfile`, `splice`, `tee` and `vmsplice` with `EINVAL`, on x86-64 and ARM64. Only root can create terminals and message queues, and a slot is reused only once it has no tasks left. Its descendants inherit the slot and filter.
- The template (canonical clone) is root-owned and world-readable, which is fine because it is public data. The host key is root-owned, mode 0600.
- Self-entry allows Docker's stock seccomp profile. The trade-off is a brief window between fork and slot entry when the trusted child runs as root in `/supervisor`. The supervisor withholds PTY input until the child confirms the privilege drop and the supervisor sees its PID in the slot's `cgroup.procs`. It kills the child and ends the session if entry fails or takes longer than five seconds.
- The supervisor writes `1` to `cgroup.kill`, waits for `populated 0` and reaps the child before cleaning the workspace. It serializes helpers in the dedicated `cleanup/` cgroup. Each helper assumes the slot uid, applies the session rlimits and deletes the directory contents in bounded descriptor-relative batches with a 120-second budget. Directories that would take the walk past a 2 KiB path are first moved to the workspace root, because AppArmor refuses file operations on paths over 8 KiB. Interrupted cleanups leave the slot unavailable for retry. Entry, drain, reap and traversal-boundary failures, and three consecutive cleanup runs without progress, quarantine the slot.

The supervisor runs as container root with `CAP_SETUID`, `CAP_SETGID`, `CAP_CHOWN` and `CAP_KILL`, plus `no-new-privileges` and a read-only root filesystem. Visitor code runs as the unprivileged slot uid. The [deployment design](deployment.md#cgroup-delegation-and-lifecycle) describes the private cgroup mount and host requirements.

For local development, `--isolation=process` runs every child under the supervisor's own uid without requiring a cgroup mount. It requires a loopback listen address or an explicit `--allow-insecure-isolation` override. It logs a warning at start.

### D3: resource limits

The child sets its own rlimits after any required slot entry and before reading visitor input. Limits can only be lowered:

- `RLIMIT_AS` (default 8 GiB of address space. The cgo binary maps about 2 GiB of address space at start, so a lower value crashes it. The child sets `GOMEMLIMIT=256MiB` as a soft Go heap limit and `oom_score_adj` to 900 so the kernel prefers killing a session before the supervisor.)
- `RLIMIT_CPU` (default 900 s)
- `RLIMIT_FSIZE` (64 MiB)
- `RLIMIT_NOFILE` (512)
- `RLIMIT_NPROC` (per uid, 256: Linux counts threads, and a Go process with cgo uses dozens)
- `RLIMIT_CORE` 0

The container adds a memory and CPU cap. The sessions tmpfs caps both disk bytes and inodes across all sessions.

The supervisor enforces:

- The global shell cap (`--max-sessions`) defaults to six.
- The global connection cap (`--max-connections`) defaults to the shell cap plus the queue capacity, with a floor of twice the shell cap. The per-IP cap (`--max-sessions-per-ip`) defaults to two. Both apply at authentication and count active, waiting and pre-shell connections. A shell uses a separate slot after admission. The supervisor closes the SSH transport at exit and releases the connection reservation.
- The SSH handshake timeout defaults to 20 s.
- Each SSH connection can open one session channel. The supervisor allows 30 s from authentication to child startup (`--shell-start-timeout`). Entering the queue suspends that deadline and admission starts a fresh startup deadline.
- Each session channel accepts up to eight environment requests, with values up to 256 bytes. PTY requests can carry a terminal name up to 64 bytes and terminal modes up to 1 KiB.
- The idle timeout defaults to 15 min without client input.
- The maximum session duration starts at admission and defaults to 60 min, with a warning a minute before.
- The supervisor clamps `pty-req` and every later `window-change` above 500×200.
- Each UID slot has a kernel-enforced `memory.max` equal to the sum of `--session-mem-quota` (384 MiB default) and `--session-disk-quota` (160 MiB default). This is a combined cap on memory charged to the slot, including native allocations and tmpfs pages, even for files that stay open after unlinking. The slot has zero swap, `memory.oom.group=1`, and `pids.max=256`.
- The supervisor sends a notice on SSH stderr when `memory.current` approaches 90% of the slot cap. It reports an OOM termination when the slot's `memory.events` records a new `oom_group_kill`. The enforcement boundary is the slot's `memory.max`.
- Each workspace has a private mode-`0700` `tmp/` directory used by `TMPDIR` and `SQLITE_TMPDIR`. The shared sessions tmpfs is sized above the sum of the slot memory caps.
- Startup drains pre-existing slot cgroups even when they have no stale workspace. A slot with a verified drain and cleanup remains unavailable while `memory.current` is at or above 16 MiB or unreadable. At or above the threshold, the supervisor requests `memory.reclaim` and rereads the charge before deciding. It checks pending memory and cleanup slots every 30 seconds.

UID isolation requires a writable cgroup v2 mount rooted at the container's own scope. Before listening, the supervisor verifies `nsdelegate` on the backing mount and that a write of the scope’s current `memory.max` returns `EPERM`. It also verifies that the slot caps, 96 MiB cleanup cap and 512 MiB supervisor reserve fit the finite scope `memory.max`. When `pids.max` is finite, it must cover 256 PIDs per slot plus 32 for cleanup and 256 for the supervisor. The sessions tmpfs must hold the sum of the slot caps plus 10% in bytes and at least one inode per 512 bytes of each slot budget, rounded up per slot, plus five inodes per slot and one for the tmpfs root.

Only `session` channels with `pty-req` and `shell` are served. `exec`, `subsystem` (sftp, scp), port forwarding, agent forwarding and X11 are refused. There is no shell.

When every slot is busy or pending reuse, an interactive visitor can join a FIFO queue (`--max-queue`, default 20). Release or successful cleanup assigns a usable slot to the head of the queue atomically. The supervisor draws the visitor's position and estimated start every second and refreshes positions when someone leaves. Press `q`, Ctrl-C or Ctrl-D to leave. Disconnects and the queue deadline (`--max-queue-wait`, default 30 min) also remove the visitor. Set `--max-queue=0` to reject overflow immediately.

While waiting, each visitor occupies one SSH connection and one channel. The queue adds one input goroutine with a 256-byte read buffer, one screen goroutine and one maximum-wait timer. The existing request loop holds the bounded PTY request and processes window changes. One shared one-second ticker refreshes screens and closes transports whose writes stall. PTY allocation, workspace creation, slot assignment, child startup and relay registration wait until admission.

The estimate sorts the active deadlines and treats each slot awaiting cleanup or memory release as available in 30 s. Quarantined slots are excluded. Each group of positions equal to the number of upcoming releases uses the same sorted times, offset by one full session duration for each earlier group. The screen shows an unknown estimate if there are no upcoming releases. Early departures and cleanup progress can change the estimate. It is an estimate, not a guarantee: cleanup and memory release can take longer than their retry interval, so the screen says "in about" and switches to "any moment now" once the time has passed.

### D4: network egress through an allow-listing proxy

The workspace can open arbitrary HTTP URLs and database hosts, so unrestricted egress would let visitors reach services on the host's bridge gateway. Deployment uses two containers from the same image:

- `edge` (`datatug demo ssh-edge`) sits on the public bridge network and an internal network. It forwards public TCP 2222 to `demo:2222` with a PROXY protocol v2 header, so per-IP limits still see the client address. The supervisor requires explicit trusted peer CIDRs and accepts only a PROXY v2 LOCAL header from loopback health checks. Forwarding writes have a 5 s deadline. After either direction ends, the edge closes both sockets within 500 ms. It also serves an HTTP CONNECT proxy that allows only `host:443` for an allow-list (default `github.com`, `api.github.com`, `codeload.github.com`, `cloud.openvaultdb.com`).
- The edge HTTP proxy listens on a Unix socket in a named volume. The edge owns the directory (UID 10001, GID 0, mode `0750`) and exposes a mode-`0666` socket. The supervisor accesses the directory as root, group 0. Slot UIDs cannot traverse it. The proxy caps accepted connections at 128 by default and bounds HTTP request reads and writes.
- `demo` is on the isolated internal bridge. Children get `HTTPS_PROXY` and `HTTP_PROXY` pointing at an authenticated loopback CONNECT relay. The relay identifies the peer UID via an exact TCP tuple lookup before reading request headers. It caps pending UID lookups at 64 and closes sockets if lookup fails or the cap is full. Each slot UID gets up to 16 sockets, including incomplete requests. The relay rejects other UIDs. It caps authenticated tunnels per session (default eight) and globally (default 64, matching the edge). Session exit revokes the token and closes its tunnels. The supervisor's startup template uses its own temporary relay.

### D5: demo project and template

The canonical project is a GitHub repository given by `--repo` (default `https://github.com/datatug/datatug-demo-project`), project sub-directory `--project-dir` (default `demo-project-1`), and branch `--ref` (default the repository's default branch). At start the supervisor clones it with go-git into `<state>/template` (pure Go, so no hooks or filters run). It then writes the bundled Chinook SQLite to the project's `.demo-data/chinook.sqlite` when absent. Each session's child copies the template, `.git` included, into its own `repo/`. Visitor fork opens run inside the session child. Packfile decoding, including delta expansion before checkout, is charged to that slot's `memory.max`.

The demo shows what DataTug already supports with no remote credentials:

- SQLite catalogs: `chinook`, `countries`, `orders`;
- the inGitDB catalog `geo`, which is OpenVaultDB's local storage engine;
- the project's OpenVaultDB databases on `cloud.openvaultdb.com`, read through the proxy.

No new database engines are added.

### D6: GitHub authentication: OAuth App device flow, `public_repo` scope

DataTug already has an OAuth App (`Ov23liAIKfguW2oYiore`) used with GitHub's device flow (`pkg/auth/ghauth`, the `dtproject` device-auth screen). The demo reuses it, requesting scope `public_repo` instead of `repo`. That is the minimum that can fork a public repository and push to the fork.

A GitHub App was rejected for the MVP:

- user-to-server tokens can fork and push only where the App is installed, so every visitor would first have to install it on their own account;
- it adds a second credential type to operate;
- it gains little, because the session discards the token on disconnect.

The client ID is overridable with `--github-client-id`. No client secret is needed or used.

The token lives only in the child's memory. `ghauth` gets an in-memory store selected by the session child, never the keyring, environment, files or git config. It is passed to go-git as HTTP basic auth per request. It disappears when the child exits. The welcome and save screens explain that access lasts until disconnect and can be revoked under GitHub → Settings → Applications.

### D7: fork persistence on a dedicated branch

Saving works as follows:

1. Identify the user (`GET /user`).
2. Confirm, then fork the canonical repository (`POST /repos/{owner}/{repo}/forks`). GitHub returns the existing fork when the user already has one, even a renamed one. Wait until the fork answers `GET /repos/{fork}` (bounded backoff, default 60 s).
3. Commit every change of the workspace tree, `.gitignore` respected, to branch `datatug-demo` (`--save-branch`). The commit author is the GitHub login with its no-reply address.
4. Push without force. If the fork's `datatug-demo` moved since the workspace's base commit, the push is refused. The visitor is offered a new branch, `datatug-demo-<UTC yyyymmdd-hhmmss>`.
5. Report the repository, branch and commit URL.

The default branch of the fork is never written.

What is saved is exactly the git-tracked tree:

- project configuration;
- queries and other project files;
- SQLite and inGitDB files inside the repository.

The save screen lists the changed paths and says that external databases (OpenVaultDB cloud) are not part of the repository, so changes made there are neither saved nor undone.

### D8: returning visitors

The session starts on a DataTug welcome screen with two choices:

1. Explore the demo project, which is the default and needs no sign-in.
2. Open my GitHub project.

Choice 2 signs in, finds the fork as in D7, and clones the fork's `datatug-demo` branch (or its default branch) into the session's `repo/` with go-git. Fork contents are untrusted. The clone is depth-limited and size-capped, with no submodules. It is rejected when:

- a symlink resolves outside `repo/`;
- the clone exceeds the size cap (default 200 MiB) or file count (default 20 000);
- the project directory or `datatug-project.json` is missing.

DataTug never executes repository-provided programs. go-git runs no hooks, and catalogs only name database files.

### D9: temporary-workspace indicator and save entry point

In a demo session, the workspace notice line says the workspace is temporary and that "Save to GitHub" keeps it. The explorer's module menu gains a "Save to GitHub" module whose page runs the D7 flow inside the existing navigation shell. It reuses the device-auth screen.

## Behavior

### Commands

- `datatug demo ssh-server`: the supervisor. Flags:
  - listening: `--listen` (default `:2222`), `--proxy-protocol`, `--proxy-protocol-trust`;
  - host key and state: `--host-key` (default `<state>/ssh_host_ed25519_key`, generated once when absent, never replaced), `--state-dir`, `--sessions-dir`;
  - demo project: `--repo`, `--ref`, `--project-dir`;
  - session limits: `--max-sessions`, `--max-queue`, `--max-queue-wait`, `--max-connections`, `--max-sessions-per-ip`, `--idle-timeout`, `--max-session-duration`, `--handshake-timeout`;
  - isolation and cgroup limits: `--isolation=uid|process`, `--uid-base`, `--cgroup-root` (default `/sys/fs/cgroup`), `--session-mem-quota`, `--session-disk-quota`, `--cleanup-mem-limit`, `--cleanup-pids-limit`, `--slot-reuse-memory-threshold`, `--allow-insecure-isolation`;
  - GitHub: `--github-client-id`, `--save-branch`;
  - egress: `--egress-proxy`, `--max-relay-sockets-per-uid`, `--max-session-tunnels`, `--max-egress-tunnels`.

  `SIGTERM` stops accepting connections, tells each session it is ending, waits up to 10 s, then kills and cleans up every session.
- `datatug demo ssh-server --print-host-key`: prints the public host key and its SHA256 fingerprint, then exits.
- `datatug demo ssh-health [--addr]`: exits 0 when the server answers with an `SSH-2.0-` banner. It is the container health check.
- `datatug demo ssh-edge`: the TCP forwarder with PROXY v2, plus the allow-listing CONNECT proxy (D4).
- `datatug demo ssh-session`: hidden. It is the per-session child, started only by the supervisor, and `--cleanup <dir>` is its cleanup mode.

### Session lifecycle

After admission, the visitor sees the welcome screen within 2 s on a warm server. On disconnect, network loss, idle timeout, maximum duration, a child crash or server shutdown, the supervisor:

- closes the PTY;
- drains the slot cgroup in UID mode or kills the process group in local process mode;
- verifies child reap and queues cleanup under the slot uid in UID mode;
- removes the session directory;
- makes the UID slot available once the verified drain, reap and cleanup have finished and residual memory is below the reuse threshold.

It logs one structured line per session start and end, with session id, client IP, duration, end reason and exit status, and never a token.

## Acceptance Criteria

### AC: ssh-launches-real-workspace

Connecting with a PTY shows DataTug's own workspace (title bar "DataTug ¦ Project: …"), in colour, full-screen. Keys and mouse work, and resizing re-renders. Disconnecting restores the visitor's terminal.

### AC: no-shell-no-forwarding

`exec`, `subsystem`, port forwarding and agent forwarding requests are refused. A session without a PTY gets a one-line explanation and is closed.

### AC: sessions-isolated

Two concurrent sessions edit their workspaces independently. One session's SQLite `ATTACH` of the other's database file fails with a permission error. Neither can read the host key. The template and canonical repository are unchanged after both sessions write.

### AC: limits-enforced

Connections above `--max-connections` or `--max-sessions-per-ip`, and interactive arrivals when the waiting queue is full or disabled, get a short "demo is full" message and are closed. Queued visitors see their position and estimated start until admission or departure. Idle and maximum-duration timeouts end the session and clean it up. A stalled handshake is dropped after `--handshake-timeout`. The child runs with the D3 rlimits and, in UID mode, the slot cgroup limits.

### AC: cleanup-always

After a normal exit, an abrupt client drop, a child panic, or a supervisor `SIGTERM`, the supervisor drains the session cgroup before cleaning the workspace. It retries interrupted cleanups and only reuses a slot after cleanup finishes and the residual charges fall below the reuse threshold. Entry, drain, reap and traversal-boundary failures, and three consecutive cleanup runs without progress, quarantine the slot.

### AC: device-flow-minimum-scope

The sign-in requests only `public_repo`, shows what it allows before it starts, and can be cancelled. Denied, expired and failed codes are reported, and the visitor stays in the temporary workspace. The token appears in no log, file, environment variable or git config.

### AC: save-to-fork

A change saved after sign-in creates or reuses the fork and creates a commit on `datatug-demo` containing exactly the workspace changes. It reports the commit URL. A diverged branch is never force-pushed: the visitor is offered a new branch.

### AC: reopen-fork

A returning visitor who chooses "Open my GitHub project" gets their saved branch back in a fresh workspace. An incompatible or oversized fork, or one with escaping symlinks, is refused with a reason.

### AC: egress-allow-list

From a session, the CONNECT proxy reaches only allow-listed hosts on port 443. Direct TCP connections to external hosts fail, and the session UID cannot reach the edge socket.

### AC: host-key-persists

Restarting or redeploying the container keeps the same host key fingerprint. `--print-host-key` prints it.

## Open Questions

- Port 22: needs the operator to move administrative sshd or add an address (founder: port 2222 first).
