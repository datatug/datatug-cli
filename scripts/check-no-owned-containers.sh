#!/usr/bin/env bash
# check-no-owned-containers.sh NAME_PREFIX [LABEL]: fails unless no container
# whose name starts with NAME_PREFIX (and, when given, carries LABEL) exists.
#
# Each journey of Go CI removes its exact owned container in its own EXIT
# trap; this proves that cleanup really happened before the next journey of
# the same job starts. It only reads: it never prunes another worker's Docker
# objects. A failing `docker ps` is a failure, never an empty listing.
set -euo pipefail

prefix="${1:?usage: check-no-owned-containers.sh NAME_PREFIX [LABEL]}"
filters=(--filter "name=$prefix")
if [ -n "${2:-}" ]; then
	filters+=(--filter "label=$2")
fi

status=0
listing=$(docker ps -aq "${filters[@]}") || status=$?
if [ "$status" != 0 ]; then
	echo "docker ps failed with status $status; the removal of the owned $prefix container is unproven" >&2
	exit 1
fi
if [ -n "$listing" ]; then
	docker ps -a "${filters[@]}"
	echo "an owned $prefix container was not removed" >&2
	exit 1
fi
echo "no $prefix container remains"
