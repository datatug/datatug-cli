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

The supervisor and session children attach only to the internal bridge. The supervisor trusts PROXY headers from `172.31.250.2/32` (edge). The demo service uses `172.31.250.3`. Child HTTPS clients use an authenticated loopback CONNECT relay. Before HTTP reads, the relay resolves the connecting socket's UID from `/proc/net/tcp` or `/proc/net/tcp6` and admits up to 16 sockets per slot UID, including incomplete requests. Each session also gets up to eight concurrent CONNECT tunnels. The template uses a temporary supervisor-only relay.

The relay reaches edge CONNECT over `/run/datatug-demo/proxy/connect.sock` in a named volume. The edge owns the directory as UID 10001 and GID 0 with mode `0750`. The socket has mode `0666`, and directory traversal restricts access to the edge and the supervisor. The supervisor mounts the volume read-only. This transport removes the need for a shared proxy credential. The internal bridge uses Docker's isolated gateway mode (Engine 28+) so the bridge gives the host no address and the demo has no direct internet route. Both services use the same image and run different DataTug subcommands.

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
- The root-owned, mode-`0711` sessions tmpfs at `/var/lib/datatug-demo/sessions` holds per-visitor homes and clones. It defaults to 3,591 MiB and 6,684,672 inodes. Size it at the sum of the slot `memory.max` values plus 10%, rounded up, with at least one inode per 512 bytes of that sum. Change both mount limits when changing `DEMO_MAX_SESSIONS`. The supervisor releases a slot after cleanup, and stopping the container also discards the tmpfs.
- A separate 64 MiB tmpfs supplies `/tmp`. It is root-only (mode 0755), so nothing a visitor writes outlives the session: each session has a private `TMPDIR` inside its workspace. The build needs no credential.

## Cgroup delegation and lifecycle

The host needs cgroup v2 with `nsdelegate`, the Docker systemd cgroup driver, and the private container cgroup namespace. It also needs Linux support for `cgroup.kill` and the `oom_group_kill` memory event. The demo uses Docker's stock seccomp profile. The runtime must place exec and health-check processes in the container init's cgroup. runc 1.3.4 does this. See the [operator runbook](../../../deploy/ssh-demo/README.md#host-requirements) for setup.

Docker creates the dedicated `datatugdemo.slice` on first start. Compose bind-mounts only `/sys/fs/cgroup/datatugdemo.slice` at `/run/datatug-demo/cgroup`. The supervisor finds its own Docker scope by its namespace PID, moves scope processes into `supervisor/`, enables the memory and PID controllers, and configures one `slot-N/` leaf per slot. The trusted child starts as root in `supervisor/`, moves itself into its slot, and drops to the slot uid and gid before handling visitor input. Its descendants inherit the slot. The supervisor gives the child five seconds to complete entry and verifies the child's PID in the slot before forwarding input. At startup and session exit the supervisor uses `cgroup.kill` and waits for `populated 0` before cleaning the workspace and releasing the slot. An unverified slot remains quarantined.

A compromised supervisor controls cgroups and processes inside its own scope. With the specified `nsdelegate` setup and capability set, the observed effect outside the scope was limited to empty sibling cgroups under the dedicated slice. A compromised supervisor could create or modify those empty cgroups. It could neither move a process outside its scope nor change its scope's own `memory.max`. Keep the slice exclusive to the deployment.

## Resource budgets

The supervisor defaults to six shells, twelve concurrent authenticated SSH connections, two connections per client IP, a 20-second handshake timeout, a 15-minute idle timeout and a 60-minute session deadline. Each session gets a hard 544 MiB combined memory and tmpfs cap (`memory.max`), zero swap and 256 processes or threads. It also sets rlimits for address space, CPU time, file size, descriptors, per-UID process count and core dumps. The Go heap target is 256 MiB and the address-space ceiling is 8 GiB. The supervisor sends a notice near the hard cap and a termination message when the kernel records an OOM group kill.

Compose defaults to a 3,776 MiB scope limit, 3,591 MiB sessions tmpfs, two CPUs and 2,048 PIDs. Startup requires a finite scope memory limit covering all slot caps plus 512 MiB for the supervisor and cleanup. It reads the sessions filesystem's block and inode totals with `statfs` and refuses startup if either is below the sizing formula. The edge gets 256 MiB and half a CPU. A container init reaps orphans and Compose allows 20 seconds to stop. Both services use restart policies, SSH banner health checks and rotated JSON logs.

The sessions tmpfs sizing accounts for per-slot page and inode metadata charges. The separate `/dev/shm` mount still has its own shared capacity. CPU capacity, loopback accept and lookup work, global PID capacity, proxy capacity, and the kernel remain shared. The supervisor, template preparation, and container-level infrastructure consume the reserve and can still encounter an outer OOM if their usage exceeds it. A kernel or container-runtime vulnerability can defeat the containment model.
