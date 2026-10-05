#!/usr/bin/env bash
# check-journey-postgres.sh runs the PostgreSQL journey (the tests of
# apps/datatugapp/commands/scan_journey_postgres_real_test.go,
# query_journey_postgres_real_test.go and copy_journey_postgres_real_test.go) against the server of the CI job
# "Journey (PostgreSQL <major>)" and fails the job unless every one of them ran
# and passed.
#
# `go test` reports a test that skips as success. These tests skip when
# DATATUG_TEST_POSTGRES_URL is not set, so a job whose service never came up, or whose
# variable was renamed, would stay green having tested nothing. This script reads the
# verbose output instead: it fails on any SKIP, and on a missing top-level
# "--- PASS: <name>" line for each test, by name.
#
# Usage: scripts/check-journey-postgres.sh            run the tests, then check
#        scripts/check-journey-postgres.sh --check F  check the output file F only
set -euo pipefail

# The tests this job requires, by name. A test added to the journey is added here.
required_tests=(
	TestPostgresScanJourney
	TestPostgresScanJourneyFailures
	TestPostgresQueryJourneyOrdersNullsByDALgosRule
	TestPostgresCopyJourneyRefusesANameAndLeavesTheTargetAsItWas
)

# check reads the verbose output of `go test` in file $1 and prints each problem. It
# returns non-zero when there is one.
check() {
	local output="$1" problems=0 name
	if grep -E -- '--- SKIP' "$output" >&2; then
		echo "check-journey-postgres: a test of the journey skipped (lines above): is DATATUG_TEST_POSTGRES_URL set, and the service up?" >&2
		problems=1
	fi
	for name in "${required_tests[@]}"; do
		if ! grep -E -q -- "^--- PASS: ${name} " "$output"; then
			echo "check-journey-postgres: no PASS line for ${name}" >&2
			problems=1
		fi
	done
	return "$problems"
}

if [ "${1:-}" = "--check" ]; then
	check "${2:?usage: check-journey-postgres.sh --check OUTPUT_FILE}"
	exit
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

output="$(mktemp "${TMPDIR:-/tmp}/journey-postgres.XXXXXX")"
trap 'rm -f "$output"' EXIT

run_pattern="^($(IFS='|'; echo "${required_tests[*]}"))\$"
status=0
go test -count=1 -v -run "$run_pattern" ./apps/datatugapp/commands/ 2>&1 | tee "$output" || status=$?
if [ "$status" -ne 0 ]; then
	echo "check-journey-postgres: go test failed (exit $status)" >&2
	exit "$status"
fi
check "$output"
