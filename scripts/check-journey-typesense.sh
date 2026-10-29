#!/usr/bin/env bash
# check-journey-typesense.sh runs the Typesense command journey against an owned,
# disposable Typesense 30.2 server.
#
# It starts the pinned image on a private loopback port with a random bootstrap
# admin key, seeds the synthetic books/semantics/archive collections, creates two
# scoped keys (search-only, and search plus collection metadata), then runs:
#   1. the Go command-path journey (apps/datatugapp/commands/cmd_query_typesense_real_test.go),
#      which drives structured --from paging, native search, schema discovery,
#      a real scan and a saved query with DataTug access policies;
#   2. the datatug binary built from this checkout (scripts/lib/typesense-cli-journey.sh),
#      so the proof includes a separate CLI process, not only a cobra call in the
#      test binary.
#
# The container is RAM/CPU/PID limited, is removed by this script's EXIT trap, and
# its failure never replaces the exit status of a failing journey. Nothing else on
# the machine is touched: no global daemon, no broad prune, no account or
# production data, no paid service. Docker is not needed for the default Go test
# run; it is required only when this journey is run on a dedicated Linux runner.
set -euo pipefail

cd "$(dirname "$0")/.."

image=${TYPESENSE_IMAGE:-typesense/typesense:30.2@sha256:610f2d34b1f93d00762869da2c67736775e5798d19a2c8b91b014b8a0cc1e110}
run=$$-$RANDOM
name=datatug-cli-typesense-$run
bootstrap=typesense-bootstrap-$run
work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-typesense.XXXXXX")
# container_id is the exact id docker create printed. created stays 0 until that
# create has returned, so a failed create never removes a container name this
# script did not own (a collision, or one left by another run).
container_id=
created=0

# shellcheck source=scripts/lib/typesense-fixture-lifecycle.sh
source scripts/lib/typesense-fixture-lifecycle.sh
typesense_lifecycle_install

if ! command -v docker >/dev/null; then
	echo "docker is required for the Typesense journey" >&2
	exit 1
fi
if ! docker info >/dev/null 2>&1; then
	echo "the docker daemon is not reachable" >&2
	exit 1
fi

echo "creating $image as $name"
container_id=$(docker create --name "$name" \
	--label "datatug.fixture=typesense-$run" \
	--memory 2g --memory-swap 2g --cpus 2 --pids-limit 256 \
	-p 127.0.0.1::8108 \
	"$image" \
	--data-dir=/data --api-key="$bootstrap" --listen-port=8108)
created=1
if [ -z "$container_id" ]; then
	echo "docker create returned no container id for the owned Typesense fixture" >&2
	exit 1
fi
echo "starting $container_id"
if ! docker start "$container_id" >/dev/null; then
	echo "the owned Typesense container failed to start" >&2
	docker logs "$container_id" >&2 || true
	exit 1
fi
port=$(docker port "$container_id" 8108/tcp | sed 's/.*://')
base=http://127.0.0.1:$port

# api sends one request with the fixture's bootstrap admin key. The key never
# leaves this process; the scoped keys created below are the only keys the
# journeys receive.
api() {
	curl -fsS --max-time 10 -H "X-TYPESENSE-API-KEY: $bootstrap" "$@"
}

# The health wait is one monotonic 120s wall-clock deadline, not 120 attempts
# that can each spend a 10s curl timeout. Every probe is bounded by the budget
# that remains, so the whole wait stays under the intended bound.
health_deadline=$((SECONDS + 120))
ready=0
while :; do
	remaining=$((health_deadline - SECONDS))
	if [ "$remaining" -le 0 ]; then
		break
	fi
	budget=10
	if [ "$remaining" -lt "$budget" ]; then
		budget=$remaining
	fi
	if curl -fsS --max-time "$budget" -H "X-TYPESENSE-API-KEY: $bootstrap" "$base/health" >"$work/health.json" 2>/dev/null &&
		grep -q '"ok":true' "$work/health.json"; then
		ready=1
		break
	fi
	sleep 1
done
if [ "$ready" != 1 ]; then
	echo "the Typesense server did not become healthy within 120s" >&2
	docker logs "$container_id" >&2 || true
	exit 1
fi
api "$base/debug" >"$work/debug.json"
grep -q '"version":"30\.2' "$work/debug.json" || {
	cat "$work/debug.json" >&2
	echo "the Typesense server is not the pinned 30.2 fixture" >&2
	exit 1
}
echo "server version $(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "$work/debug.json") on $base"

