# Operate the DataTug SSH demo

Run the commands from the repository root on a Linux host with Docker Engine, BuildKit, and Docker Compose v2. See the [deployment design](../../spec/features/ssh-demo/deployment.md) and [visitor guide](../../docs/ssh-demo.md).

## Host requirements

Use rootful Docker Engine 28 or newer with the systemd cgroup driver, cgroup v2 mounted with `nsdelegate`, and a private demo cgroup namespace. The kernel must support `clone3(CLONE_INTO_CGROUP)`, `cgroup.kill` and the `oom_group_kill` memory event. Use a runtime that places exec and health-check processes in the container init's cgroup if the top-level scope cannot accept processes.

Docker creates `datatugdemo.slice` on first start. The slice belongs to this deployment. Keep other workloads out of it. Compose bind-mounts `/sys/fs/cgroup/datatugdemo.slice` at `/run/datatug-demo/cgroup` so the supervisor can create its session slots below its own scope.

```bash
docker info --format '{{.CgroupDriver}} cgroup v{{.CgroupVersion}}'
findmnt -no FSTYPE,OPTIONS /sys/fs/cgroup
```

The stock Docker seccomp profile blocks `clone3` for this capability set. Install a demo profile based on the default profile shipped by the host's Docker Engine. Permit Go's 88-byte `clone3` argument structure and retain `ENOSYS` for other sizes so callers can fall back. Keep the other syscall rules from the base profile. Seccomp can check the structure size and cannot inspect the flags behind the pointer. Enforce a host policy that denies unprivileged user namespace creation for the demo, such as `kernel.unprivileged_userns_clone=0` on hosts that provide that sysctl. See the [Docker seccomp documentation](https://docs.docker.com/engine/security/seccomp/) and the [Moby 28.5.2 default profile](https://github.com/moby/moby/blob/v28.5.2/vendor/github.com/moby/profiles/seccomp/default.json) for an example input.

With the matching base profile saved as `docker-default-seccomp.json`, generate and install the demo profile on the machine running Compose.

```bash
jq '.syscalls |= (
  map(.names -= ["clone3"]) | map(select(.names | length > 0))
  + [{"names":["clone3"],"action":"SCMP_ACT_ALLOW",
      "args":[{"index":1,"value":88,"op":"SCMP_CMP_EQ"}]},
     {"names":["clone3"],"action":"SCMP_ACT_ERRNO","errnoRet":38,
      "args":[{"index":1,"value":88,"op":"SCMP_CMP_NE"}]}]
)' docker-default-seccomp.json > datatug-demo-seccomp.json &&
sudo install -o root -g root -m 0644 datatug-demo-seccomp.json /etc/docker/datatug-demo-seccomp.json
```

The supervisor probes atomic placement before serving. Keep the default AppArmor confinement and the capabilities specified by Compose.

## Build, start, and stop

The build needs no credential: every dependency is a public Go module.

```bash
export DEMO_BUILD_REVISION="$(git rev-parse HEAD)"
export DEMO_BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker compose -f deploy/ssh-demo/compose.yaml build
docker compose -f deploy/ssh-demo/compose.yaml up -d --wait
docker compose -f deploy/ssh-demo/compose.yaml ps
```

Allow inbound TCP 2222 at the provider firewall if one is enabled. Review logs or stop the services with these commands.

```bash
docker compose -f deploy/ssh-demo/compose.yaml logs --tail 100 -f edge demo
docker compose -f deploy/ssh-demo/compose.yaml stop
```

To upgrade, check out the desired revision, rebuild the image, then run `up -d --wait --force-recreate`. Recreate the containers when changing slot UID assignments so processes from the previous configuration cannot survive. The host key survives in the `state` volume. Keep the same Compose project name and volume when upgrading or moving the checkout.

After pushing demo data changes to the canonical repository, restart the supervisor. It clones and prepares a new template on startup. A restart ends active sessions.

```bash
docker compose -f deploy/ssh-demo/compose.yaml restart demo
docker compose -f deploy/ssh-demo/compose.yaml up -d --wait
```

## Address and host key

The founder chose direct IP access on 2026-11-26. Until DNS is configured, use:

```bash
ssh -p 2222 178.104.41.143
```

When the founder adds DNS, set `demo.datatug.app` to a DNS-only (grey-cloud) A record for `178.104.41.143`. Add AAAA `2a01:4f8:1c1b:e43f::1` only after publishing and verifying IPv6 access. The hostname will then use the same host key and server configuration via `ssh -p 2222 demo.datatug.app`. This deployment requires zero Caddy changes or web redirects.

Print the public key and SHA256 fingerprint from the deployment volume. While the stack runs:

```bash
docker compose -f deploy/ssh-demo/compose.yaml exec -T demo \
  /usr/local/bin/datatug demo ssh-server --print-host-key --state-dir /var/lib/datatug-demo/state
```

With the stack stopped, `run --rm --no-deps -T demo demo ssh-server --print-host-key --state-dir /var/lib/datatug-demo/state`
does the same. (`run` fails while the demo runs: the demo has a fixed address on the internal network.)

Publish the fingerprint on `datatug.app` so visitors can compare it with the ED25519 fingerprint in their first SSH prompt before accepting the key. Once DNS exists, consider an SSHFP record on a DNSSEC-signed zone. To generate the SHA256 record, put the first line of the output in `demo-host-ed25519.pub`:

```bash
ssh-keygen -r demo.datatug.app -f demo-host-ed25519.pub | awk '/ SSHFP 4 2 /'
```

Back up the **private** host key while the service container exists. Store this copy in protected, encrypted backup storage. Keep mode `0600` and root ownership inside the volume if you restore it. Restore before starting a replacement supervisor so it uses the existing identity.

```bash
umask 077
backup_dir=$(mktemp -d "$HOME/datatug-demo-key.XXXXXXXX")
demo_container=$(docker compose -f deploy/ssh-demo/compose.yaml ps -q --all demo)
test -n "$demo_container" &&
  docker cp "$demo_container:/var/lib/datatug-demo/state/ssh_host_ed25519_key" "$backup_dir/"
```

## Port 22

This host's administrative sshd owns TCP 22. The current demo endpoint stays on TCP 2222. To offer the demo on port 22, choose one of these options.

1. Keep the existing port assignment. Once DNS exists, visitors use `ssh -p 2222 demo.datatug.app`.
2. Move administrative SSH to TCP 22022. **Keep your current admin session open and have provider console access. An incorrect sshd or firewall change can lock you out.**

   First allow inbound TCP 22022 for your admin source addresses in the provider firewall. If UFW is active, allow it there as well, using the same source restrictions as the current admin rule.

   ```bash
   sudo ufw status
   # Replace the example address with your administration address or CIDR.
   sudo ufw allow from 192.0.2.10 to any port 22022 proto tcp
   ```

   In `/etc/ssh/sshd_config` or an included drop-in, retain TCP 22 and add TCP 22022. Update any active `ListenAddress` directives that pin a port to allow the new port as well.

   ```text
   Port 22
   Port 22022
   ```

   Validate the config and inspect the effective ports and addresses. On Debian or Ubuntu with service-managed SSH, reload the service. If `ssh.socket` is active, use the socket activation commands instead.

   ```bash
   if sudo sshd -t; then
     sudo sshd -T | awk '/^(port|listenaddress) /'
     if systemctl is-active --quiet ssh.socket; then
       sudo systemctl daemon-reload && sudo systemctl restart ssh.socket ssh.service
     else
       sudo systemctl reload ssh.service
     fi
     sudo ss -lntp '( sport = :22 or sport = :22022 )'
   fi
   ```

   From a second terminal, verify that `ssh -p 22022 -l "$USER" 178.104.41.143` reaches your admin account. Set the login explicitly if it differs from your local username. Only after that succeeds, remove `Port 22` and any explicit `:22` listeners from the admin config. Repeat validation and reload, then test another admin login through 22022. Update provider and host rules for TCP 22 to match the intended public demo access. With TCP 22 free, publish the demo there.

   ```bash
   DEMO_SSH_PUBLISH=22 docker compose -f deploy/ssh-demo/compose.yaml up -d --wait
   ```

3. Add a second public IPv4 address. Restrict administrative sshd to the first address with `Port 22` and `ListenAddress 178.104.41.143`. Remove wildcard listeners, validate and reload as above, and verify admin access in a second session. Bind the demo to TCP 22 on the second address and allow inbound TCP 22 for that address at the provider firewall. The Compose setting `DEMO_SSH_PUBLISH=<second-ip>:22` expands to `<second-ip>:22:2222`. If DNS is enabled, point the demo record at the second address.

## Configuration

Set these in the environment or in the Compose environment file. Re-run `up -d --wait` for changed container settings.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DEMO_BUILD_VERSION` | `dev` | Version stamped into the CLI and image. |
| `DEMO_BUILD_REVISION` | `unknown` | Source commit stamped into the CLI and image. |
| `DEMO_BUILD_DATE` | Empty | UTC build timestamp stamped into the CLI. |
| `DEMO_IMAGE` | `datatug-ssh-demo:local` | Local image tag shared by both services. |
| `DEMO_SSH_PUBLISH` | `2222` | Host port or `IP:port` published to the edge's SSH listener. |
| `DEMO_REPO` | `https://github.com/datatug/datatug-demo-project` | Canonical public demo repository. |
| `DEMO_REF` | Repository default branch | Git branch or full ref cloned at startup. |
| `DEMO_PROJECT_DIR` | `demo-project-1` | Relative path to the project in that repository. |
| `DEMO_GITHUB_CLIENT_ID` | Built-in DataTug OAuth App | Optional device-flow client ID. |
| `DEMO_SAVE_BRANCH` | `datatug-demo` | Branch in the visitor's fork used by save and reopen. |
| `DEMO_MAX_SESSIONS` | `6` | Total live session slots. |
| `DEMO_MAX_SESSIONS_PER_IP` | `2` | Concurrent authenticated SSH connections per client IP. |
| `DEMO_IDLE_TIMEOUT` | `15m` | How long a session can go without client input. |
| `DEMO_MAX_SESSION_DURATION` | `60m` | Maximum time per session. |
| `DEMO_HANDSHAKE_TIMEOUT` | `20s` | Maximum time for SSH handshake. |
| `DEMO_UID_BASE` | `20000` | First uid and gid in the session slot range. |
| `DEMO_SESSIONS_TMPFS` | `3591m` | Shared sessions tmpfs size. |
| `DEMO_SESSIONS_TMPFS_INODES` | `6684672` | Shared sessions tmpfs inode count. |
| `DEMO_MAX_EGRESS_TUNNELS` | `64` | Global CONNECT tunnel cap at the relay and edge. |
| `DEMO_SECCOMP_PROFILE` | `/etc/docker/datatug-demo-seccomp.json` | Demo seccomp profile on the machine running Compose. |
| `DEMO_MEM_LIMIT` | `3776m` | Demo memory cap, including tmpfs usage. |
| `DEMO_CPUS` | `2` | Demo cgroup CPU quota in cores. |
| `DEMO_PIDS_LIMIT` | `2048` | Demo cgroup limit for processes and threads. |

## Size and troubleshoot

- **Capacity.** Each slot defaults to a combined 544 MiB cgroup cap for application memory and charged tmpfs and 256 PIDs (including threads). Budget all slot caps plus the 512 MiB supervisor reserve in `DEMO_MEM_LIMIT`. The child also has a 256 MiB Go heap target and an 8 GiB virtual address ceiling. Set the sessions tmpfs byte limit to the sum of the slot caps plus 10%, rounded up to a mountable size. Set the inode limit to at least one inode per 512 bytes of the sum of the slot caps. The supervisor checks both limits at startup.
- **Healthcheck fails.** Inspect `docker compose -f deploy/ssh-demo/compose.yaml ps` and both service logs. The edge health probe traverses the TCP forwarder, so startup or template failures in the supervisor can affect both probes. Probe the demo directly with `docker compose -f deploy/ssh-demo/compose.yaml exec -T demo /usr/local/bin/datatug demo ssh-health --addr 127.0.0.1:2222 --proxy-protocol`.
- **“Demo is full.”** Check global and per-IP slots, including visitors behind the same NAT. Inspect session end events for `end_reason=full` and check for slots retained after cleanup errors. Measure memory, tmpfs, CPU and PID headroom before raising the caps.
- **Template clone fails.** Check the HTTPS repo URL, ref and project manifest path. Ensure the edge can resolve the GitHub hosts and open TCP 443. Check the edge log's `host` and `result` for CONNECT denials, and confirm the edge has created `/run/datatug-demo/proxy/connect.sock` in the shared volume.
- **UID isolation refuses to start.** The supervisor needs container uid 0, its four capabilities, and a root-owned sessions tmpfs with mode `0711`. Check the delegated cgroup mount, finite scope `memory.max`, and the seccomp profile. Visit the startup logs for the operation that failed.
- **Session logs.** Match the JSON `session start` and `session end` events by `session_id` and inspect `client_ip`, `ssh_user`, `pty_width`, `pty_height`, `duration_ms`, `end_reason`, and `exit_status`. Edge egress logs report the hostname and outcome.

## Isolation

The supervisor runs as container root with `SETUID`, `SETGID`, `CHOWN` and `KILL`. It assigns each visitor a different uid and gid and a mode-`0700` workspace. Both services use read-only root filesystems and `no-new-privileges`. Visitors can read world-readable image files. Validate cgroup delegation and socket-volume ownership before adding Docker user namespace remapping.

CPU fairness is enforced per container plus a per-child `RLIMIT_CPU`. Each session has its own memory and PID cgroup and individual rlimits. CPU, the kernel, and parts of the network stack remain shared. The sessions tmpfs is sized from the combined hard slot caps and the separate `/dev/shm` mount still has its own shared limit. Direct egress from the supervisor network is blocked and HTTPS egress uses the edge allow-list. DataTug forgets GitHub tokens at disconnect. Visitors can revoke their OAuth grant in GitHub Settings under Applications.

## Integration check

Verify SSH, the supervisor's Unix-socket egress, visitor UID denial at the socket directory, and per-slot memory, PID and cleanup behavior on the deployment host. The legacy `integration-test.sh` assumes the previous TCP proxy port and requires an update for the Unix-socket deployment.
