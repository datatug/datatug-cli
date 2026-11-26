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
- Each session gets `<sessions-dir>/<random-id>/`, mode 0700, owned by the slot uid, with `home/` (the child's `$HOME`) and `repo/` (the project copy).
- The child runs with `SysProcAttr.Credential` set to the slot uid/gid and no supplementary groups.
- The template (canonical clone) is root-owned and world-readable, which is fine because it is public data. The host key is root-owned, mode 0600.
- Cleanup runs as the slot uid (`datatug demo ssh-session --cleanup <dir>`) and empties the directory. The supervisor kills the session's process group (`kill(-pgid, SIGKILL)`), then removes the empty directory. A slot uid is reused only after its directory is gone and no process of that uid remains.

The supervisor therefore runs as container root with only `CAP_SETUID`, `CAP_SETGID`, `CAP_CHOWN` and `CAP_KILL`, plus `no-new-privileges` and a read-only root filesystem. Visitor code never runs as root. This deliberately departs from "run as non-root". A non-root supervisor cannot give sessions separate uids: Docker does not grant ambient capabilities to a non-root user, and `no-new-privileges` ignores file capabilities. Per-session uids are a stronger boundary than one shared non-root uid. Operators who can should also enable Docker `userns-remap`, so container root is not host root.

Without root (local development), `--isolation=process` runs every child under the supervisor's own uid. It is refused unless the listen address is loopback or `--allow-insecure-isolation` is passed. It logs a warning at start.

### D3: resource limits

The child sets its own rlimits as its first action, before reading any visitor input. Limits can only be lowered:

- `RLIMIT_AS` (default 8 GiB: address space, not memory. The cgo binary maps about 2 GiB of address space at start, so a lower value crashes it. Real memory is bounded by `GOMEMLIMIT=768MiB` in the child's environment and by the container memory cap. The child sets its `oom_score_adj` to 900, so the kernel kills a session before the supervisor.)
- `RLIMIT_CPU` (default 900 s)
- `RLIMIT_FSIZE` (64 MiB)
- `RLIMIT_NOFILE` (512)
- `RLIMIT_NPROC` (per uid, 256: Linux counts threads, and a Go process with cgo uses dozens)
- `RLIMIT_CORE` 0

The container adds a memory and CPU cap. The sessions directory is a size-capped tmpfs, which bounds disk use for all sessions together.

The supervisor enforces:

- maximum concurrent sessions (default 16);
- maximum sessions per client IP (default 3);
- SSH handshake timeout (default 20 s);
- idle timeout (no input, default 15 min);
- maximum session duration (default 60 min, with a warning a minute before);
- a PTY size cap: `pty-req` and every later `window-change` above 500×200 are clamped.

Only `session` channels with `pty-req` and `shell` are served. `exec`, `subsystem` (sftp, scp), port forwarding, agent forwarding and X11 are refused. There is no shell.

### D4: network egress through an allow-listing proxy

The workspace can open arbitrary HTTP URLs and database hosts, so unrestricted egress would let visitors reach services on the host's bridge gateway. Deployment uses two containers from the same image:

- `edge` (`datatug demo ssh-edge`) sits on the public bridge network and an internal network. It forwards public TCP 2222 to `demo:2222` with a PROXY protocol v2 header, so per-IP limits still see the client address. It also serves an HTTP CONNECT proxy that allows only `host:443` for an allow-list (default `github.com`, `api.github.com`, `codeload.github.com`, `cloud.openvaultdb.com`).
- `demo` (`datatug demo ssh-server`) is only on the internal network (`internal: true`, no route out). Children get `HTTPS_PROXY`/`HTTP_PROXY` pointing at `edge`. A raw TCP dial (for example a PostgreSQL connection the visitor types) has no route and fails.

This needs no host root and no host firewall changes.

### D5: demo project and template

The canonical project is a GitHub repository given by `--repo` (default `https://github.com/datatug/datatug-demo-project`), project sub-directory `--project-dir` (default `demo-project-1`), and branch `--ref` (default the repository's default branch). At start the supervisor clones it with go-git into `<state>/template` (pure Go, so no hooks or filters run). It then writes the bundled Chinook SQLite to the project's `.demo-data/chinook.sqlite` when absent. Each session's child copies the template, `.git` included, into its own `repo/`.

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
  - listening: `--listen` (default `:2222`), `--proxy-protocol`;
  - host key and state: `--host-key` (default `<state>/ssh_host_ed25519_key`, generated once when absent, never replaced), `--state-dir`, `--sessions-dir`;
  - demo project: `--repo`, `--ref`, `--project-dir`;
  - session limits: `--max-sessions`, `--max-sessions-per-ip`, `--idle-timeout`, `--max-session-duration`, `--handshake-timeout`;
  - isolation: `--isolation=uid|process`, `--uid-base`, `--allow-insecure-isolation`;
  - GitHub: `--github-client-id`, `--save-branch`;
  - egress: `--egress-proxy`.

  `SIGTERM` stops accepting connections, tells each session it is ending, waits up to 10 s, then kills and cleans up every session.
- `datatug demo ssh-server --print-host-key`: prints the public host key and its SHA256 fingerprint, then exits.
- `datatug demo ssh-health [--addr]`: exits 0 when the server answers with an `SSH-2.0-` banner. It is the container health check.
- `datatug demo ssh-edge`: the TCP forwarder with PROXY v2, plus the allow-listing CONNECT proxy (D4).
- `datatug demo ssh-session`: hidden. It is the per-session child, started only by the supervisor, and `--cleanup <dir>` is its cleanup mode.

### Session lifecycle

On connect, the visitor sees the welcome screen within 2 s on a warm server. On disconnect, network loss, idle timeout, maximum duration, a child crash or server shutdown, the supervisor:

- closes the PTY;
- kills the process group;
- runs cleanup as the slot uid;
- removes the session directory;
- frees the slot.

It logs one structured line per session start and end, with session id, client IP, duration, end reason and exit status, and never a token.

## Acceptance Criteria

### AC: ssh-launches-real-workspace

Connecting with a PTY shows DataTug's own workspace (title bar "DataTug ¦ Project: …"), in colour, full-screen. Keys and mouse work, and resizing re-renders. Disconnecting restores the visitor's terminal.

### AC: no-shell-no-forwarding

`exec`, `subsystem`, port forwarding and agent forwarding requests are refused. A session without a PTY gets a one-line explanation and is closed.

### AC: sessions-isolated

Two concurrent sessions edit their workspaces independently. One session's SQLite `ATTACH` of the other's database file fails with a permission error. Neither can read the host key. The template and canonical repository are unchanged after both sessions write.

### AC: limits-enforced

Connections beyond `--max-sessions` or `--max-sessions-per-ip` get a short "demo is full" message and are closed. Idle and maximum-duration timeouts end the session and clean it up. A stalled handshake is dropped after `--handshake-timeout`. The child runs with the D3 rlimits.

### AC: cleanup-always

After a normal exit, an abrupt client drop, a child panic, or a supervisor `SIGTERM`, no session directory, slot-uid process or temporary file remains.

### AC: device-flow-minimum-scope

The sign-in requests only `public_repo`, shows what it allows before it starts, and can be cancelled. Denied, expired and failed codes are reported, and the visitor stays in the temporary workspace. The token appears in no log, file, environment variable or git config.

### AC: save-to-fork

A change saved after sign-in creates or reuses the fork and creates a commit on `datatug-demo` containing exactly the workspace changes. It reports the commit URL. A diverged branch is never force-pushed: the visitor is offered a new branch.

### AC: reopen-fork

A returning visitor who chooses "Open my GitHub project" gets their saved branch back in a fresh workspace. An incompatible or oversized fork, or one with escaping symlinks, is refused with a reason.

### AC: egress-allow-list

From a session, the CONNECT proxy reaches only allow-listed hosts on port 443. Direct TCP connections to other hosts fail.

### AC: host-key-persists

Restarting or redeploying the container keeps the same host key fingerprint. `--print-host-key` prints it.

## Open Questions

- Port 22: needs the operator to move administrative sshd or add an address (founder: port 2222 first).
