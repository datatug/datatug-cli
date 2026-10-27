#!/usr/bin/env bash
# check-journey-postgrest.sh runs the PostgREST command journey against an owned,
# disposable PostgreSQL 17 + PostgREST 16.5 project.
#
# It starts the pinned pair as a unique Compose project on two random loopback
# ports, seeds native rows through PostgreSQL's init hook (never through the
# adapter or the CLI), creates least-privilege reader roles and mints short-lived
# read-only JWTs for them, then runs:
#   1. the Go command-path journey (apps/datatugapp/commands/cmd_query_postgrest_real_test.go),
#      which drives `query run`, saved-query resolution, access policies, the
#      generic schema reader and the failure paths against the real service;
#   2. the datatug binary built from this checkout, so at least one proof is a
#      separate CLI process rather than a cobra call in the test binary.
# The Compose project and its volume are removed when the script ends. That
# teardown failure is an error once the owned project has started, but it never
# replaces the exit status of a failing journey. It never uses another worker's
# container, database, port or Compose project.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD
compose_file=$root/testdata/postgrest/docker-compose.yml
project=datatug-cli-postgrest-$(date +%s)-$$
read -r postgres_port postgrest_port < <(python3 - <<'PY'
import socket

def free_port():
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port

print(free_port(), free_port())
PY
)
export POSTGRES_PORT=$postgres_port POSTGREST_PORT=$postgrest_port

compose=(docker compose -p "$project" -f "$compose_file")
work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-postgrest.XXXXXX")
# started stays 0 until this script's own Compose project has been created, so a
# launch failure never removes containers or a project name it did not create.
started=0
# teardown removes only this script's own Compose project and its volume; it
# returns non-zero when it could not, and never removes a project it did not
# start.
teardown() {
	status=0
	if [ "$started" = 1 ]; then
		"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1
		removed=$?
		if [ "$removed" != 0 ]; then
			echo "could not remove the owned Compose project $project (docker compose down exited $removed)" >&2
			status=1
		fi
	fi
	rm -rf "$work" >/dev/null 2>&1 || true
	return "$status"
}
# cleanup is the trap: it keeps the exit status of the journey, and a teardown
# failure turns a passing journey into a failing one.
cleanup() {
	status=$?
	set +e
	teardown
	torn=$?
	[ "$status" != 0 ] || status=$torn
	exit "$status"
}
trap cleanup EXIT INT TERM

echo "PostgREST fixture project: $project"
echo "PostgreSQL port: $postgres_port, PostgREST port: $postgrest_port"

started=1
"${compose[@]}" up -d

base=http://127.0.0.1:$postgrest_port
jar="$work/cookies.txt"
admin_auth=postgres
for attempt in $(seq 1 90); do
	if curl -fsS -o /dev/null "$base/" 2>/dev/null; then
		break
	fi
	if [ "$attempt" -eq 90 ]; then
		echo "PostgREST did not become ready" >&2
		"${compose[@]}" logs postgres postgrest >&2 || true
		exit 1
	fi
	sleep 1
done

server_header="$(curl -fsSI "$base/" | tr -d '\r' | awk 'tolower($1) == "server:" { print $2 }')"
case "$server_header" in
	postgrest/16.5*) ;;
	*)
		echo "unexpected PostgREST server header: ${server_header:-<missing>}" >&2
		exit 1
		;;
esac
echo "PostgREST server: $server_header"

psql() { "${compose[@]}" exec -T postgres psql -U "$admin_auth" -d appdb -tAc "$@"; }
item_count="$(psql 'select count(*) from app.items')"
label_count="$(psql 'select count(*) from app.labels')"
if [ "$item_count" != "6" ] || [ "$label_count" != "3" ]; then
	echo "expected 6 items and 3 labels externally seeded, found ${item_count} and ${label_count}" >&2
	exit 1
fi
can_insert="$(psql "select has_table_privilege('app_reader','app.items','INSERT')")"
can_select="$(psql "select has_table_privilege('app_reader','app.items','SELECT')")"
noaccess_select="$(psql "select has_table_privilege('noaccess','app.items','SELECT')")"
if [ "$can_insert" != "f" ] || [ "$can_select" != "t" ] || [ "$noaccess_select" != "f" ]; then
	echo "fixture role privileges are wrong: select=${can_select} insert=${can_insert} noaccess=${noaccess_select}" >&2
	exit 1
