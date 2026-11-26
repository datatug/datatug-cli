#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C TERM=xterm-256color
deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/dt-ssh.XXXXXXXX")
project="datatug-ssh-test-${work_dir##*.}"
project=${project,,}
export DEMO_IMAGE="$project:local"
# The empty host port lets Docker allocate a free loopback port at bind time.
export DEMO_SSH_PUBLISH=127.0.0.1:
# compose.yaml pins the internal bridge to 172.31.250.0/24, which a deployed demo
# on this host already holds. Run a copy on a free 172.31.N.0/24 instead; the
# project directory stays deploy/ssh-demo, so relative build paths still resolve.
used_subnets=$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' $(docker network ls -q))
test_net=
for n in $(seq 200 249); do
    if [[ " $used_subnets " != *" 172.31.$n."* && " $used_subnets " != *" 172.31.0.0/16 "* ]]; then
        test_net=172.31.$n
        break
    fi
done
if [[ -z $test_net ]]; then
    printf 'FAIL no free 172.31.N.0/24 for the test network\n' >&2
    rm -rf -- "$work_dir"
    exit 1
fi
sed "s/172\.31\.250\./$test_net./g" "$deploy_dir/compose.yaml" > "$work_dir/compose.yaml"
compose=(docker compose -p "$project" --project-directory "$deploy_dir" -f "$work_dir/compose.yaml" -f "$work_dir/compose.test.yaml")
forward_pid=
failures=0

stop_forward_client() {
    if [[ -n $forward_pid ]]; then
        kill "$forward_pid" 2>/dev/null || true
        wait "$forward_pid" 2>/dev/null || true
        forward_pid=
    fi
}

cleanup() {
    local status=$?
    trap - EXIT
    stop_forward_client
    if (( status != 0 )); then
        "${compose[@]}" logs --no-color --tail 50 >&2 || true
    fi
    if ! "${compose[@]}" down --volumes --remove-orphans --rmi all; then
        printf 'FAIL teardown\n' >&2
        status=1
    fi
    rm -rf -- "$work_dir"
    exit "$status"
}

cat > "$work_dir/compose.test.yaml" <<YAML
services:
  demo:
    volumes:
      - "$work_dir/ssh-slot-link-probe:/run/datatug-demo/ssh-slot-link-probe:ro"
YAML
# The edge serves CONNECT only on a unix socket in the proxy-socket volume.
# A tiny Go client (golang image) speaks to it from a throwaway container.
mkdir -p "$work_dir/connectprobe"
cat > "$work_dir/connectprobe/main.go" <<'GO'
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	conn, err := net.DialTimeout("unix", os.Args[1], 5*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", os.Args[2], os.Args[2])
	line, _ := bufio.NewReader(conn).ReadString('\n')
	fmt.Print(line)
}
GO
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

check() {
    local label=$1
    shift
    if "$@"; then
        printf 'PASS %s\n' "$label"
    else
        printf 'FAIL %s\n' "$label" >&2
        failures=$((failures + 1))
    fi
}

