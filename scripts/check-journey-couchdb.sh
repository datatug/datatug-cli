#!/usr/bin/env bash
# check-journey-couchdb.sh runs the CouchDB command journey against an owned,
# disposable CouchDB 3.5.3 server.
#
# It starts the pinned image on a private loopback port with random
# administrator and reader credentials, creates one database whose members are
# the reader, then runs:
#   1. the Go command-path journey (apps/datatugapp/commands/cmd_query_couchdb_real_test.go),
#      which seeds native documents and Mango indexes over the admin HTTP API and
#      drives the real `query run` command, saved-query resolution, access policies
#      and the generic schema reader;
#   2. the datatug binary built from this checkout, so at least one proof is a
#      separate CLI process rather than a cobra call in the test binary.
# The container is removed when the script ends. It never uses another worker's
# container, database or port.
set -euo pipefail

cd "$(dirname "$0")/.."
image=${COUCHDB_IMAGE:-apache/couchdb:3.5.3@sha256:d6bc2568f38d9f568afdc09cb33b871608db861201d885e34769878049da43f2}
run=$$-$RANDOM
name=datatug-cli-couchdb-$run
database=datatug_cli_couchdb_$run
admin_password=couch-admin-$run
reader_user=datatug_reader_$run
reader_password=couch-reader-$run
work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-couchdb.XXXXXX")
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

echo "starting $image as $name"
docker run -d --name "$name" \
	-e COUCHDB_USER=admin -e COUCHDB_PASSWORD="$admin_password" \
	-p 127.0.0.1::5984 "$image" >/dev/null
port=$(docker port "$name" 5984/tcp | head -1 | sed 's/.*://')
base=http://127.0.0.1:$port
admin_auth=admin:$admin_password

ready=0
for _ in $(seq 1 120); do
	if curl -fsS -u "$admin_auth" "$base/_up" >/dev/null 2>&1; then ready=1; break; fi
	sleep 1
done
if [ "$ready" != 1 ]; then
	echo "the CouchDB server did not come up" >&2
	docker logs "$name" >&2 || true
	exit 1
fi
echo "server version $(curl -fsS -u "$admin_auth" "$base/" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')"

# A CouchDB 3.5 single node does not create _users until it is named, and a
# database created by the administrator is readable only by its members.
curl -fsS -u "$admin_auth" -X PUT "$base/_users" >/dev/null
curl -fsS -u "$admin_auth" -X PUT "$base/_users/org.couchdb.user:$reader_user" \
	-H 'Content-Type: application/json' \
	-d "{\"name\":\"$reader_user\",\"password\":\"$reader_password\",\"roles\":[],\"type\":\"user\"}" >/dev/null
curl -fsS -u "$admin_auth" -X PUT "$base/$database" >/dev/null
curl -fsS -u "$admin_auth" -X PUT "$base/$database/_security" \
	-H 'Content-Type: application/json' \
	-d "{\"admins\":{\"names\":[],\"roles\":[]},\"members\":{\"names\":[\"$reader_user\"],\"roles\":[]}}" >/dev/null

export DATATUG_TEST_COUCHDB_URL="couchdb+http://127.0.0.1:$port/$database"
export DATATUG_TEST_COUCHDB_ADMIN_USER=admin
export DATATUG_TEST_COUCHDB_ADMIN_PASSWORD="$admin_password"
export DATATUG_TEST_COUCHDB_READER_USER="$reader_user"
export DATATUG_TEST_COUCHDB_READER_PASSWORD="$reader_password"

echo "running the Go command journey"
test_log=$work/test.log
status=0
go test ./apps/datatugapp/commands/ -run 'TestCouchDBQueryJourney|TestCouchDBSavedQueryAndPolicyJourney' -count=1 -v >"$test_log" 2>&1 || status=$?
if [ "$status" != 0 ]; then
	cat "$test_log"
	exit "$status"
fi
grep -q -- '--- PASS: TestCouchDBQueryJourney' "$test_log" || { cat "$test_log"; echo "the CouchDB query journey did not pass" >&2; exit 1; }
grep -q -- '--- PASS: TestCouchDBSavedQueryAndPolicyJourney' "$test_log" || { cat "$test_log"; echo "the saved-query journey did not pass" >&2; exit 1; }
echo "Go command journey passed"

echo "building datatug from this checkout"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .

export DATATUG_COUCHDB_USERNAME="$reader_user"
export DATATUG_COUCHDB_PASSWORD="$reader_password"
cat >"$work/top.yaml" <<YAML
from: {name: $database}
columns:
  - {field: name}
  - {field: score}
where:
  op: ==
  left: {field: status}
  right: {value: active}
orderBy:
  - {field: score, desc: true}
limit: 3
YAML
"$work/datatug" query run --db "$DATATUG_TEST_COUCHDB_URL" -f "$work/top.yaml" --format json --no-policies >"$work/top.json" 2>"$work/top.err" || {
	cat "$work/top.err" >&2
	echo "the built CLI could not run the bounded CouchDB query" >&2
	exit 1
}
rows=$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$work/top.json")
[ "$rows" = 3 ] || { cat "$work/top.json"; echo "the built CLI returned $rows rows, want 3" >&2; exit 1; }
first=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[0]["name"])' "$work/top.json")
[ "$first" = "Order 149" ] || { cat "$work/top.json"; echo "the built CLI returned $first first" >&2; exit 1; }

set +e
"$work/datatug" query run --db "$DATATUG_TEST_COUCHDB_URL" --from "$database" --limit 5 --format json --no-policies >"$work/from.out" 2>"$work/from.err"
from_status=$?
set -e
[ "$from_status" = 4 ] || { cat "$work/from.err" >&2; echo "a bounded CouchDB --from read with no filter/order exited $from_status, want 4 (refused for lack of a served Mango index, not for being unbounded)" >&2; exit 1; }
[ ! -s "$work/from.out" ] || { cat "$work/from.out"; echo "a refused --from read printed rows" >&2; exit 1; }
grep -q 'CouchDB requires a Mango index' "$work/from.err" || { cat "$work/from.err"; echo "the --from refusal did not explain the Mango index" >&2; exit 1; }

echo "couchdb journey passed: $database on $base"