fi
echo "Externally seeded rows: items=${item_count}, labels=${label_count}; app_reader select=${can_select}, insert=${can_insert}, noaccess select=${noaccess_select}"

# Short-lived tokens for the two roles, signed with the fixture's own secret.
mint_token() {
	python3 - "$1" <<'PY'
import base64
import hashlib
import hmac
import json
import sys
import time

role = sys.argv[1]
secret = b"datatug-cli-journey-secret-that-is-long-enough"

def encode(value):
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode()

header = encode(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
payload = encode(
    json.dumps(
        {"role": role, "iat": int(time.time()) - 60, "exp": int(time.time()) + 3600},
        separators=(",", ":"),
    ).encode()
)
signature = encode(hmac.new(secret, f"{header}.{payload}".encode(), hashlib.sha256).digest())
print(f"{header}.{payload}.{signature}")
PY
}
reader_token="$(mint_token app_reader)"
noaccess_token="$(mint_token noaccess)"

source="postgrest+http://127.0.0.1:$postgrest_port?schema=app"
export DATATUG_TEST_POSTGREST_URL="$source"
export DATATUG_TEST_POSTGREST_BEARER="$reader_token"
export DATATUG_TEST_POSTGREST_NOACCESS_BEARER="$noaccess_token"

echo "running the Go command journey"
test_log=$work/test.log
status=0
go test ./apps/datatugapp/commands/ -run 'TestPostgRESTQueryJourney|TestPostgRESTSavedQueryAndPolicyJourney' -count=1 -v >"$test_log" 2>&1 || status=$?
if [ "$status" != 0 ]; then
	cat "$test_log"
	exit "$status"
fi
grep -q -- '--- PASS: TestPostgRESTQueryJourney' "$test_log" || { cat "$test_log"; echo "the PostgREST query journey did not pass" >&2; exit 1; }
grep -q -- '--- PASS: TestPostgRESTSavedQueryAndPolicyJourney' "$test_log" || { cat "$test_log"; echo "the saved-query journey did not pass" >&2; exit 1; }
echo "Go command journey passed"

echo "building datatug from this checkout"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .

# The CLI process uses the real credential variables, not the test ones.
export DATATUG_POSTGREST_BEARER="$reader_token"
export DATATUG_TEST_POSTGREST_ITEMS="$source"

project_dir="$work/project"
mkdir -p "$project_dir/environments/local/catalogs/items" "$project_dir/connections/local" "$project_dir/queries/items" "$project_dir/policies"
cat >"$project_dir/datatug-project.json" <<JSON
{"id":"postgrest-project","title":"PostgREST project","access":"private"}
JSON
cat >"$project_dir/environments/local/local.env.json" <<JSON
{"id":"local","dbServers":[{"driver":"postgrest","catalogs":["items"]}]}
JSON
cat >"$project_dir/connections/local/items.json" <<JSON
{"dsnEnv":"DATATUG_TEST_POSTGREST_ITEMS"}
JSON
cat >"$project_dir/environments/local/catalogs/items/items.db.json" <<JSON
{"driver":"postgrest","path":"connections/local/items.json","dbModel":"items"}
JSON
cat >"$project_dir/queries/items/top.query.json" <<'JSON'
{"id":"items/top","title":"Top items","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"items"}]}
JSON
cat >"$project_dir/queries/items/top.query.tugql.yaml" <<'YAML'
from: {name: items}
columns:
  - {field: title}
  - {field: qty}
  - {field: owner}
where:
  op: '>='
  left: {field: qty}
  right: {value: 10}
orderBy:
  - {field: qty, desc: true}
limit: 3
YAML
cat >"$project_dir/policies/global.yaml" <<'YAML'
apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: postgrest
default: deny
scopes:
  - path: /items
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: owner}
          right: {param: currentUser}
        fields: [title, qty, owner]
YAML

"$work/datatug" query run --project "$project_dir" --query items/top --env local --as alice --format json >"$work/saved.json" 2>"$work/saved.err" || {
	cat "$work/saved.err" >&2
	echo "the built CLI could not run the saved PostgREST query through its connection descriptor" >&2
	exit 1
}
saved_rows=$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$work/saved.json")
[ "$saved_rows" = 3 ] || { cat "$work/saved.json"; echo "the saved CLI query returned $saved_rows rows, want 3" >&2; exit 1; }
saved_owners=$(python3 -c 'import json,sys; print(sorted({r["owner"] for r in json.load(open(sys.argv[1]))}))' "$work/saved.json")
[ "$saved_owners" = "['alice']" ] || { cat "$work/saved.json"; echo "the saved CLI query returned owners $saved_owners, want alice only" >&2; exit 1; }
if grep -rq -e "$reader_token" -e "127.0.0.1:$postgrest_port" "$project_dir"; then
	echo "the saved project holds a credential or the endpoint" >&2
	exit 1
fi
[ "$(cat "$project_dir/environments/local/catalogs/items/items.db.json")" = '{"driver":"postgrest","path":"connections/local/items.json","dbModel":"items"}' ] || { cat "$project_dir/environments/local/catalogs/items/items.db.json" >&2; echo "the catalog does not record the descriptor" >&2; exit 1; }

"$work/datatug" query run --db "$source" --from items --format json --no-policies >"$work/from.json" 2>"$work/from.err" || {
	cat "$work/from.err" >&2
	echo "the built CLI could not read a default-bounded PostgREST relation" >&2
	exit 1
}
from_rows=$(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))))' "$work/from.json")
[ "$from_rows" = 6 ] || { cat "$work/from.json"; echo "the default-bounded --from read returned $from_rows rows, want 6" >&2; exit 1; }
grep -q 'reading at most 100 rows (PostgREST exploration default' "$work/from.err" || { cat "$work/from.err"; echo "a default-bounded --from read did not state its bound" >&2; exit 1; }

