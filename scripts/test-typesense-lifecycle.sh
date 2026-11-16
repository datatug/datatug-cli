#!/usr/bin/env bash
# test-typesense-lifecycle.sh regression-tests the shipped container lifecycle of
# scripts/check-journey-typesense.sh without Docker: a fake docker executable
# records every call, so a failed start, a failed teardown, a create failure and
# the failure modes of the final inspect are reproduced deterministically. It
# never starts a container and never signals anything but its own controlled
# fixture child.
set -euo pipefail

cd "$(dirname "$0")/.."
lib=scripts/lib/typesense-fixture-lifecycle.sh
bash -n "$lib"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/datatug-typesense-lifecycle.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/state"
cat >"$tmp/bin/docker" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$DOCKER_CALLS"
action=$1
shift
case "$action" in
	create)
		[ "${DOCKER_CREATE_FAIL:-0}" = 1 ] && exit 1
		printf 'owned-container-id-%s\n' "${DOCKER_SUFFIX:-x}"
		;;
	start)
		[ "${DOCKER_START_FAIL:-0}" = 1 ] && exit 1
		;;
	rm)
		for last; do :; done
		if [ "${DOCKER_RM_FAIL:-0}" = 1 ]; then
			printf 'Error response from daemon: removal failed\n' >&2
			exit 1
		fi
		printf '%s\n' "$last" >"$DOCKER_STATE/removed"
		;;
	inspect)
		for last; do :; done
		case "${DOCKER_INSPECT_MODE:-absent}" in
			absent)
				# Docker's real removed-container shape: [] on stdout, the
				# no-such-object diagnostic on stderr.
				printf '[]\n'
				printf 'error: no such object: %s\n' "$last" >&2
				exit 1
				;;
			absent-upper)
				printf '[]\n'
				printf 'error: no such object: %s\n' "${last^^}" >&2
				exit 1
				;;
			wrong-absent)
				printf '[]\n'
				printf 'error: no such object: 0123456789abcdef\n' >&2
				exit 1
				;;
			absent-realistic)
				printf '[]\n'
				printf 'error: no such object: %s\n' "$last" >&2
				exit 1
				;;
			present)
				printf '[{"Id":"%s"}]\n' "$last"
				;;
			denied)
				printf 'permission denied while trying to connect to the Docker daemon socket\n' >&2
				exit 1
				;;
			daemon)
				printf 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n' >&2
				exit 1
				;;
			generic)
				printf 'Error: response from daemon: unknown failure\n' >&2
				exit 1
				;;
			*)
				printf 'unexpected inspect mode: %s\n' "${DOCKER_INSPECT_MODE:-}" >&2
				exit 2
				;;
		esac
		;;
esac
exit 0
FAKE
chmod +x "$tmp/bin/docker"
export PATH="$tmp/bin:$PATH"
export DOCKER_CALLS="$tmp/calls"
export DOCKER_STATE="$tmp/state"

checks=0
failures=0
check() {
	checks=$((checks + 1))
	if ! "$@"; then
		failures=$((failures + 1))
	fi
}
expect_equal() {
	local name=$1 want=$2 got=$3
	if [ "$got" != "$want" ]; then
		echo "not ok: $name: got $got, want $want" >&2
		return 1
	fi
	echo "ok: $name"
}
expect_contains() {
	local name=$1 want=$2 got=$3
	case "$got" in
		*"$want"*) echo "ok: $name" ;;
		*)
			echo "not ok: $name: missing $want in $got" >&2
			return 1
			;;
	esac
}
count_calls() {
	grep -cx "$1" "$DOCKER_CALLS" 2>/dev/null || true
}
count_rm_calls() {
	grep -c '^rm ' "$DOCKER_CALLS" 2>/dev/null || true
}

