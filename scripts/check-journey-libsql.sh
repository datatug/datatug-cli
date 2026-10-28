#!/usr/bin/env bash
# check-journey-libsql.sh runs the libSQL command journey against the published
# adapter's own fixture, without a source worktree of that adapter and without
# copying its protocol code.
#
# The fixture is resolved from the module the build already pins
# (github.com/dal-go/dalgo2libsql): `go mod download -json` gives its module
# directory, and its scripts/libsql-fixture.mjs is executed from there. That script
# owns everything the journey must not reimplement: a commit-pinned real
# libsql-server 0.24.33 container, an ephemeral Ed25519 key pair and short-lived
# JWTs, writer-only seeding of native fixtures (mixed storage classes, integer and
# text primary keys, a composite key, blobs, big integers, a foreign key, a view),
# a reader-write 403 probe, and teardown of only its own container and recorded
# volumes. Only the reader token reaches the CLI child
# (scripts/lib/libsql-cli-journey.sh), which maps it to DATATUG_LIBSQL_BEARER and
# never prints it.
#
# The child's exit status is this script's status. A teardown failure makes the
# fixture process fail the run after the child; a failing child status is kept.
# Nothing else on the machine is touched: no global daemon, no broad prune, no
# paid service, no production credential.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD

if ! command -v docker >/dev/null; then
	echo "docker is required for the libSQL journey" >&2
	exit 1
fi
if ! docker info >/dev/null 2>&1; then
	echo "the docker daemon is not reachable" >&2
	exit 1
fi

# Node 22 or newer is needed for the fixture's fetch and node:crypto use. Set
# NODE to an explicit binary (for example a Node 24 installation) when the
# first one on PATH is older.
node=${NODE:-node}
if ! command -v "$node" >/dev/null; then
	echo "node is required for the published libSQL fixture" >&2
	exit 1
fi
node_major=$("$node" -p 'process.versions.node.split(".")[0]')
if [ "$node_major" -lt 22 ]; then
	echo "node 22 or newer is required for the published libSQL fixture (found $("$node" --version))" >&2
	exit 1
fi

# The module directory of the exact adapter version this build pins: no source
# worktree, no copied fixture, no local replace.
module_dir=$(go mod download -json github.com/dal-go/dalgo2libsql | python3 -c 'import json,sys; print(json.load(sys.stdin)["Dir"])')
fixture="$module_dir/scripts/libsql-fixture.mjs"
if [ ! -f "$fixture" ]; then
	echo "the published libSQL module has no fixture at $fixture" >&2
	exit 1
fi
echo "published libSQL fixture: $fixture (node $("$node" --version))"

work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-libsql-run.XXXXXX")
trap 'rm -rf "$work"' EXIT
log="$work/libsql-fixture.log"
set +e
"$node" "$fixture" -- bash "$root/scripts/lib/libsql-cli-journey.sh" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
set -e
if [ "$status" != 0 ]; then
	echo "the libSQL journey failed with status $status; its log is $log" >&2
	exit "$status"
fi
# The receipt is printed only after the fixture process has torn down its own
# container and verified its recorded volumes are gone.
grep -qF -- 'libsql journey passed:' "$log" || {
	echo "the libSQL journey printed no pass receipt" >&2
	exit 1
}
grep -qF -- 'teardown: containerRemoved=true' "$log" || {
	echo "the libSQL fixture reported no verified teardown" >&2
	exit 1
}
echo "libsql journey PASS: published fixture container and volumes removed"
