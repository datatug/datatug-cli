#!/usr/bin/env bash
# test-fixture-lifecycle.sh regression-tests scripts/lib/fixture-lifecycle.sh,
# the teardown and trap code scripts/check-journey-postgrest.sh actually uses.
# Every scenario runs those real functions in a subshell with a docker stub, so
# it proves the shipped cleanup, not a copy of it:
#   - a failing body stays failed when teardown succeeds;
#   - a failing body keeps its own status when teardown also fails;
#   - a passing body fails when teardown fails;
#   - a passing body passes when teardown succeeds, removing its work dir;
#   - a body that never started a project invokes no docker command;
#   - INT and TERM exit non-zero and still tear down exactly once.
set -euo pipefail
# work, started and compose are read by the lifecycle functions each scenario
# sources below, and that source path is this checkout's, not an input.
# shellcheck disable=SC2034,SC1091
cd "$(dirname "$0")"
helper=$PWD/lib/fixture-lifecycle.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/datatug-fixture-lifecycle-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

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
	local name=$1 name_text=$2 haystack=$3
	case "$haystack" in
		*"$name_text"*) echo "ok: $name" ;;
		*) echo "not ok: $name: missing $name_text in $haystack" >&2; return 1 ;;
	esac
}

# run_cleanup_case NAME EXPECTED_STATUS DOCKER_STATUS STARTED BODY runs the real
# lifecycle in a subshell and leaves its docker log, stderr and work dir behind.
run_cleanup_case() {
	local label=$1 want=$2 docker_status=$3 started_flag=$4 body=$5
	local dir=$tmp/$label got=0
	mkdir -p "$dir/work"
	(
		set -euo pipefail
		work=$dir/work
		project=owned-fixture-project
		started=$started_flag
		compose=(docker compose -p "$project")
		# The helper invokes this stub indirectly through compose. SC2317 is the
		# same indirect-invocation report on the older shellcheck the CI runner
		# ships (SC2329 superseded it), so both codes stay disabled.
		# shellcheck disable=SC2329,SC2317
		docker() {
			printf '%s\n' "$*" >>"$dir/docker.log"
			return "$docker_status"
		}
		# shellcheck source=scripts/lib/fixture-lifecycle.sh
		. "$helper"
		fixture_lifecycle_install
		eval "$body"
		exit 0
	) >/dev/null 2>"$dir/stderr" || got=$?
	check expect_equal "$label exits" "$want" "$got"
}

run_cleanup_case failing-body-kept 7 0 1 'exit 7'
check expect_equal failing-body-kept-teardown-ran 1 "$(wc -l <"$tmp/failing-body-kept/docker.log" | tr -d ' ')"
check expect_equal failing-body-kept-work-removed yes "$([ -e "$tmp/failing-body-kept/work" ] && echo no || echo yes)"

run_cleanup_case failing-body-beats-cleanup-failure 7 5 1 'exit 7'
check expect_contains failing-body-beats-cleanup-failure-message \
	"could not remove the owned Compose project owned-fixture-project" \
	"$(cat "$tmp/failing-body-beats-cleanup-failure/stderr")"

run_cleanup_case passing-body-cleanup-failure 1 5 1 ':'

run_cleanup_case passing-body-cleanup-success 0 0 1 ':'
check expect_equal passing-body-cleanup-success-teardown-ran 1 "$(wc -l <"$tmp/passing-body-cleanup-success/docker.log" | tr -d ' ')"
check expect_contains passing-body-cleanup-success-exact-command \
	"compose -p owned-fixture-project down -v --remove-orphans" \
	"$(cat "$tmp/passing-body-cleanup-success/docker.log")"
check expect_equal passing-body-cleanup-success-work-removed yes \
	"$([ -e "$tmp/passing-body-cleanup-success/work" ] && echo no || echo yes)"

run_cleanup_case never-started-removes-nothing 0 9 0 ':'
check expect_equal never-started-removes-nothing-no-docker yes \
	"$([ -e "$tmp/never-started-removes-nothing/docker.log" ] && echo no || echo yes)"

# signal_case SIGNAL EXPECTED_STATUS runs the real lifecycle in a foreground
# child process (a background job ignores INT in a non-interactive shell), has
# that child signal itself once its traps are installed, and checks the exact
# status, the single teardown and the message.
signal_case() {
	local signal=$1 want=$2 label="interrupt-$1" dir=$tmp/"interrupt-$1"
	mkdir -p "$dir/work"
	cat >"$dir/case.sh" <<'CHILD'
# shellcheck disable=SC2034,SC1091,SC2329
set -euo pipefail
dir=$1 helper=$2 signal=$3
work=$dir/work
project=owned-fixture-project
started=1
compose=(docker compose -p "$project")
# The helper invokes this stub indirectly through compose.
# shellcheck disable=SC2329
docker() {
	printf '%s\n' "$*" >>"$dir/docker.log"
	return 0
}
# shellcheck source=scripts/lib/fixture-lifecycle.sh
. "$helper"
fixture_lifecycle_install
# $$ is this script's PID, also inside this background subshell.
( sleep 0.2; kill -"$signal" $$ ) &
child=$!
: >"$dir/ready"
wait "$child"
CHILD
	local got=0
	bash "$dir/case.sh" "$dir" "$helper" "$signal" >/dev/null 2>"$dir/stderr" || got=$?
	expect_equal "$label exits" "$want" "$got"
	check expect_equal "$label-teardown-ran-once" 1 \
		"$([ -e "$dir/docker.log" ] && wc -l <"$dir/docker.log" | tr -d ' ' || echo 0)"
	check expect_contains "$label-message" "interrupted ($signal)" "$(cat "$dir/stderr")"
}

signal_case INT 130
signal_case TERM 143

echo "fixture lifecycle: ${checks} checks, ${failures} failures"
if [ "$failures" != 0 ]; then
	exit 1
fi
