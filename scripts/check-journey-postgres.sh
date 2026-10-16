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
	TestPostgresDatabaseCompareJourneyPreservesExactValuesAndChanges
	TestPostgresCopyJourneyRefusesANameAndLeavesTheTargetAsItWas
	TestPostgresToBigQueryProviderNeutralCopyPreservesRows
	TestPostgresRoundTripJourney
	TestPostgresOriginJourney
	TestPostgresCopyFailureLeavesTheTargetAsItWas
	TestPostgresViolatedForeignKeyIsCreatedNotValid
	TestPostgresTargetSchema
	TestPostgresExactValuesRoundTrip
	TestPostgresCopyIsIndependentOfTheServersOutputSettings
	TestPostgresStatementTimeoutDoesNotCutOrMislabelTheCopy
	TestPostgresFailedStatementsAreSaidWithTheirOwnReason
	TestPostgresSequencesKeepTheirPosition
	TestPostgresReloadIsOneTransaction
	TestPostgresFilteredCopies
	TestPostgresSourceIsReadAsOneSnapshot
	TestPostgresJSONBRecognizerAgreesWithTheServer
	TestPostgresRealHoldsWhatF32ExactAccepts
	TestPostgresDecimalsTextKeepsTheText
	TestPostgresReloadSurvivesKeyCyclesAndOrphans
	TestPostgresStandardConformingStringsOffDoesNotRewriteTheDDL
	TestPostgresReloadKeepsTheDefinitionOfEveryKey
	TestPostgresDecimalDefaultsThatDoNotFit
	TestPostgresYearZeroWidensTheColumn
	TestPostgresNormalizedCountIsForTheTypeTheColumnGets
	TestPostgresReloadOfATableWithAnIdentityAndADeferredKey
	TestPostgresReloadOfAChildWhoseParentIsNotReloaded
	TestPostgresStatementTimeoutDoesNotCutASlowReload
	TestPostgresReloadMovesTheCounterOfASequenceTheColumnDoesNotOwn
	TestPostgresReloadRefusesAKeyItCannotPutBack
	TestPostgresVerifyReadsEveryFieldOfAForeignKey
	TestPostgresDecimalWarningsSayWhatTheServerDoes
	TestPostgresNumericNaNIsKeptAsTextAndCounted
)

# Tests of a feature that only PostgreSQL 18 has. On 18 they must PASS like every other; on an older
# major (DATATUG_TEST_POSTGRES_MAJOR) they must SKIP, by name: a skip is allowed there and nowhere else.
# A test that is added here is not added to required_tests.
pg18_only_tests=(
	TestPostgresReloadKeepsANotEnforcedKey
)

# The tests of the journey are found by name here and a new one must be listed in required_tests (or in
# pg18_only_tests): TestEveryPostgresTestOfTheJourneyIsRequired (pkg/dbcopy/faithful) fails when a test
# that needs the server is in neither.

# check reads the verbose output of `go test` in file $1 and prints each problem. It
# returns non-zero when there is one.
check() {
	local output="$1" problems=0 name major="${DATATUG_TEST_POSTGRES_MAJOR:-}" older=0 skips
	if [ -n "$major" ] && [ "$major" -lt 18 ]; then
		older=1
	fi
	skips="$(grep -E -- '--- SKIP' "$output" || true)"
	if [ "$older" = 1 ]; then
		for name in "${pg18_only_tests[@]}"; do
			if ! grep -E -q -- "^--- SKIP: ${name} " "$output"; then
				echo "check-journey-postgres: ${name} must run and skip on PostgreSQL ${major}, and did not" >&2
				problems=1
			fi
			skips="$(printf '%s\n' "$skips" | grep -v -E -- "--- SKIP: ${name} " || true)"
		done
	fi
	if [ -n "$skips" ]; then
		printf '%s\n' "$skips" >&2
		echo "check-journey-postgres: a test of the journey skipped (lines above): is DATATUG_TEST_POSTGRES_URL set, and the service up?" >&2
		problems=1
	fi
	for name in "${required_tests[@]}" "${pg18_only_tests[@]}"; do
		if [ "$older" = 1 ] && printf '%s\n' "${pg18_only_tests[@]}" | grep -q -x -- "$name"; then
			continue
		fi
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

run_pattern="^($(IFS='|'; echo "${required_tests[*]}|${pg18_only_tests[*]}"))\$"
status=0
go test -count=1 -v -run "$run_pattern" ./apps/datatugapp/commands/ ./pkg/dbcopy/faithful/ 2>&1 | tee "$output" || status=$?
if [ "$status" -ne 0 ]; then
	echo "check-journey-postgres: go test failed (exit $status)" >&2
	exit "$status"
fi
check "$output"
