# Operate the DataTug SSH demo

Run the commands from the repository root on a Linux host with Docker Engine, BuildKit, and Docker Compose v2. See the [deployment design](../../spec/features/ssh-demo/deployment.md) and [visitor guide](../../docs/ssh-demo.md).

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

To upgrade, check out the desired revision on this host and repeat the build and `up -d --wait` commands with fresh build credentials. The host key survives in the `state` volume. Keep the same Compose project name and volume when upgrading or moving the checkout.

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
| `DEMO_MAX_SESSIONS` | `16` | Total live session slots. |
| `DEMO_MAX_SESSIONS_PER_IP` | `3` | Live session slots per client IP. |
| `DEMO_IDLE_TIMEOUT` | `15m` | How long a session can go without client input. |
| `DEMO_MAX_SESSION_DURATION` | `60m` | Maximum time per session. |
| `DEMO_HANDSHAKE_TIMEOUT` | `20s` | Maximum time for SSH handshake. |
| `DEMO_UID_BASE` | `20000` | First uid and gid in the session slot range. |
| `DEMO_SESSIONS_TMPFS` | `2g` | Aggregate session workspace storage cap. |
| `DEMO_MEM_LIMIT` | `3g` | Demo memory cap, including tmpfs usage. |
| `DEMO_CPUS` | `2` | Demo cgroup CPU quota in cores. |
| `DEMO_PIDS_LIMIT` | `2048` | Demo cgroup limit for processes and threads. |

## Size and troubleshoot

- **Capacity.** Measure peak resident memory per session under representative SQLite, DuckDB, and OpenVaultDB queries. Reserve headroom for the supervisor, template preparation, and used tmpfs pages, then size concurrency to fit the cgroup's memory and PID limits. The child has a Go memory target of 768 MiB and an 8 GiB virtual address ceiling. Native allocations can add to resident memory. Size tmpfs from peak workspace bytes times expected concurrency, including the copied repository and generated files.
- **Healthcheck fails.** Inspect `docker compose -f deploy/ssh-demo/compose.yaml ps` and both service logs. The edge health probe traverses the TCP forwarder, so startup or template failures in the supervisor can affect both probes. Probe the demo directly with `docker compose -f deploy/ssh-demo/compose.yaml exec -T demo /usr/local/bin/datatug demo ssh-health --addr 127.0.0.1:2222`.
- **“Demo is full.”** Check global and per-IP slots, including visitors behind the same NAT. Inspect session end events for `end_reason=full` and check for slots retained after cleanup errors. Measure memory, tmpfs, CPU and PID headroom before raising the caps.
- **Template clone fails.** Check the HTTPS repo URL, ref and project manifest path. Ensure the edge can resolve the GitHub hosts and open TCP 443. Check the edge log's `host` and `result` for CONNECT denials, and confirm the supervisor has `HTTPS_PROXY=http://edge:3128`.
- **UID isolation refuses to start.** The supervisor needs container uid 0, its four capabilities, and a root-owned sessions tmpfs with mode `0711`. Verify the effective Compose service and mount options if you supplied an override.
- **Session logs.** Match the JSON `session start` and `session end` events by `session_id` and inspect `client_ip`, `ssh_user`, `pty_width`, `pty_height`, `duration_ms`, `end_reason`, and `exit_status`. Edge egress logs report the hostname and outcome.

## Isolation

The supervisor runs as container root with `SETUID`, `SETGID`, `CHOWN` and `KILL`. It assigns each visitor a different uid and gid and a mode-`0700` workspace. Both services use read-only root filesystems and `no-new-privileges`. Visitors can read world-readable image files. Enable Docker `userns-remap` where supported to map container identities to unprivileged host identities.

CPU fairness is enforced per container plus a per-child `RLIMIT_CPU`. The container has a shared cgroup and sessions have individual rlimits. Direct egress from the supervisor network is blocked and HTTPS egress uses the edge allow-list. DataTug forgets GitHub tokens at disconnect. Visitors can revoke their OAuth grant in GitHub Settings under Applications.

## Integration check

With build credentials set, Bash, OpenSSH, GNU `timeout`, and Python 3 installed, run:

```bash
deploy/ssh-demo/integration-test.sh
```

The script creates a disposable Compose project and publishes SSH and the proxy on random loopback ports. It tears down the containers, state volume, networks and test image when it exits. The production Compose file publishes only SSH.