# Five rows matching qty >= 10, four of them sharing qty 20, while the server
# caps one physical page at two rows: the rows can only come back if every page
# is continued and the id tie-breaker is applied.
cat >"$work/five.yaml" <<'YAML'
from: {name: items}
columns:
  - {field: title}
  - {field: qty}
  - {field: owner}
where:
  op: '>='
  left: {field: qty}
  right: {value: 10}
orderBy:
  - {field: qty}
limit: 5
YAML
"$work/datatug" query run --db "$source" -f "$work/five.yaml" --format json --no-policies >"$work/five.json" 2>"$work/five.err" || {
	cat "$work/five.err" >&2
	echo "the built CLI could not read five rows across server-capped pages" >&2
	exit 1
}
if ! python3 - "$work/five.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 5:
    sys.exit("rows=%d, want 5 across server-capped pages: %r" % (len(rows), rows))
titles = [row["title"] for row in rows]
if titles != ["alpha", "beta", "gamma", "delta", "epsilon"]:
    sys.exit("titles=%r, want qty then id order" % (titles,))
if [row["qty"] for row in rows] != [10, 20, 20, 20, 20]:
    sys.exit("qty values=%r, want duplicate 20s kept in id order" % ([row["qty"] for row in rows],))
for row in rows:
    if "id" in row or "secret" in row:
        sys.exit("an undeclared field was returned: %r" % (row,))
PY
then
	cat "$work/five.json" >&2
	echo "the bounded multi-page read did not return the expected native rows" >&2
	exit 1
fi

# An unbounded read and an aggregate are refused explicitly: never a partial
# result from a hidden page cap.
set +e
"$work/datatug" query run --db "$source" --from items --limit 0 --format json --no-policies >"$work/unbounded.out" 2>"$work/unbounded.err"
unbounded_status=$?
set -e
[ "$unbounded_status" = 4 ] || { cat "$work/unbounded.err" >&2; echo "an unbounded PostgREST read exited $unbounded_status, want 4" >&2; exit 1; }
[ ! -s "$work/unbounded.out" ] || { cat "$work/unbounded.out"; echo "a refused unbounded read printed rows" >&2; exit 1; }
grep -q 'positive limit' "$work/unbounded.err" || { cat "$work/unbounded.err"; echo "the unbounded refusal did not explain the positive limit" >&2; exit 1; }