cat >"$work/books-schema.json" <<'JSON'
{
  "name": "books",
  "fields": [
    {"name": "title", "type": "string"},
    {"name": "author", "type": "string", "facet": true},
    {"name": "genre", "type": "string", "facet": true},
    {"name": "year", "type": "int32", "facet": true},
    {"name": "rank", "type": "int64"},
    {"name": "seq", "type": "int64", "sort": true},
    {"name": "big", "type": "int64"},
    {"name": "ratio", "type": "float"},
    {"name": "active", "type": "bool", "facet": true},
    {"name": "tags", "type": "string[]", "facet": true},
    {"name": "note", "type": "string", "optional": true}
  ],
  "default_sorting_field": "rank"
}
JSON
cat >"$work/semantics-schema.json" <<'JSON'
{
  "name": "semantics",
  "fields": [
    {"name": "label", "type": "string", "facet": true},
    {"name": "tags", "type": "string[]", "facet": true, "optional": true},
    {"name": "note", "type": "string", "optional": true},
    {"name": "n", "type": "float"},
    {"name": "m", "type": "int32", "sort": true}
  ]
}
JSON
cat >"$work/archive-schema.json" <<'JSON'
{"name":"archive","fields":[{"name":"title","type":"string"}]}
JSON

api -X POST -H 'Content-Type: application/json' --data-binary @"$work/books-schema.json" "$base/collections" >/dev/null
api -X POST -H 'Content-Type: application/json' --data-binary @"$work/semantics-schema.json" "$base/collections" >/dev/null
api -X POST -H 'Content-Type: application/json' --data-binary @"$work/archive-schema.json" "$base/collections" >/dev/null

cat >"$work/books.ndjson" <<'JSON'
{"id":"001","title":"Dune","author":"Frank Herbert","genre":"science fiction","year":1965,"rank":10,"seq":1,"big":9007199254740993,"ratio":1.5,"active":true,"tags":["classic","desert"]}
{"id":"002","title":"Dune Messiah","author":"Frank Herbert","genre":"science fiction","year":1969,"rank":10,"seq":2,"big":-9223372036854775808,"ratio":2.25,"active":false,"tags":["sequel"]}
{"id":"003","title":"Children of Dune","author":"Frank Herbert","genre":"science fiction","year":1976,"rank":20,"seq":3,"big":9223372036854775807,"ratio":3.5,"active":true,"tags":["sequel"],"note":"a b (c) && d"}
{"id":"004","title":"Neuromancer","author":"William Gibson","genre":"cyberpunk","year":1984,"rank":20,"seq":4,"big":0,"ratio":4.5,"active":true,"tags":["classic"]}
{"id":"005","title":"Foundation","author":"Isaac Asimov","genre":"science fiction","year":1951,"rank":30,"seq":5,"big":42,"ratio":1.0,"active":false,"tags":["classic"],"note":null}
{"id":"006","title":"Snow Crash","author":"Neal Stephenson","genre":"cyberpunk","year":1992,"rank":40,"seq":6,"big":84,"ratio":2.0,"active":true,"tags":["classic"]}
{"id":"007","title":"The Left Hand of Darkness","author":"Ursula K. Le Guin","genre":"science fiction","year":1969,"rank":50,"seq":7,"big":7,"ratio":0.5,"active":true,"tags":["classic"]}
{"id":"008","title":"Hyperion","author":"Dan Simmons","genre":"science fiction","year":1989,"rank":60,"seq":8,"big":8,"ratio":0.25,"active":true,"tags":["classic"]}
{"id":"009","title":"The Dispossessed","author":"Ursula K. Le Guin","genre":"science fiction","year":1974,"rank":70,"seq":9,"big":9,"ratio":0.125,"active":true,"tags":["classic"]}
{"id":"010","title":"Ender's Game","author":"Orson Scott Card","genre":"science fiction","year":1985,"rank":80,"seq":10,"big":10,"ratio":0.0625,"active":true,"tags":["classic"]}
{"id":"011","title":"The Stars My Destination","author":"Alfred Bester","genre":"science fiction","year":1956,"rank":90,"seq":11,"big":11,"ratio":0.03125,"active":true,"tags":["classic"]}
{"id":"012","title":"A Canticle for Leibowitz","author":"Walter M. Miller Jr.","genre":"science fiction","year":1959,"rank":100,"seq":12,"big":12,"ratio":0.015625,"active":true,"tags":["classic"]}
JSON
cat >"$work/semantics.ndjson" <<'JSON'
{"id":"f0","label":"classic","tags":["classic","desert"],"n":0.1,"m":4}
{"id":"f1","label":"Classic","tags":["sequel"],"n":0.1000000001,"m":6}
{"id":"f2","label":"CLASSIC","tags":["classic"],"n":0.10000000149,"m":7}
{"id":"f3","label":"red-blue","tags":["a_b"],"n":100000001,"m":9}
{"id":"f4","label":"redblue","tags":["ab"],"n":100000000,"m":10}
{"id":"f5","label":"café","n":1.5,"m":12}
{"id":"f6","label":"cafe","n":2.5,"m":13}
{"id":"f7","label":"a.b","tags":["ab"],"n":3.5,"m":14}
{"id":"f8","label":"ab","tags":["a_b"],"n":4.5,"m":15}
{"id":"f9","label":"hello world","n":5.5,"m":16}
{"id":"f10","label":"hello  world","n":6.5,"m":17}
JSON
cat >"$work/archive.ndjson" <<'JSON'
{"id":"a1","title":"Archive"}
JSON

