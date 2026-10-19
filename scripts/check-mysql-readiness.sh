#!/usr/bin/env bash
# Bounded readiness regression; no Docker daemon, network or datasets required.
set -euo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=scripts/lib/mysql-readiness.sh
source scripts/lib/mysql-readiness.sh

work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-mysql-readiness.XXXXXX")
trap 'rm -rf "$work"' EXIT
ready='/usr/sbin/mysqld: ready for connections. Version: 8.4.11 socket: mysql.sock port: 3306 MySQL Community Server'
producer_status=0
# The fake Docker producer must propagate write failures, just like the CLI.
docker() {
	[[ $1 == logs && $2 == fake-mysql ]] || return 64
	cat "$work/logs" || return "$?"
	return "$producer_status"
}
expect_status() {
	local expected=$1 actual=0
	mysql_tcp_ready fake-mysql || actual=$?
	[[ $actual == "$expected" ]] || { echo "$2: expected $expected, got $actual" >&2; exit 1; }
	printf 'PASS %s\n' "$2"
}

printf '%s\n' "$ready" > "$work/logs"
# Keep enough output after the ready line to force an early reader to close
# before cat finishes, on both macOS and Linux pipe-buffer sizes.
dd if=/dev/zero bs=65536 count=32 2>/dev/null | tr '\0' x >> "$work/logs"
# Positive control proves this fixture exposes the original pipefail bug.
set +e
docker logs fake-mysql 2>&1 | grep -q 'ready for connections.*port: 3306'
statuses=("${PIPESTATUS[@]}")
set -e
[[ ${statuses[0]} -ne 0 && ${statuses[1]} == 0 ]] || { echo 'SIGPIPE control did not reproduce the old failure' >&2; exit 1; }
expect_status 0 'ready line with large trailing output drains producer'

printf '%s\n' "$ready" > "$work/logs"
expect_status 0 'ordinary SQL TCP readiness'
printf 'mysqld: ready for connections. port: 3306\n' > "$work/logs"
expect_status 0 'SQL port at end of line'
printf 'mysqld: ready for connections. port:   3306  MySQL\n' > "$work/logs"
expect_status 0 'SQL port whitespace'
for port in 0 33060 33061 13306 3306.1; do
	printf 'mysqld: ready for connections. port: %s MySQL\n' "$port" > "$work/logs"
	expect_status 1 "reject port $port"
done
printf 'X Plugin ready for connections. port: 33060, socket: mysqlx.sock\n' > "$work/logs"
expect_status 1 'X Plugin alone is not SQL readiness'
printf 'mysqld: ready for connections. port: 0 MySQL\n' >> "$work/logs"
expect_status 1 'temporary socket server plus X Plugin is not ready'
printf '%s\n' "$ready" >> "$work/logs"
expect_status 0 'real SQL server after temporary and X Plugin messages'
printf 'Starting temporary server\n' > "$work/logs"
expect_status 1 'startup without readiness'
: > "$work/logs"
expect_status 1 'empty logs'
printf '%s\n' "$ready" > "$work/logs"
producer_status=23
expect_status 23 'producer failure rejects even matching output'
: > "$work/logs"
expect_status 1 'producer failure without matching output'
