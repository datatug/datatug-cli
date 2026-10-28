#!/usr/bin/env bash
# check-journey-couchdb.sh runs the CouchDB command journey against an owned,
# disposable CouchDB 3.5.3 server.
#
# It starts the pinned image on a private loopback port with random
# administrator and reader credentials, creates one database whose members are
# the reader, then runs:
#   1. the Go command-path journey (apps/datatugapp/commands/cmd_query_couchdb_real_test.go),
#      which seeds native documents and Mango indexes over the admin HTTP API and
#      drives the real `query run` command, native primary-index enumeration,
#      saved-query resolution, access policies and the generic schema reader;
#   2. the datatug binary built from this checkout, so at least one proof is a
#      separate CLI process rather than a cobra call in the test binary.
# The container and its anonymous volume are removed when the script ends. That
# teardown failure is an error once the owned container has started, but it
# never replaces the exit status of a failing journey. It never uses another
# worker's container, database or port.
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
# started stays 0 until this script's own docker run has succeeded, so a launch
# failure never removes a container name this script did not create.
started=0
cleanup() {
	status=$?
	set +e
	if [ "$started" = 1 ]; then
		# -v also drops the image's anonymous data volume; the exact owned name
		# keeps this away from every other container.
		docker rm -f -v "$name" >/dev/null 2>&1
		removed=$?
		if [ "$removed" != 0 ]; then
			echo "could not remove the owned CouchDB container $name (docker rm exited $removed)" >&2
			[ "$status" != 0 ] || status=1
		fi
	fi
	rm -rf "$work" >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT

echo "starting $image as $name"
docker run -d --name "$name" \
	-e COUCHDB_USER=admin -e COUCHDB_PASSWORD="$admin_password" \
	-p 127.0.0.1::5984 "$image" >/dev/null
started=1
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

# The reader is a database member, which CouchDB alone would let write. Make the
# fixture server-enforced read-only: validate_doc_update rejects every writer
# that is not the server admin (whose userCtx carries the _admin role), so the
# Go test can still seed with the admin account. The probe below proves the
# reader is really refused (the adapter's own write refusal is tested separately).
curl -fsS -u "$admin_auth" -X PUT "$base/$database/_design/datatug_readonly" \
	-H 'Content-Type: application/json' \
	-d '{"validate_doc_update":"function(newDoc, oldDoc, userCtx) { if (userCtx.roles.indexOf(\"_admin\") !== -1) { return; } throw({forbidden: \"datatug journey fixture is read-only\"}); }"}' >/dev/null
write_status=$(curl -s -o "$work/write-probe.json" -w '%{http_code}' \
	-u "$reader_user:$reader_password" -X PUT "$base/$database/datatug-write-probe" \
	-H 'Content-Type: application/json' -d '{"probe":true}')
if [ "$write_status" != 403 ]; then
	cat "$work/write-probe.json" >&2 || true
	echo "the reader write probe exited HTTP $write_status, want 403: the fixture is not server-enforced read-only" >&2
	exit 1
fi

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
export DATATUG_TEST_COUCHDB_ORDERS="$DATATUG_TEST_COUCHDB_URL"

# A saved query names a connection descriptor that names the operator's
# environment variable; neither the descriptor nor the catalog holds the
# endpoint or a credential. This is the same source the Go journey resolves, in
# a separate CLI process.
project="$work/project"
mkdir -p "$project/environments/local/catalogs/orders" "$project/connections/local" "$project/queries/orders"
cat >"$project/datatug-project.json" <<JSON
{"id":"couch-project","title":"CouchDB project"}
JSON
cat >"$project/environments/local/local.env.json" <<JSON
{"id":"local","dbServers":[{"driver":"couchdb","catalogs":["orders"]}]}
JSON
cat >"$project/connections/local/orders.json" <<JSON
{"dsnEnv":"DATATUG_TEST_COUCHDB_ORDERS"}
JSON
cat >"$project/environments/local/catalogs/orders/orders.db.json" <<JSON
{"driver":"couchdb","path":"connections/local/orders.json","dbModel":"orders"}
JSON
cat >"$project/queries/orders/top.query.json" <<JSON
{"id":"orders/top","title":"Top orders","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"orders"}]}
JSON
cat >"$project/queries/orders/top.query.tugql.yaml" <<YAML
from: {name: $database}
columns:
  - {field: name}
  - {field: score}
  - {field: ownerID}
where:
  op: ==
  left: {field: status}
  right: {value: active}
orderBy:
  - {field: score, desc: true}
limit: 3
YAML
"$work/datatug" query run --project "$project" --query orders/top --env local --format json >"$work/saved.json" 2>"$work/saved.err" || {
	cat "$work/saved.err" >&2
	echo "the built CLI could not run the saved CouchDB query through its connection descriptor" >&2
	exit 1
}
saved_rows=$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$work/saved.json")
[ "$saved_rows" = 3 ] || { cat "$work/saved.json"; echo "the saved CLI query returned $saved_rows rows, want 3" >&2; exit 1; }
if grep -rq -e "$admin_password" -e "$reader_password" "$project"; then
	echo "the saved project holds a credential" >&2
	exit 1
fi
[ "$(cat "$project/environments/local/catalogs/orders/orders.db.json")" = '{"driver":"couchdb","path":"connections/local/orders.json","dbModel":"orders"}' ] || { cat "$project/environments/local/catalogs/orders/orders.db.json" >&2; echo "the catalog does not record the descriptor" >&2; exit 1; }

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

# An unfiltered --from read is served by CouchDB's native _all_docs primary
# index. It must return the five requested native records in _id order, with
# their native fields, and the fixture's read-only _design document — which
# sorts before every application document — must not consume a result slot.
"$work/datatug" query run --db "$DATATUG_TEST_COUCHDB_URL" --from "$database" --limit 5 --format json --no-policies >"$work/from.json" 2>"$work/from.err" || {
	cat "$work/from.err" >&2
	echo "the built CLI could not read a bounded primary-index page of CouchDB" >&2
	exit 1
}
if ! python3 - "$work/from.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
want = ["archived-%03d" % i for i in range(5)]
if len(rows) != 5:
    sys.exit("rows=%d, want 5 native records: %r" % (len(rows), rows))
keys = [row["$key"] for row in rows]
if keys != want:
    sys.exit("keys=%r, want %r in native _id order (management documents must not consume a slot)" % (keys, want))
names = [row["name"] for row in rows]
if names != ["Archived %03d" % i for i in range(5)]:
    sys.exit("names=%r" % (names,))
if [row["status"] for row in rows] != ["archived"] * 5:
    sys.exit("statuses=%r" % ([row["status"] for row in rows],))
PY
then
	cat "$work/from.json" >&2
	echo "the bounded --from read did not return the expected 5 native records" >&2
	exit 1
fi

# The same read with no --limit gets the visible exploration bound, and that
# bound is a real multi-page budget: 180 seeded documents, 100 returned.
"$work/datatug" query run --db "$DATATUG_TEST_COUCHDB_URL" --from "$database" --format json --no-policies >"$work/from-default.json" 2>"$work/from-default.err" || {
	cat "$work/from-default.err" >&2
	echo "the built CLI could not read a default-bounded primary-index page of CouchDB" >&2
	exit 1
}
default_rows=$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$work/from-default.json")
[ "$default_rows" = 100 ] || { cat "$work/from-default.json"; echo "a --from read with no --limit returned $default_rows rows, want the 100-row exploration bound" >&2; exit 1; }
grep -q 'reading at most 100 rows' "$work/from-default.err" || { cat "$work/from-default.err"; echo "a default-bounded --from read did not state its bound" >&2; exit 1; }

# A filter keeps the Mango path: no seeded index serves this predicate, so the
# read is refused explicitly rather than being served by the primary scan.
cat >"$work/filtered-no-index.yaml" <<YAML
from: {name: $database}
columns:
  - {field: _id}
  - {field: name}
where:
  op: ==
  left: {field: kind}
  right: {value: order}
limit: 3
YAML
set +e
"$work/datatug" query run --db "$DATATUG_TEST_COUCHDB_URL" -f "$work/filtered-no-index.yaml" --format json --no-policies >"$work/noindex.out" 2>"$work/noindex.err"
noindex_status=$?
set -e
[ "$noindex_status" = 4 ] || { cat "$work/noindex.err" >&2; echo "a filtered CouchDB read with no served Mango index exited $noindex_status, want 4" >&2; exit 1; }
[ ! -s "$work/noindex.out" ] || { cat "$work/noindex.out"; echo "a refused filtered read printed rows" >&2; exit 1; }
grep -q 'CouchDB requires a Mango index' "$work/noindex.err" || { cat "$work/noindex.err"; echo "the filtered refusal did not explain the Mango index" >&2; exit 1; }

echo "couchdb journey passed: $database on $base"
