# SSH demo deployment design

The [feature specification](README.md) defines the SSH and session behavior. The [operator runbook](../../../deploy/ssh-demo/README.md) covers building, routing, key management and troubleshooting.

## Topology

```text
SSH visitor
    |
Host TCP 2222 (published to edge)
    |
    v
edge :2222 ---- SSH + PROXY v2 over internal bridge ----> demo :2222
edge CONNECT socket <-------- Unix socket -------- supervisor relay
    |                                                     ^
    |                                                             |
    +--> Internet :443 via public bridge                   Same-binary PTY children
```

The edge joins the public and internal bridges. It forwards SSH with a PROXY v2 header so that the supervisor can enforce per-client-IP quotas. Its CONNECT proxy allows TCP 443 for exactly `github.com`, `api.github.com`, `codeload.github.com` and `cloud.openvaultdb.com`. The proxy rejects IP literals and unsafe resolved addresses. Only SSH is published on the host.

The supervisor and session children attach only to the internal bridge. The supervisor trusts PROXY headers from `172.31.250.2/32` (edge). The demo service uses `172.31.250.3`. Child HTTPS clients use an authenticated loopback CONNECT relay. Before HTTP reads, the relay resolves the client socket's UID through an exact TCP tuple request to Linux `NETLINK_SOCK_DIAG`. It caps unidentified sockets and concurrent lookups at 64 and closes connections on lookup error or overflow. It admits up to 16 sockets per slot UID, including incomplete requests. Each session also gets up to eight concurrent CONNECT tunnels. The template uses a temporary supervisor-only relay.

The relay reaches edge CONNECT over `/run/datatug-demo/proxy/connect.sock` in a named volume. The edge owns the directory as UID 10001 and GID 0 with mode `0750`. Before binding the socket, the edge verifies the parent is a real directory with no permissions for others. The socket has mode `0666`, and directory traversal restricts access to the edge and the supervisor. The supervisor mounts the volume read-only. The internal bridge uses Docker's isolated gateway mode so the bridge gives the host no address and the demo has no direct internet route. Both services use the same image and run different DataTug subcommands.

## Process isolation

Both services drop all capabilities and enable `no-new-privileges` and a read-only root filesystem. The edge runs as uid/gid 10001 with zero capabilities. The supervisor starts as container root to implement D2 and receives four capabilities.

| Capability | Use |
| --- | --- |
| `SETUID` | Start each session and its cleanup process under the allocated slot uid. |
| `SETGID` | Assign the slot gid and clear supplementary groups. |
| `CHOWN` | Assign the workspace and PTY slave to the slot identity. |
| `KILL` | Signal the session leader for graceful termination before draining its cgroup. |

The base image's setuid and setgid bits are stripped. A session runs with an unprivileged uid and gid in its own mode-`0700` directory. The Linux uid boundary protects other sessions and the host key. World-readable image files and the public template are visible to sessions.

## Storage and startup

- The `state` volume at `/var/lib/datatug-demo/state` holds the mode-`0600` Ed25519 host key and the public project template. It survives restarts and container replacement. The supervisor shallow-clones the canonical project on each start, installs the bundled fixture when needed, invokes `datatug demo ssh-session --prepare-template`, then replaces the template after preparation succeeds.
- The root-owned, mode-`0711` sessions tmpfs at `/var/lib/datatug-demo/sessions` holds per-visitor homes and clones. It defaults to 3,591 MiB and 3,342,336 inodes. Size it at the sum of the slot `memory.max` values plus 10%, rounded up. For the inode limit divide each slot cap by 1 KiB, round up, and sum the results. Scale both limits with `DEMO_MAX_SESSIONS`. Stopping the container discards the tmpfs.
- A separate 64 MiB tmpfs supplies `/tmp` with mode `0755`. Both services set `ipc: none` to omit the Docker `/dev/shm` mount. UID-mode startup rejects either path when it is writable by others. Sessions use a private `TMPDIR` inside their workspace.

## Cgroup delegation and lifecycle

The host needs Docker Engine with `writable-cgroups=true` support and cgroup v2 with `nsdelegate`. It also needs Linux support for `cgroup.kill` and the `oom_group_kill` memory event. The demo uses Docker's stock seccomp profile. The runtime must place exec and health-check processes in the container init's cgroup. runc 1.3.4 does this. See the [operator runbook](../../../deploy/ssh-demo/README.md#host-requirements) for setup.

The demo's private cgroup namespace and writable `/sys/fs/cgroup` mount expose only its own Docker scope. Docker supplies the scope and mount without custom host setup. The supervisor moves scope processes into `supervisor/`, enables the memory and PID controllers, and configures one `slot-N/` leaf per slot and one `cleanup/` leaf. The trusted child starts as root in `supervisor/`, enters its slot, drops to the slot uid and gid, and installs a thread-group seccomp filter for `link` and `linkat` before handling visitor input. Its descendants inherit the slot and filter. The supervisor gives the child five seconds to complete entry and verifies the child's PID in the slot before forwarding input.

At startup and session exit the supervisor writes `cgroup.kill` and verifies `populated 0` before workspace cleanup. On restart it drains retired slot cgroups and cleans workspaces owned by their former UIDs. A stale workspace below the configured UID base stops startup. Cleanup re-execs the trusted binary, enters the `cleanup/` cgroup and assumes the slot UID and GID before walking the workspace. It applies session rlimits, uses a 32 MiB Go heap target, and deletes in bounded batches without following symlinks. Only one helper runs at a time. Sessions awaiting cleanup retain their reservations.

After a verified drain, child reap and cleanup, the supervisor releases the reservation for reuse only when `memory.current` is below 16 MiB. It checks pending memory charges every 30 seconds and logs quarantined capacity. Drain, reap and cleanup failures quarantine the slot until supervisor restart. Keeping slot cgroups in place preserves the accounting for retained charges.

## Resource budgets

The supervisor defaults to six shells, twelve concurrent authenticated SSH connections, two connections per client IP, a 20-second handshake timeout, a 15-minute idle timeout and a 60-minute session deadline. Each session gets a hard 544 MiB combined memory and tmpfs cap (`memory.max`), zero swap and 256 processes or threads. It also sets rlimits for address space, CPU time, file size, descriptors, per-UID process count and core dumps. The Go heap target is 256 MiB and the address-space ceiling is 8 GiB. The supervisor sends a notice near the hard cap and a termination message when the kernel records an OOM group kill.

Compose defaults to a 3,872 MiB scope limit, 3,591 MiB sessions tmpfs, two CPUs and 2,048 PIDs. Startup requires a finite scope memory limit covering all slot caps plus a 96 MiB cleanup limit and a 512 MiB supervisor reserve. A finite `pids.max` must cover 256 PIDs per slot plus 32 for cleanup and 256 for the supervisor. The cleanup cgroup also has zero swap and group OOM kill enabled. Startup checks the sessions filesystem's block and inode totals with `statfs`. The edge gets 256 MiB and half a CPU. A container init reaps orphans and Compose allows 20 seconds to stop. Both services use restart policies, SSH banner health checks and rotated JSON logs.

The sessions tmpfs sizing accounts for per-slot page and inode metadata charges. CPU capacity, loopback accept and lookup work, global PID capacity, proxy capacity, and the kernel remain shared. The supervisor, template preparation, and container-level infrastructure consume the reserve and can still encounter an outer OOM if their usage exceeds it.
