# shellcheck shell=bash
# fixture-lifecycle.sh owns the teardown and trap wiring of a disposable
# Compose fixture. scripts/check-journey-postgrest.sh sources it, and
# scripts/test-fixture-lifecycle.sh regression-tests the same functions.
#
# The sourcing script sets, before calling fixture_lifecycle_install:
#   project  the exact Compose project name this run alone owns
#   work     the scratch directory this run alone created
#   compose  array: docker compose bound to that project and its compose file
#   started  0 until that project exists, 1 once `docker compose up` returned
#
# The functions keep the script body's exit status out of reach: a teardown
# never resets it, a non-zero body status survives a successful teardown, and
# a teardown failure turns a passing body into a failure. Cleanup runs once:
# the EXIT trap is disarmed before teardown, and a signal exits through the
# same EXIT trap with its conventional non-zero status instead of returning 0.

fixture_lifecycle_teardown() {
	local removed=0
	# Only the project this run started is removed; a failed launch leaves
	# `started` at 0 and must not touch a project name it never created.
	if [ "${started:-0}" = 1 ]; then
		# compose is the array the sourcing script assigned before installing.
		# shellcheck disable=SC2154
		"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1
		removed=$?
		if [ "$removed" != 0 ]; then
			echo "could not remove the owned Compose project ${project:-<unset>} (docker compose down exited $removed)" >&2
			return 1
		fi
	fi
	if [ -n "${work:-}" ]; then
		rm -rf "$work" >/dev/null 2>&1 || true
	fi
	return 0
}

fixture_lifecycle_cleanup() {
	local status=$? torn=0
	# Disarm every trap first so this cleanup cannot run twice, then keep the
	# body's own status unless the body passed and teardown failed.
	trap - EXIT INT TERM
	set +e
	fixture_lifecycle_teardown
	torn=$?
	set -e
	if [ "$status" = 0 ] && [ "$torn" != 0 ]; then
		status=$torn
	fi
	exit "$status"
}

# fixture_lifecycle_signal NAME CODE exits with the conventional signal status;
# the EXIT trap still performs the one teardown.
fixture_lifecycle_signal() {
	local name=$1 code=$2
	trap - INT TERM
	echo "interrupted ($name); removing the owned fixture" >&2
	exit "$code"
}

fixture_lifecycle_install() {
	trap fixture_lifecycle_cleanup EXIT
	trap 'fixture_lifecycle_signal INT 130' INT
	trap 'fixture_lifecycle_signal TERM 143' TERM
}