# run_cleanup_case LABEL BODY_STATUS runs one emulated journey body in a
# subshell, with the lifecycle installed exactly as the real script installs it.
# The label selects the fake docker's failure mode.
run_cleanup_case() {
	local label=$1 body_status=$2
	local dir=$tmp/$label status=0
	mkdir -p "$dir"
	: >"$DOCKER_CALLS"
	rm -f "$DOCKER_STATE/removed"
	# Keep errexit enabled inside the body: a subshell on the left of `||`
	# would have errexit suppressed, so capture the status explicitly.
	set +e
	(
		set -euo pipefail
		case "$label" in
			start-fail) export DOCKER_START_FAIL=1 ;;
			create-fail) export DOCKER_CREATE_FAIL=1 ;;
			rm-failure | body-and-cleanup-fail | passing-body-cleanup-failure) export DOCKER_RM_FAIL=1 ;;
			inspect-denied) export DOCKER_INSPECT_MODE=denied ;;
			inspect-daemon) export DOCKER_INSPECT_MODE=daemon ;;
			inspect-generic) export DOCKER_INSPECT_MODE=generic ;;
			inspect-present) export DOCKER_INSPECT_MODE=present ;;
			inspect-absent-upper) export DOCKER_INSPECT_MODE=absent-upper ;;
			inspect-absent-realistic) export DOCKER_INSPECT_MODE=absent-realistic ;;
			inspect-wrong-cid) export DOCKER_INSPECT_MODE=wrong-absent ;;
		esac
		work=$dir/work
		rm -rf "$work"
		mkdir -p "$work"
		name=datatug-cli-typesense-regression
		container_id=
		created=0
		# shellcheck source=scripts/lib/typesense-fixture-lifecycle.sh
		source "$lib"
		typesense_lifecycle_install
		container_id=$(docker create --name "$name" --label "datatug.fixture=typesense-regression" image)
		created=1
		[ -n "$container_id" ]
		docker start "$container_id"
		exit "$body_status"
	) >/dev/null 2>"$dir/stderr"
	status=$?
	set -e
	echo "$status"
}

# A passing body with a successful teardown removes the exact created id once
# and leaves no work directory.
check expect_equal passing-body 0 "$(run_cleanup_case ok 0)"
check expect_equal passing-body-rm-once 1 "$(count_calls 'rm -f -v owned-container-id-x')"
check expect_equal passing-body-rm-total 1 "$(count_rm_calls)"
check expect_equal passing-body-inspect-once 1 "$(count_calls 'inspect owned-container-id-x')"
check expect_equal passing-body-work-removed yes \
	"$([ -e "$tmp/ok/work" ] && echo no || echo yes)"

# A failed start keeps its own non-zero status and still removes the created id.
check expect_equal failed-start-keeps-status 1 "$(run_cleanup_case start-fail 0)"
check expect_equal failed-start-rm-once 1 "$(count_calls 'rm -f -v owned-container-id-x')"
check expect_equal failed-start-rm-total 1 "$(count_rm_calls)"

# A failed create never removes anything: created never became 1, so a name a
# previous run used (or a collision) is not touched.
check expect_equal failed-create 1 "$(run_cleanup_case create-fail 0)"
check expect_equal failed-create-no-rm 0 "$(count_rm_calls)"

# A failed rm acknowledgement must fail even when the container is already
# absent: a non-volume cleanup is unproven, and no later inspect can prove it.
check expect_equal rm-failure 1 "$(run_cleanup_case rm-failure 0)"
check expect_equal rm-failure-rm-once 1 "$(count_calls 'rm -f -v owned-container-id-x')"
check expect_equal rm-failure-rm-total 1 "$(count_rm_calls)"
check expect_equal rm-failure-inspect-skipped 0 "$(count_calls 'inspect owned-container-id-x')"
check expect_contains rm-failure-message \
	'(docker rm exited 1)' "$(cat "$tmp/rm-failure/stderr")"

# A failed body keeps its own status even when teardown also fails. The value
# is distinct from both success and the cleanup failure.
check expect_equal failed-body-beats-cleanup-failure 7 \
	"$(run_cleanup_case body-and-cleanup-fail 7)"
check expect_contains failed-body-beats-cleanup-failure-message \
	'(docker rm exited 1)' "$(cat "$tmp/body-and-cleanup-fail/stderr")"

# A body that passes but whose teardown fails is red.
check expect_equal passing-body-cleanup-failure 1 "$(run_cleanup_case passing-body-cleanup-failure 0)"

# The final inspect must prove this exact id is absent. A permission error, a
# daemon error and an unknown error are unproven, never absence.
check expect_equal inspect-permission-denied 1 \
	"$(run_cleanup_case inspect-denied 0)"
check expect_contains inspect-permission-denied-message \
	'could not verify removal' "$(cat "$tmp/inspect-denied/stderr")"
check expect_equal inspect-daemon-failure 1 \
	"$(run_cleanup_case inspect-daemon 0)"
check expect_contains inspect-daemon-failure-message \
	'could not verify removal' "$(cat "$tmp/inspect-daemon/stderr")"
check expect_equal inspect-unknown-failure 1 \
	"$(run_cleanup_case inspect-generic 0)"