port_for() {
    local binding
    binding=$("${compose[@]}" port edge "$1") || return 1
    if [[ $binding =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then
        printf '%s\n' "${BASH_REMATCH[1]}"
    else
        printf 'Unexpected publish address: %s\n' "$binding" >&2
        return 1
    fi
}

# exec, not run: the demo has a fixed address, so a second demo container
# cannot join the internal network while the first one runs.
print_host_key() {
    "${compose[@]}" exec -T demo /usr/local/bin/datatug demo ssh-server --print-host-key \
        --state-dir /var/lib/datatug-demo/state
}

if ! docker build --target slot-link-probe --output "type=local,dest=$work_dir" \
        -f "$deploy_dir/Dockerfile" "$deploy_dir/../.." ||
    ! "${compose[@]}" build edge || ! "${compose[@]}" up -d --wait --wait-timeout 180; then
    printf 'FAIL build or start a healthy Compose project\n' >&2
    exit 1
fi
printf 'PASS build and start a healthy Compose project\n'
if ! ssh_port=$(port_for 2222); then
    printf 'FAIL resolve the allocated loopback ports\n' >&2
    exit 1
fi
ssh_client=(
    ssh -F /dev/null -v -p "$ssh_port"
    -o BatchMode=yes -o ConnectTimeout=8
    -o ControlMaster=no -o HostKeyAlgorithms=ssh-ed25519
    -o GlobalKnownHostsFile=/dev/null -o "UserKnownHostsFile=$work_dir/known_hosts"
)
ssh_target=visitor@127.0.0.1

expected_key=$(print_host_key) || expected_key=
expected_fingerprint=$(awk '/^SHA256:[[:alnum:]\/+]+$/ {print; exit}' <<< "$expected_key")
exec_status=0
timeout --kill-after=2s 15s "${ssh_client[@]}" -o StrictHostKeyChecking=accept-new -T "$ssh_target" \
    somecommand < /dev/null > "$work_dir/exec.log" 2>&1 || exec_status=$?

check_initial_key() {
    local observed_fingerprint
    observed_fingerprint=$(ssh-keygen -E sha256 -lf "$work_dir/known_hosts" | awk 'NR == 1 {print $2}') || return 1
    [[ -n $expected_fingerprint && $observed_fingerprint == "$expected_fingerprint" ]] &&
        grep -Fq 'Remote protocol version 2.0, remote software version datatug-demo' "$work_dir/exec.log" &&
        grep -Fq "Server host key: ssh-ed25519 $expected_fingerprint" "$work_dir/exec.log"
}

# check_pty [name [remote command...]]: with a command, the client sends exec
# after pty-req, as Warp's SSH wrapper does; the server must run the demo.
check_pty() {
    local name=${1:-pty} pty_status=0
    if (($#)); then shift; fi
    # OpenSSH reports a zero-size window with /dev/null as its stdin. Give this
    # probe a sized local PTY while keeping it independent of the caller's terminal.
    timeout --kill-after=2s 15s python3 -c '
import fcntl
import os
import pty
import struct
import subprocess
import sys
import termios

master, slave = pty.openpty()
try:
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    sys.exit(subprocess.run(sys.argv[1:], stdin=slave, check=False).returncode)
finally:
    os.close(slave)
    os.close(master)
' "${ssh_client[@]}" -o StrictHostKeyChecking=yes -tt "$ssh_target" "$@" \
        < /dev/null > "$work_dir/$name.log" 2>&1 || pty_status=$?
    if [[ $pty_status == 0 || $pty_status == 124 ]] &&
        grep -Fq 'Welcome to the DataTug demo' "$work_dir/$name.log"; then
        return 0
    fi
    cat "$work_dir/$name.log" >&2
    return 1
}

check_exec_without_pty() {
    local logfile=$1 status=$2
    if [[ $status == 1 ]] && grep -Fq 'This demo needs an interactive terminal' "$logfile" &&
        ! grep -Fq 'exec request failed' "$logfile"; then
        return 0
    fi
    cat "$logfile" >&2
    return 1
}

check_forwarding_refused() {
    local socket_path="$work_dir/forward.sock" logfile="$work_dir/forward.log"
    # OpenSSH requests direct-tcpip only after someone connects to the local -L listener.
    timeout --kill-after=2s 20s "${ssh_client[@]}" -o StrictHostKeyChecking=yes -o ExitOnForwardFailure=yes \
        -N -L "$socket_path:127.0.0.1:2222" "$ssh_target" < /dev/null > "$logfile" 2>&1 &
    forward_pid=$!
    if python3 - "$socket_path" <<'PY'
import socket
import sys
import time

with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sock:
    sock.settimeout(5)
    deadline = time.monotonic() + 8
    while True:
        try:
            sock.connect(sys.argv[1])
            break
        except (FileNotFoundError, ConnectionRefusedError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.1)
    if sock.recv(1):
        sys.exit("Forwarded connection received unexpected data")
PY
    then
        stop_forward_client
        if grep -Eq 'open failed: (administratively prohibited|unknown channel type):' "$logfile"; then
            return 0
        fi
    fi
    stop_forward_client
    cat "$logfile" >&2
    return 1
}

check_persistent_key() {
    local restarted_key status=0
    "${compose[@]}" restart demo &&
        "${compose[@]}" up -d --wait --wait-timeout 180 --no-recreate &&
        restarted_key=$(print_host_key) &&
        [[ $restarted_key == "$expected_key" ]] || return 1
    timeout --kill-after=2s 15s "${ssh_client[@]}" -o StrictHostKeyChecking=yes -T "$ssh_target" somecommand \
        < /dev/null > "$work_dir/restart.log" 2>&1 || status=$?
    check_exec_without_pty "$work_dir/restart.log" "$status" &&
        grep -Fq "Server host key: ssh-ed25519 $expected_fingerprint" "$work_dir/restart.log"
}

check_direct_egress_blocked() {
    local logfile="$work_dir/egress.log"
    if ! "${compose[@]}" exec -T demo /usr/local/bin/datatug demo ssh-health \
        --addr 1.1.1.1:443 --timeout 5s > "$logfile" 2>&1 &&
        grep -Fq 'ssh-health: connect 1.1.1.1:443:' "$logfile"; then
        return 0
    fi
    cat "$logfile" >&2
    return 1
}

# The host's address on the internal bridge would expose every host service
# that listens on all interfaces (admin sshd first). With the isolated gateway
# mode the bridge has no host address, so the TCP connect itself must fail.
check_host_gateway_unreachable() {
    local logfile="$work_dir/gateway.log" network gateway
    network=$("${compose[@]}" ps -q demo | xargs docker inspect \
        -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}}{{end}}') || return 1
    gateway=$(docker network inspect "$network" -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}') || return 1
    [[ -n $gateway ]] || return 1
    if ip -4 addr show | grep -Fq "inet $gateway/"; then
        printf 'host still has %s on the internal bridge\n' "$gateway" >&2
        return 1
    fi
    if ! "${compose[@]}" exec -T demo /usr/local/bin/datatug demo ssh-health \
        --addr "$gateway:22" --timeout 5s > "$logfile" 2>&1 &&
        grep -Fq "ssh-health: connect $gateway:22:" "$logfile"; then
        return 0
    fi
    cat "$logfile" >&2
    return 1
}

connect_status() {
    timeout --kill-after=2s 60s docker run --rm --network none \
        -v "${project}_proxy-socket:/s:ro" -v "$work_dir/connectprobe:/p:ro" -w /p \
        -e GOCACHE=/tmp/gocache -e GO111MODULE=off \
        golang:1.27.3 go run main.go /s/connect.sock "$1"
}

check_connect_refused() {
    local response
    response=$(connect_status example.com:443) || { printf '%s\n' "$response" >&2; return 1; }
    [[ $response =~ ^HTTP/1\.[01][[:blank:]]403([[:space:]]|$) ]] || { printf '%s\n' "$response" >&2; return 1; }
}

check_connect_allowed() {
    local response
    response=$(connect_status github.com:443) || { printf '%s\n' "$response" >&2; return 1; }
    [[ $response =~ ^HTTP/1\.[01][[:blank:]]200([[:space:]]|$) ]] || { printf '%s\n' "$response" >&2; return 1; }
}

# A slot uid must not reach the edge's socket directory at all.
check_slot_uid_cannot_reach_proxy() {
    local out
    local uid=${DEMO_UID_BASE:-20000}
    if out=$("${compose[@]}" exec -T --user "$uid:$uid" demo /bin/sh -c 'ls /run/datatug-demo/proxy' 2>&1); then
        printf 'slot uid listed the proxy directory: %s\n' "$out" >&2
        return 1
    fi
    [[ $out == *"Permission denied"* ]] || { printf '%s\n' "$out" >&2; return 1; }
}

slots_dir='/sys/fs/cgroup'
last_slot=$(("${DEMO_MAX_SESSIONS:-6}" - 1))

check_private_cgroup_and_ipc() {
    "${compose[@]}" exec -T demo /bin/sh -ec '
        grep -q " - cgroup2 .*nsdelegate" /proc/self/mountinfo
        test -d /sys/fs/cgroup/supervisor
        test ! -e /sys/fs/cgroup/../cgroup.controllers
        ! grep -q " /dev/shm " /proc/self/mountinfo
        limit=$(cat /sys/fs/cgroup/memory.max)
        if (echo "$limit" > /sys/fs/cgroup/memory.max) 2>/dev/null; then
            exit 1
        fi
    ' && "${compose[@]}" exec -T edge /bin/sh -c '! grep -q " /dev/shm " /proc/self/mountinfo'
}

check_slot_limits() {
    local out
    out=$("${compose[@]}" exec -T demo /bin/sh -c "for d in $slots_dir/slot-*; do
        printf '%s %s %s %s\n' \"\$(basename \$d)\" \"\$(cat \$d/memory.max)\" \"\$(cat \$d/memory.swap.max)\" \"\$(cat \$d/pids.max)\"; done") || return 1
    printf '%s\n' "$out" | awk -v want="${DEMO_MAX_SESSIONS:-6}" '
        $2 == 570425344 && $3 == 0 && $4 == 256 { ok++ } END { exit !(ok == want && NR == want) }' ||
        { printf '%s\n' "$out" >&2; return 1; }
}

check_sessions_inode_budget() {
    "${compose[@]}" exec -T demo /bin/sh -ec '
        required=1
        for slot in /sys/fs/cgroup/slot-*; do
            cap=$(cat "$slot/memory.max")
            required=$((required + (cap + 511) / 512 + 5))
            test -e "$slot/memory.reclaim"
        done
        inodes=$(stat -f -c %c /var/lib/datatug-demo/sessions)
        test "$inodes" -ge "$required"
    '
}

check_cleanup_limits() {
    "${compose[@]}" exec -T demo /bin/sh -ec '
        cd /sys/fs/cgroup/cleanup
        test "$(cat memory.max)" = 100663296
        test "$(cat memory.swap.max)" = 0
        test "$(cat memory.oom.group)" = 1
        test "$(cat pids.max)" = 32
    '
}

# The kernel, not a watchdog, stops a slot: a 700 MiB tmpfs write inside the
# last slot is OOM-group-killed at its 544 MiB memory.max and the supervisor
# stays healthy.
check_slot_oom_contained() {
    local status=0 events
    "${compose[@]}" exec -T demo /bin/sh -c "echo \$\$ > $slots_dir/slot-$last_slot/cgroup.procs &&
        exec head -c 734003200 /dev/zero > /var/lib/datatug-demo/sessions/oom-probe" >/dev/null 2>&1 || status=$?
    "${compose[@]}" exec -T demo /bin/rm -f /var/lib/datatug-demo/sessions/oom-probe || true
    events=$("${compose[@]}" exec -T demo /bin/sh -c "cat $slots_dir/slot-$last_slot/memory.events") || return 1
    (( status == 137 )) || { printf 'probe exit %s, want 137 (killed)\n' "$status" >&2; return 1; }
    [[ $events =~ oom_group_kill\ [1-9] ]] || { printf '%s\n' "$events" >&2; return 1; }
    "${compose[@]}" exec -T demo /usr/local/bin/datatug demo ssh-health --addr 127.0.0.1:2222 --proxy-protocol >/dev/null
}

check_slot_isolation() {
    local uid=${DEMO_UID_BASE:-20000}
    "${compose[@]}" exec -T demo /bin/sh -ec '
        base=/var/lib/datatug-demo/sessions
        dir=$base/slot-0/6bbbd4c7425cd06af440590be39f3608
        other=$base/slot-1/6bbbd4c7425cd06af440590be39f3608
        trap "rmdir $dir $other" EXIT
        mkdir -m 0700 "$dir"
        chown "$2:$2" "$dir"
        mkdir -m 0777 "$other"
        chown "$(($2 + 1)):$(($2 + 1))" "$other"
        test "$(stat -c "%u:%g:%a" "$base/slot-0")" = "0:$2:710"
        test "$(stat -c "%u:%g:%a" "$base/slot-1")" = "0:$(($2 + 1)):710"
        GOMAXPROCS=2 /run/datatug-demo/ssh-slot-link-probe isolation "$1" "$2" "$dir" "$other"
    ' probe "$slots_dir/slot-0" "$uid"
}

# Build both trees under a live session's credentials and cgroup. Closing
# that SSH connection exercises the production cleanup and slot reuse path.
check_deep_session_cleanup() (
    local uid=${DEMO_UID_BASE:-20000} client= workspace= result deadline
    local parent=/var/lib/datatug-demo/sessions/slot-0
    local fifo="$work_dir/deep-input"
    mkfifo "$fifo" || return 1
    exec {hold}<>"$fifo"
    trap 'if [[ -n $client ]]; then kill "$client" 2>/dev/null || true; wait "$client" 2>/dev/null || true; fi' EXIT
    timeout 90s "${ssh_client[@]}" -o StrictHostKeyChecking=yes -tt "$ssh_target" \
        <&"$hold" >"$work_dir/deep-ssh.log" 2>&1 &
    client=$!
    deadline=$((SECONDS + 20))
    while [[ -z $workspace && $SECONDS -lt $deadline ]]; do
        workspace=$("${compose[@]}" exec -T demo /bin/sh -c '
            if [ -n "$(cat "$1/cgroup.procs")" ]; then
                set -- "$2"/*
                if [ "$#" -eq 1 ] && [ -d "$1" ]; then printf "%s" "$1"; fi
            fi
        ' probe "$slots_dir/slot-0" "$parent") || return 1
        [[ -n $workspace ]] || sleep 1
    done
    [[ -n $workspace ]] || { cat "$work_dir/deep-ssh.log" >&2; return 1; }
    result=$("${compose[@]}" exec -T -e GOMAXPROCS=2 demo /run/datatug-demo/ssh-slot-link-probe \
        deep "$slots_dir/slot-0" "$uid" "$workspace") || return 1
    [[ $result == 'created 10000 short-name levels and 40 long-name levels' ]] || return 1
    kill "$client" 2>/dev/null || return 1
    wait "$client" 2>/dev/null || true
    client=
    deadline=$((SECONDS + 165))
    while ! "${compose[@]}" exec -T demo /usr/bin/test ! -e "$workspace"; do
        (( SECONDS < deadline )) || return 1
        sleep 1
    done

    timeout 20s "${ssh_client[@]}" -o StrictHostKeyChecking=yes -tt "$ssh_target" \
        <&"$hold" >"$work_dir/reused-ssh.log" 2>&1 &
    client=$!
    deadline=$((SECONDS + 15))
    while (( SECONDS < deadline )); do
        result=$("${compose[@]}" exec -T demo /bin/cat "$slots_dir/slot-0/cgroup.procs") || return 1
        [[ -z $result ]] || return 0
        sleep 1
    done
    cat "$work_dir/reused-ssh.log" >&2
    return 1
)

# A live session's child is inside a slot as a slot uid; after the visitor
# leaves, the slot drains to empty.
check_session_in_slot_and_drained() {
    local pids uid client
    timeout 30s "${ssh_client[@]}" -o StrictHostKeyChecking=no -tt "$ssh_target" \
        < <(sleep 20) > /dev/null 2>&1 &
    client=$!
    sleep 6
    pids=$("${compose[@]}" exec -T demo /bin/sh -c "cat $slots_dir/slot-*/cgroup.procs") || { kill "$client"; return 1; }
    [[ -n $pids ]] || { printf 'no process in any slot during a session\n' >&2; kill "$client"; return 1; }
    uid=$("${compose[@]}" exec -T demo /bin/sh -c "awk '/^Uid:/ {print \$2, \$3, \$4, \$5}' /proc/$(head -n1 <<< "$pids")/status") || { kill "$client"; return 1; }
    kill "$client" 2>/dev/null || true
    wait "$client" 2>/dev/null || true
    local real eff saved fs
    read -r real eff saved fs <<< "$uid"
    [[ $real =~ ^[0-9]+$ && $eff == "$real" && $saved == "$real" && $fs == "$real" ]] &&
        (( real >= ${DEMO_UID_BASE:-20000} && real <= ${DEMO_UID_BASE:-20000} + last_slot )) ||
        { printf 'slot process uids: %s\n' "$uid" >&2; return 1; }
    sleep 6
    pids=$("${compose[@]}" exec -T demo /bin/sh -c "cat $slots_dir/slot-*/cgroup.procs") || return 1
    [[ -z $pids ]] || { printf 'slot not drained: %s\n' "$pids" >&2; return 1; }
}

check_queue_handover() {
    DEMO_MAX_SESSIONS=1 DEMO_MAX_QUEUE=20 DEMO_MAX_QUEUE_WAIT=30m DEMO_MAX_SESSIONS_PER_IP=2 \
        "${compose[@]}" up -d --no-deps --force-recreate --wait --wait-timeout 180 demo || return 1
    python3 - "$work_dir" "${ssh_client[@]}" -o StrictHostKeyChecking=yes -tt "$ssh_target" <<'PY'
import contextlib
import fcntl
import os
import pathlib
import pty
import struct
import subprocess
import sys
import termios
import time

work_dir = pathlib.Path(sys.argv[1])
ssh_command = sys.argv[2:]


def stop(client):
    if client.poll() is None:
        client.terminate()
        try:
            client.wait(timeout=5)
        except subprocess.TimeoutExpired:
            client.kill()
            client.wait()


def start(stack, name):
    path = work_dir / f"queue-{name}.log"
    master, slave = pty.openpty()
    stack.callback(os.close, master)
    try:
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
        with path.open("wb") as output:
            client = subprocess.Popen(ssh_command, stdin=slave, stdout=output, stderr=output)
    finally:
        os.close(slave)
    stack.callback(stop, client)
    return client, path


def see(client, path, text, seconds):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        output = path.read_bytes()
        if text in output:
            return
        if client.poll() is not None:
            break
        time.sleep(0.1)
    sys.stderr.buffer.write(path.read_bytes())
    sys.exit(f"{path.name} did not show {text.decode()!r}")


with contextlib.ExitStack() as stack:
    first, first_log = start(stack, "first")
    welcome = b"Welcome to the DataTug demo"
    see(first, first_log, welcome, 20)
    second, second_log = start(stack, "second")
    see(second, second_log, b"You are number 1 in the queue", 10)
    stop(first)
    see(second, second_log, welcome, 45)
PY
}

check 'SSH banner and host key fingerprint' check_initial_key
check 'PTY shows the DataTug welcome screen' check_pty
check 'exec with a PTY (as Warp sends) shows the DataTug welcome screen' check_pty pty-exec somecommand
check 'exec without a PTY gets the no-terminal message' check_exec_without_pty "$work_dir/exec.log" "$exec_status"
check 'SSH local forwarding channel is refused' check_forwarding_refused
check 'host key survives demo restart' check_persistent_key
check 'demo cannot dial 1.1.1.1:443 directly' check_direct_egress_blocked
check 'demo cannot reach host services through the bridge gateway' check_host_gateway_unreachable
check 'edge returns HTTP 403 for a non-allow-listed CONNECT' check_connect_refused
check 'edge tunnels an allow-listed CONNECT (github.com:443)' check_connect_allowed
check 'slot uid cannot reach the edge proxy socket' check_slot_uid_cannot_reach_proxy
check 'demo cgroup mount exposes its delegated scope and both services omit /dev/shm' check_private_cgroup_and_ipc
check 'every slot has its memory, swap and pids limits' check_slot_limits
check 'sessions tmpfs has the required inode budget and slots expose memory.reclaim' check_sessions_inode_budget
check 'cleanup cgroup has independent memory, swap, OOM and PID limits' check_cleanup_limits
check 'a session child runs in a slot as its slot uid, and the slot drains' check_session_in_slot_and_drained
check 'slot 0 denies link, linkat, AF_UNIX sockets, System V IPC and access to slot 1' check_slot_isolation
check 'cleanup removes the 10000-level, long-name and read-only boundary trees and slot 0 serves another visitor' check_deep_session_cleanup
check 'kernel OOM-kills a slot over its limit; supervisor stays healthy' check_slot_oom_contained
check 'second SSH visitor queues and gets the welcome screen when the sole slot is released' check_queue_handover

(( failures == 0 ))
