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
compose=(docker compose -p "$project" -f "$deploy_dir/compose.yaml" -f "$work_dir/compose.test.yaml")
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

cat > "$work_dir/compose.test.yaml" <<'YAML'
services:
  edge:
    ports:
      - "127.0.0.1::3128"
YAML
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

print_host_key() {
    "${compose[@]}" run --rm --no-deps -T demo demo ssh-server --print-host-key \
        --state-dir /var/lib/datatug-demo/state
}

if ! "${compose[@]}" build edge || ! "${compose[@]}" up -d --wait --wait-timeout 180; then
    printf 'FAIL build or start a healthy Compose project\n' >&2
    exit 1
fi
printf 'PASS build and start a healthy Compose project\n'
if ! ssh_port=$(port_for 2222) || ! proxy_port=$(port_for 3128); then
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
timeout --kill-after=2s 15s "${ssh_client[@]}" -o StrictHostKeyChecking=accept-new -tt "$ssh_target" \
    somecommand < /dev/null > "$work_dir/exec.log" 2>&1 || exec_status=$?

check_initial_key() {
    local observed_fingerprint
    observed_fingerprint=$(ssh-keygen -E sha256 -lf "$work_dir/known_hosts" | awk 'NR == 1 {print $2}') || return 1
    [[ -n $expected_fingerprint && $observed_fingerprint == "$expected_fingerprint" ]] &&
        grep -Fq 'Remote protocol version 2.0, remote software version datatug-demo' "$work_dir/exec.log" &&
        grep -Fq "Server host key: ssh-ed25519 $expected_fingerprint" "$work_dir/exec.log"
}

check_pty() {
    local pty_status=0
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
' "${ssh_client[@]}" -o StrictHostKeyChecking=yes -tt "$ssh_target" \
        < /dev/null > "$work_dir/pty.log" 2>&1 || pty_status=$?
    if [[ $pty_status == 0 || $pty_status == 124 ]] &&
        grep -Fq 'Welcome to the DataTug demo' "$work_dir/pty.log"; then
        return 0
    fi
    cat "$work_dir/pty.log" >&2
    return 1
}

check_exec_refused() {
    local logfile=$1 status=$2
    if [[ $status == 255 ]] && grep -Fq 'exec request failed on channel' "$logfile"; then
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
    check_exec_refused "$work_dir/restart.log" "$status" &&
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

check_connect_refused() {
    local response
    response=$(timeout --kill-after=2s 8s bash -e -c '
        exec 3<>"/dev/tcp/127.0.0.1/$1"
        printf "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n" >&3
        IFS= read -r line <&3
        printf "%s\n" "$line"
    ' bash "$proxy_port") || return 1
    [[ $response =~ ^HTTP/1\.[01][[:blank:]]403([[:space:]]|$) ]]
}

check 'SSH banner and host key fingerprint' check_initial_key
check 'PTY shows the DataTug welcome screen' check_pty
check 'SSH exec request is refused' check_exec_refused "$work_dir/exec.log" "$exec_status"
check 'SSH local forwarding channel is refused' check_forwarding_refused
check 'host key survives demo restart' check_persistent_key
check 'demo cannot dial 1.1.1.1:443 directly' check_direct_egress_blocked
check 'edge returns HTTP 403 for a non-allow-listed CONNECT' check_connect_refused

(( failures == 0 ))