check expect_contains inspect-unknown-failure-message \
	'could not verify removal' "$(cat "$tmp/inspect-generic/stderr")"

# A still-present container means removal did not happen, even if rm said so.
check expect_equal inspect-present 1 \
	"$(run_cleanup_case inspect-present 0)"
check expect_contains inspect-present-message \
	'still reports it' "$(cat "$tmp/inspect-present/stderr")"

# The no-such-object reply must name this exact owned id, compared without case.
# Docker's own reply for our id passes even when its case differs; a reply for a
# different id is not proof that this container is gone.
check expect_equal inspect-absent-upper 0 \
	"$(run_cleanup_case inspect-absent-upper 0)"
check expect_equal inspect-wrong-cid 1 \
	"$(run_cleanup_case inspect-wrong-cid 0)"
check expect_contains inspect-wrong-cid-message \
	'could not verify removal' "$(cat "$tmp/inspect-wrong-cid/stderr")"

# Real Docker splits the streams: stdout is the empty JSON array while the
# owned-id no-such-object reply is on stderr. Pin that exact shape on the fake,
# then prove teardown accepts it and still removes the exact id once.
: >"$tmp/realistic-calls"
realistic_stdout=$(DOCKER_CALLS="$tmp/realistic-calls" DOCKER_INSPECT_MODE=absent-realistic \
	"$tmp/bin/docker" inspect owned-container-id-x 2>"$tmp/realistic-stderr") || true
check expect_equal realistic-inspect-stdout '[]' "$realistic_stdout"
check expect_equal realistic-inspect-stderr \
	'error: no such object: owned-container-id-x' "$(cat "$tmp/realistic-stderr")"
check expect_equal inspect-absent-realistic 0 \
	"$(run_cleanup_case inspect-absent-realistic 0)"
check expect_equal inspect-absent-realistic-rm-once 1 \
	"$(count_calls 'rm -f -v owned-container-id-x')"
check expect_equal inspect-absent-realistic-inspect-once 1 \
	"$(count_calls 'inspect owned-container-id-x')"
check expect_equal inspect-absent-realistic-work-removed yes \
	"$([ -e "$tmp/inspect-absent-realistic/work" ] && echo no || echo yes)"

# signal_case SIGNAL EXPECTED_STATUS runs the real lifecycle in a controlled
# child process, with its own fake container. Once created and started, that
# child signals itself through its own BASHPID, never the parent, the host or an
# arbitrary PID. The INT/TERM traps exit with the conventional status, the EXIT
# trap tears the exact id down once, and every trap is disarmed first.
signal_case() {
	local signal=$1 want=$2
	local dir=$tmp/signal-$signal status=0
	mkdir -p "$dir"
	: >"$DOCKER_CALLS"
	rm -f "$DOCKER_STATE/removed"
	cat >"$dir/case.sh" <<'CHILD'
#!/usr/bin/env bash
set -euo pipefail
dir=$1
lib=$2
signal=$3
work=$dir/work
mkdir -p "$work"
name=datatug-cli-typesense-signal
container_id=
created=0
# shellcheck source=scripts/lib/typesense-fixture-lifecycle.sh
source "$lib"
typesense_lifecycle_install
container_id=$(docker create --name "$name" --label "datatug.fixture=typesense-signal" image)
created=1
[ -n "$container_id" ]
docker start "$container_id"
: >"$dir/created"
# Signal this controlled fixture process itself, by its own BASHPID.
kill -"$signal" "$BASHPID"
# The trap exits with the conventional signal status; this is unreachable.
sleep 5
exit 0
CHILD
	bash "$dir/case.sh" "$dir" "$lib" "$signal" >/dev/null 2>"$dir/stderr" || status=$?
	check expect_equal "signal-$signal exits" "$want" "$status"
	check expect_equal "signal-$signal-rm-once" 1 "$(count_calls 'rm -f -v owned-container-id-x')"
	check expect_equal "signal-$signal-rm-total" 1 "$(count_rm_calls)"
	check expect_equal "signal-$signal-inspect-once" 1 "$(count_calls 'inspect owned-container-id-x')"
	check expect_contains "signal-$signal-message" "interrupted ($signal)" "$(cat "$dir/stderr")"
	check expect_equal "signal-$signal-work-removed" yes \
		"$([ -e "$dir/work" ] && echo no || echo yes)"
}

signal_case INT 130
signal_case TERM 143

echo "typesense lifecycle: ${checks} checks, ${failures} failures"
if [ "$failures" != 0 ]; then
	exit 1
fi
echo "typesense lifecycle regression passed"