cat >"$work/aggregate.yaml" <<'YAML'
from: {name: items}
columns:
  - {field: qty}
  - {aggregate: {function: count, args: [{star: true}]}, as: total}
groupBy: [{field: qty}]
limit: 3
YAML
set +e
"$work/datatug" query run --db "$source" -f "$work/aggregate.yaml" --format json --no-policies >"$work/aggregate.out" 2>"$work/aggregate.err"
aggregate_status=$?
set -e
[ "$aggregate_status" = 4 ] || { cat "$work/aggregate.err" >&2; echo "a PostgREST aggregate exited $aggregate_status, want 4" >&2; exit 1; }
[ ! -s "$work/aggregate.out" ] || { cat "$work/aggregate.out"; echo "a refused aggregate printed a partial result" >&2; exit 1; }
grep -q 'not supported' "$work/aggregate.err" || { cat "$work/aggregate.err"; echo "the aggregate refusal did not say the operation is unsupported" >&2; exit 1; }

# A bad token reaches the service and comes back as its own refusal, with
# neither the token nor the endpoint in the message.
set +e
DATATUG_POSTGREST_BEARER="not-a-valid-jwt" "$work/datatug" query run --db "$source" --from items --limit 1 --format json --no-policies >"$work/badauth.out" 2>"$work/badauth.err"
badauth_status=$?
set -e
[ "$badauth_status" = 4 ] || { cat "$work/badauth.err" >&2; echo "a bad PostgREST token exited $badauth_status, want 4" >&2; exit 1; }
[ ! -s "$work/badauth.out" ] || { cat "$work/badauth.out"; echo "a refused read printed rows" >&2; exit 1; }
grep -q 'HTTP 401' "$work/badauth.err" || { cat "$work/badauth.err"; echo "the bad-token refusal did not report HTTP 401" >&2; exit 1; }
if grep -q -e "not-a-valid-jwt" -e "127.0.0.1:$postgrest_port" "$work/badauth.err"; then
	echo "the bad-token refusal leaked the token or the endpoint" >&2
	exit 1
fi

# The same schema reader the saved-query path uses also feeds a scan: the project
# records only a descriptor naming the operator's variable.
scanned="$work/scanned"
"$work/datatug" scan -d "$scanned" -D postgrest --dsn-env DATATUG_TEST_POSTGREST_ITEMS --db items --env local >"$work/scan.out" 2>"$work/scan.err" || {
	cat "$work/scan.err" >&2
	echo "the built CLI could not scan the PostgREST API root" >&2
	exit 1
}
python3 - "$scanned/environments/local/catalogs/items/items.db.json" "$scanned/connections/local/items.json" <<'PY'
import json, sys
catalog = json.load(open(sys.argv[1]))
if catalog.get("driver") != "postgrest" or catalog.get("path") != "connections/local/items.json" or catalog.get("dbModel") != "items":
    sys.exit("the scan did not record the descriptor: %r" % (catalog,))
descriptor = json.load(open(sys.argv[2]))
if descriptor != {"dsnEnv": "DATATUG_TEST_POSTGREST_ITEMS"}:
    sys.exit("the descriptor does not name only the variable: %r" % (descriptor,))
PY
if grep -rq -e "$reader_token" -e "127.0.0.1:$postgrest_port" "$scanned"; then
	echo "the scanned project holds a credential or the endpoint" >&2
	exit 1
fi
columns_file="$scanned/dbmodels/items/default/tables/items/default.items.columns.json"
[ -f "$columns_file" ] || { find "$scanned" -type f >&2; echo "the scan records no items columns" >&2; exit 1; }
python3 - "$columns_file" <<'PY'
import json, sys
columns = {column["name"] for column in json.load(open(sys.argv[1]))["columns"]}
missing = {"id", "title", "qty", "owner"} - columns
if missing:
    sys.exit("the scan records no columns %s: %r" % (sorted(missing), sorted(columns)))
PY

echo "postgrest journey passed: $project on $base"

trap - EXIT INT TERM
if ! teardown; then
	exit 1
fi
if docker ps -aq --filter "label=com.docker.compose.project=${project}" | grep -q .; then
	echo "teardown left containers for project ${project}" >&2
	exit 1
fi
echo "Teardown complete: no containers remain for project ${project}"
