# shellcheck shell=bash
# typesense-fixture-lifecycle.sh owns the container teardown and trap wiring of
# the disposable Typesense fixture. scripts/check-journey-typesense.sh sources
# it, and scripts/test-typesense-lifecycle.sh regression-tests the same
# functions with a fake docker executable.
#
# The sourcing script sets, before calling typesense_lifecycle_install:
#   name          the unique name this run alone gave docker create
#   container_id  the exact container id docker create printed, empty until then
#   created       0 until docker create returned, 1 once the container exists
#   work          the scratch directory this run alone created
#
# Cleanup removes only the exact container id this run created. A failed create
# leaves created at 0, so a name a previous run used (or a collision) is never
# removed. Teardown fails closed: docker rm -f -v must acknowledge the exact id,
# and a separate docker inspect must return Docker's own exact no-such-object
# reply for that same id. A non-zero rm, a still-present container, and a
# permission, daemon or unknown inspect error are all teardown failures. The
# functions keep the body's exit status: a teardown never resets it, a non-zero
# body status survives a successful teardown, and a teardown failure turns a
# passing body into a failure. Cleanup runs once: every trap is disarmed before
# teardown, and a signal exits through the same EXIT trap with its conventional
# non-zero status.

typesense_lifecycle_teardown() {
	if [ "${created:-0}" = 1 ] && [ -n "${container_id:-}" ]; then
		local rm_output rm_status inspect_stderr inspect_status expected
		rm_output=$(docker rm -f -v "$container_id" 2>&1)
		rm_status=$?
		# The volume-removing rm must be acknowledged. Even if the container is
		# already absent, a failed acknowledgement proves no volume cleanup.
		if [ "$rm_status" != 0 ]; then
			printf 'could not remove the owned Typesense container %s (docker rm exited %s): %s\n' \
				"$container_id" "$rm_status" "$rm_output" >&2
			return 1
		fi
		# A name is not an ownership proof, and another container that reused the
		# name must not be touched. Prove this exact id is gone with Docker's own
		# no-such-object reply; any other inspect outcome is unproven, not absent.
		# Docker prints an empty JSON array ("[]") on stdout and its diagnostic on
		# stderr, so capture stderr alone: 2>&1 first duplicates stderr into the
		# command substitution, then >/dev/null discards stdout. Combining the two
		# streams would make the exact reply unmatchable for a real removal.
		inspect_stderr=$(docker inspect "$container_id" 2>&1 >/dev/null)
		inspect_status=$?
		if [ "$inspect_status" = 0 ]; then
			printf 'could not remove the owned Typesense container %s: docker inspect still reports it\n' \
				"$container_id" >&2
			return 1
		fi
		expected="Error: No such object: $container_id"
		if [ "${inspect_stderr,,}" != "${expected,,}" ]; then
			printf 'could not verify removal of the owned Typesense container %s (docker inspect exited %s): %s\n' \
				"$container_id" "$inspect_status" "$inspect_stderr" >&2
			return 1
		fi
	fi
	if [ -n "${work:-}" ]; then
		rm -rf "$work" >/dev/null 2>&1 || true
	fi
	return 0
}

typesense_lifecycle_cleanup() {
	local status=$? torn=0
	# Disarm every trap first so this cleanup cannot run twice, then keep the
	# body's own status unless the body passed and teardown failed.
	trap - EXIT INT TERM
	set +e
	typesense_lifecycle_teardown
	torn=$?
	set -e
	if [ "$status" = 0 ] && [ "$torn" != 0 ]; then
		status=$torn
	fi
	exit "$status"
}

typesense_lifecycle_signal() {
	local name=$1 code=$2
	trap - INT TERM
	echo "interrupted ($name); removing the owned Typesense container" >&2
	exit "$code"
}

typesense_lifecycle_install() {
	trap typesense_lifecycle_cleanup EXIT
	trap 'typesense_lifecycle_signal INT 130' INT
	trap 'typesense_lifecycle_signal TERM 143' TERM
}
