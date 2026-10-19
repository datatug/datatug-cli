#!/usr/bin/env bash
# Read the complete snapshot: grep -q can close the pipe early and make
# docker logs fail with SIGPIPE under pipefail despite a matching ready line.
mysql_tcp_ready() (
	set -o pipefail
	docker logs "$1" 2>&1 | LC_ALL=C grep -E 'ready for connections.*port:[[:space:]]+3306([[:space:]]|$)' >/dev/null
)
