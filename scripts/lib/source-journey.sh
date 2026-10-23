# shellcheck shell=bash
# run_source_journey VAR URL WORK TEST... runs the journey of a MySQL, Oracle or SQL Server source
# (apps/datatugapp/commands/server_journey_*_real_test.go: `datatug scan`, `datatug query run` and saved
# queries) against the server a journey script started, with the administrator's URL in the variable
# VAR, and fails unless every TEST ran and passed. `go test` reports a skipped test as success, so the
# verbose output is read: any SKIP, or a missing PASS line for a TEST, fails. WORK is the script's
# scratch folder, where the output is kept.
run_source_journey() {
	local var=$1 url=$2 work=$3
	shift 3
	local out="$work/source-journey.txt" status=0 pattern name
	pattern="^($(IFS='|'; echo "$*"))\$"
	env "$var=$url" go test -count=1 -v -run "$pattern" ./apps/datatugapp/commands/ >"$out" 2>&1 || status=$?
	if [ "$status" -ne 0 ] || grep -q -- '--- SKIP' "$out"; then
		cat "$out" >&2
		echo "the source journey failed or skipped" >&2
		return 1
	fi
	for name in "$@"; do
		grep -q -- "^--- PASS: $name " "$out" || { cat "$out" >&2; echo "no PASS line for $name" >&2; return 1; }
	done
	echo "source journey (scan, query run, saved queries) passed: $*"
}
