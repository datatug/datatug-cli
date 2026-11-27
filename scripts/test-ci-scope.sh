#!/usr/bin/env bash
# Regression test of scripts/ci-scope.sh against a throwaway repository.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/ci-scope.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"
git init -q
git config user.email ci@example.invalid
git config user.name ci
mkdir -p spec/features apps/datatugapp/commands pkg/dbcopy .github/workflows docs
echo a > spec/features/README.md
echo a > apps/datatugapp/commands/cmd_query_redis.go
echo a > pkg/dbcopy/copy.go
echo a > .github/workflows/golangci.yml
echo a > docs/README.md
echo a > main.go
git add -A
git commit -qm base
base=$(git rev-parse HEAD)

fail=0
expect() { # expect <scope> <true|false> <output>
	if ! grep -qx "$1=$2" <<<"$3"; then
		echo "FAIL: $label: expected $1=$2" >&2
		fail=1
	fi
}
change() { # change <path>: one commit on top of base touching path
	git checkout -q --detach "$base"
	mkdir -p "$(dirname "$1")"
	echo "$RANDOM" >> "$1"
	git add -A
	git commit -qm change
}

label="spec only"
change spec/features/README.md
out=$(bash "$script" "$base")
expect code false "$out"
expect redis false "$out"
expect mysql false "$out"

label="docs only"
change docs/README.md
out=$(bash "$script" "$base")
expect code true "$out"
expect redis false "$out"

label="redis command"
change apps/datatugapp/commands/cmd_query_redis.go
out=$(bash "$script" "$base")
expect code true "$out"
expect redis true "$out"
expect mongodb true "$out"
expect mysql true "$out"
expect oracle true "$out"
expect clickhouse true "$out"

label="nested Go file outside the scope lists"
change pkg/other/deep/x.go
out=$(bash "$script" "$base")
expect clickhouse true "$out"
expect postgrest true "$out"
expect redis false "$out"

label="nested dbcopy file (wider than GitHub's single-level *)"
change pkg/dbcopy/faithful/x.go
out=$(bash "$script" "$base")
expect mysql true "$out"
expect redis false "$out"

label="workflow change selects everything"
change .github/workflows/golangci.yml
out=$(bash "$script" "$base")
expect redis true "$out"
expect typesense true "$out"

label="missing base selects everything"
out=$(bash "$script" "")
expect redis true "$out"
expect code true "$out"
out=$(bash "$script" 0000000000000000000000000000000000000000)
expect oracle true "$out"

label="every scope is printed exactly once"
n=$(bash "$script" "$base" | cut -d= -f1 | sort | uniq -d | wc -l)
[ "$n" -eq 0 ] || { echo "FAIL: duplicate scope lines" >&2; fail=1; }

[ "$fail" -eq 0 ] && echo "ci-scope tests passed"
exit "$fail"
