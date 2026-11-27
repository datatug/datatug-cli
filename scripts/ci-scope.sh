#!/usr/bin/env bash
# ci-scope.sh BASE: prints one `<scope>=true|false` line per scope of Go CI
# (.github/workflows/golangci.yml), comparing BASE with HEAD.
#
# A scope is true when any changed path matches its pathspecs. Pathspecs use
# git's default matching, where `*` also crosses `/`, so a pattern can only
# select more than its GitHub `paths:` counterpart did, never less.
#
# Every scope is true when BASE is empty (Go CI passes none on a push to main,
# which always runs everything) or not a commit in this clone, or when the
# workflow, the shared actions or these scripts changed: a change to the CI
# itself runs all of it.
#
# The `changes` job of Go CI runs this and writes the lines to GITHUB_OUTPUT;
# scripts/test-ci-scope.sh is its regression test.
set -euo pipefail
# Pathspecs below are expanded unquoted on purpose; never let bash glob them
# against the working directory (`*.go` would become the root .go files).
set -f

base="${1:-}"

declare -A scopes
# Anything but SpecScore specs and agent/tooling metadata. Docs stay in:
# tests read README.md and docs/ (quickstart_doc_test.go, scan_layout_test.go).
scopes[code]=". :(exclude)spec/** :(exclude).claude/** :(exclude)codegraph/**"
sql_script_journey="pkg/dbcopy/*.go pkg/dbcopy/faithful/** internal/crossverify/** apps/datatugapp/commands/cmd_db_copy*.go apps/datatugapp/commands/cmd_scan_db.go apps/datatugapp/commands/cmd_query*.go apps/datatugapp/commands/server_journey_*_real_test.go pkg/api/** pkg/secureread/** go.mod go.sum scripts/lib/source-journey.sh scripts/journey-datasets.txt"
scopes[mysql]="$sql_script_journey scripts/check-journey-mysql.sh scripts/check-mysql-readiness.sh scripts/lib/mysql-readiness.sh"
scopes[mssql]="$sql_script_journey pkg/dbcopy/** scripts/check-journey-mssql.sh"
scopes[oracle]="$sql_script_journey pkg/dbcopy/** scripts/check-journey-oracle.sh"
scopes[clickhouse]="*.go go.mod go.sum scripts/check-journey-clickhouse.sh testdata/clickhouse"
accountfree="pkg/dbcopy/** pkg/api/** pkg/secureread/** pkg/schemers/** apps/datatugapp/commands/** go.mod go.sum scripts/test-fixture-lifecycle.sh scripts/lib/fixture-lifecycle.sh"
scopes[couchdb]="$accountfree scripts/check-journey-couchdb.sh"
scopes[libsql]="$accountfree scripts/check-journey-libsql.sh scripts/lib/libsql-cli-journey.sh"
scopes[postgrest]="*.go go.mod go.sum go.work go.work.sum scripts/check-journey-postgrest.sh scripts/test-fixture-lifecycle.sh scripts/lib testdata/postgrest"
scopes[mongodb]="pkg/dbcopy/** pkg/secureread/** apps/datatugapp/commands/** go.mod go.sum"
scopes[redis]="apps/datatugapp/commands/cmd_query*.go go.mod go.sum"
scopes[typesense]="apps/datatugapp/commands/** pkg/accesspolicies/** pkg/api/** pkg/dbcopy/** pkg/secureread/** pkg/schemers/** main.go go.mod go.sum docs/query/typesense.md scripts/check-journey-typesense.sh scripts/test-typesense-lifecycle.sh scripts/lib/typesense-fixture-lifecycle.sh scripts/lib/typesense-cli-journey.sh"

ci_itself=".github/workflows/golangci.yml .github/workflows/native-release.yml .github/actions scripts/ci-scope.sh scripts/check-no-owned-containers.sh"

all=false
if [ -z "$base" ] || ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	all=true
else
	# shellcheck disable=SC2086 # pathspec lists are space-separated on purpose
	if ! git diff --quiet "$base" HEAD -- $ci_itself; then
		all=true
	fi
fi

for scope in $(printf '%s\n' "${!scopes[@]}" | sort); do
	if [ "$all" = true ]; then
		echo "$scope=true"
		continue
	fi
	# shellcheck disable=SC2086
	if git diff --quiet "$base" HEAD -- ${scopes[$scope]}; then
		echo "$scope=false"
	else
		echo "$scope=true"
	fi
done
