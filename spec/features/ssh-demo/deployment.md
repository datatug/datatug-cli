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
edge :3128 <--------- HTTP CONNECT from demo and sessions ---------+
    |                                                             |
    +--> Internet :443 via public bridge                   Same-binary PTY children
```

The edge joins the public and internal bridges. It forwards SSH with a PROXY v2 header so that the supervisor can enforce per-client-IP quotas. Its CONNECT proxy allows TCP 443 for exactly `github.com`, `api.github.com`, `codeload.github.com` and `cloud.openvaultdb.com`. The proxy rejects IP literals and unsafe resolved addresses. Only SSH is published on the host.

The supervisor and session children attach only to the internal bridge. HTTPS clients use the edge proxy and the supervisor passes proxy settings in each child's clean environment. Keeping public ingress and egress on the edge gives the supervisor's network namespace no direct route to the internet. Both services use the same image and run different DataTug subcommands.

## Process isolation

Both services drop all capabilities and enable `no-new-privileges` and a read-only root filesystem. The edge runs as uid/gid 10001 with zero capabilities. The supervisor starts as container root to implement D2 and receives four capabilities.

| Capability | Use |
| --- | --- |
| `SETUID` | Start each session and its cleanup process under the allocated slot uid. |
| `SETGID` | Assign the slot gid and clear supplementary groups. |
| `CHOWN` | Assign the workspace and PTY slave to the slot identity. |
| `KILL` | Terminate process groups and stray processes belonging to a slot uid before reusing it. |

The base image's setuid and setgid bits are stripped. A session runs with an unprivileged uid and gid in its own mode-`0700` directory. The Linux uid boundary protects other sessions and the host key. World-readable image files and the public template are visible to sessions.

## Storage and startup

- The `state` volume at `/var/lib/datatug-demo/state` holds the mode-`0600` Ed25519 host key and the public project template. It survives restarts and container replacement. The supervisor shallow-clones the canonical project on each start, installs the bundled fixture when needed, invokes `datatug demo ssh-session --prepare-template`, then replaces the template after preparation succeeds.
- The root-owned, mode-`0711` sessions tmpfs at `/var/lib/datatug-demo/sessions` holds per-visitor homes and clones. It defaults to 2 GiB across all sessions. The supervisor releases a slot after cleanup, and stopping the container also discards the tmpfs.
- A separate 64 MiB tmpfs supplies `/tmp`. Build credentials are mounted as a BuildKit secret and have no runtime mount.

## Resource budgets and lifecycle

The supervisor defaults to 16 sessions total, three per client IP, a 20-second handshake timeout, a 15-minute idle timeout and a 60-minute session deadline. Session processes inherit the cgroup budget and set their own rlimits for address space, CPU time, file size, descriptors, per-uid process count and core dumps. The merged cgo implementation uses an 8 GiB virtual address limit and a 768 MiB Go memory target to allow for startup address reservations and resident memory use. Individual sessions share the container CPU quota.

Compose defaults to a 3 GiB memory cap, two CPUs and 2,048 PIDs for the supervisor and its children. The edge gets 256 MiB and half a CPU. A container init reaps orphaned processes in the supervisor container and Compose gives it 20 seconds to stop. Both services use restart policies, SSH banner health checks and rotated JSON logs. The edge probe reaches the supervisor through PROXY v2. The supervisor's probe connects over loopback.