import_documents() {
	local collection=$1 file=$2
	api -X POST -H 'Content-Type: text/plain' --data-binary @"$file" \
		"$base/collections/$collection/documents/import?action=upsert" >"$work/import-$collection.json"
	if grep -q '"success":false' "$work/import-$collection.json"; then
		cat "$work/import-$collection.json" >&2
		echo "seeding collection $collection failed" >&2
		exit 1
	fi
}
import_documents books "$work/books.ndjson"
import_documents semantics "$work/semantics.ndjson"
import_documents archive "$work/archive.ndjson"

# The fixture premise is the seeded count: a partial import must fail here, not
# show up later as a mysterious native found count.
api "$base/collections/books" >"$work/books-meta.json"
api "$base/collections/semantics" >"$work/semantics-meta.json"
python3 - "$work/books-meta.json" "$work/semantics-meta.json" <<'PY'
import json, sys
want = {sys.argv[1]: 12, sys.argv[2]: 11}
for path, count in want.items():
    documents = json.load(open(path)).get("num_documents")
    if documents != count:
        sys.exit("%s has %r documents, want %d" % (path, documents, count))
PY

# One key may only search; the other may also read collection metadata. Both are
# scoped to books and semantics, so neither can see the archive collection. The
# values are captured, never printed.
create_key() {
	local description=$1 actions=$2 collections=$3 response value
	response=$(api -X POST -H 'Content-Type: application/json' \
		--data-binary "{\"description\":\"$description\",\"actions\":$actions,\"collections\":$collections}" \
		"$base/keys")
	value=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"])')
	if [ -z "$value" ]; then
		echo "the fixture could not create the $description key" >&2
		exit 1
	fi
	printf '%s' "$value"
}
search_key=$(create_key "datatug search-only" '["documents:search"]' '["books","semantics"]')
metadata_key=$(create_key "datatug search-and-metadata" '["documents:search","collections:get","collections:list"]' '["books","semantics"]')
if [ -z "$search_key" ] || [ -z "$metadata_key" ]; then
	echo "the fixture created an empty scoped key" >&2
	exit 1
fi

export DATATUG_TEST_TYPESENSE_URL="typesense+http://127.0.0.1:$port/books"
export DATATUG_TEST_TYPESENSE_SEMANTICS_URL="typesense+http://127.0.0.1:$port/semantics"
export DATATUG_TEST_TYPESENSE_SEARCH_KEY="$search_key"
export DATATUG_TEST_TYPESENSE_METADATA_KEY="$metadata_key"
export DATATUG_TEST_TYPESENSE_REQUIRE_FIXTURE=1

echo "running the Go command journey"
test_log=$work/test.log
status=0
go test ./apps/datatugapp/commands/ \
	-run 'TestTypesenseQueryJourney|TestTypesenseSavedQueryAndPolicyJourney' -count=1 -v >"$test_log" 2>&1 || status=$?
if [ "$status" != 0 ]; then
	cat "$test_log"
	exit "$status"
fi
grep -q -- '--- PASS: TestTypesenseQueryJourney' "$test_log" || {
	cat "$test_log"
	echo "the Typesense query journey did not pass" >&2
	exit 1
}
grep -q -- '--- PASS: TestTypesenseSavedQueryAndPolicyJourney' "$test_log" || {
	cat "$test_log"
	echo "the Typesense saved-query journey did not pass" >&2
	exit 1
}
echo "Go command journey passed"

bash scripts/lib/typesense-cli-journey.sh

echo "typesense journey passed: books and semantics on $base with scoped search-only and search+metadata keys"
