#!/usr/bin/env bash
# libsql-cli-journey.sh runs INSIDE the published dalgo2libsql fixture process:
# LIBSQL_FIXTURE_URL and LIBSQL_RO_TOKEN are set by scripts/libsql-fixture.mjs for
# its child. The writer token never leaves the fixture process; this script maps
# the reader token to the CLI's own variable and never prints it.
#
# It drives the Go command journey, then a datatug binary built from this
# checkout, so the provider proof includes a separate CLI process.
set -euo pipefail

cd "$(dirname "$0")/../.."
root=$PWD

if [ -z "${LIBSQL_FIXTURE_URL:-}" ] || [ -z "${LIBSQL_RO_TOKEN:-}" ]; then
	echo "this script runs only as the child of scripts/libsql-fixture.mjs" >&2
	exit 1
fi
case "$LIBSQL_FIXTURE_URL" in
	http://127.0.0.1:*) ;;
	*)
		echo "the fixture URL is not a loopback HTTP origin" >&2
		exit 1
		;;
esac

# Map the fixture's names to the CLI's own. The endpoint is loopback-only. The
# token is copied, never echoed, and the fixture copies are dropped.
export DATATUG_LIBSQL_BEARER="$LIBSQL_RO_TOKEN"
export DATATUG_TEST_LIBSQL_URL="libsql+${LIBSQL_FIXTURE_URL}"
unset LIBSQL_RO_TOKEN LIBSQL_FIXTURE_URL LIBSQL_REQUIRE_FIXTURE

echo "running the Go command journey"
go test ./apps/datatugapp/commands/ \
	-run 'TestLibSQLQueryJourney|TestLibSQLSavedQueryAndPolicyJourney' -count=1 -v 2>&1 | tee "$root/.libsql-go-journey.log"
grep -q -- '--- PASS: TestLibSQLQueryJourney' "$root/.libsql-go-journey.log" || {
	echo "the libSQL query journey did not pass" >&2
	exit 1
}
grep -q -- '--- PASS: TestLibSQLSavedQueryAndPolicyJourney' "$root/.libsql-go-journey.log" || {
	echo "the saved-query journey did not pass" >&2
	exit 1
}
rm -f "$root/.libsql-go-journey.log"
echo "Go command journey passed"

work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-libsql.XXXXXX")
cleanup() {
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

echo "building datatug from this checkout"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .
source=$DATATUG_TEST_LIBSQL_URL

# An exploratory --from read states its visible default bound, and an explicit
# --offset pages by the declared integer primary key, not by an invented rowid.
"$work/datatug" query run --db "$source" --from items --format json --no-policies >"$work/from.json" 2>"$work/from.err"
python3 - "$work/from.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 12:
    sys.exit("default-bounded --from returned %d rows, want 12" % len(rows))
if [row["$key"] for row in rows] != [str(i) for i in range(1, 13)]:
    sys.exit("rows are not in integer primary-key order: %r" % [row["$key"] for row in rows])
PY
grep -q 'reading at most 100 rows (libSQL exploration default' "$work/from.err" || {
	cat "$work/from.err" >&2
	echo "a default-bounded --from read did not state its bound" >&2
	exit 1
}

"$work/datatug" query run --db "$source" --from items --limit 3 --offset 2 --format json --no-policies >"$work/page.json" 2>"$work/page.err"
python3 - "$work/page.json" <<'PY'
import json, sys
keys = [row["$key"] for row in json.load(open(sys.argv[1]))]
if keys != ["3", "4", "5"]:
    sys.exit("limit+offset keys = %r, want 3,4,5" % (keys,))
PY

# Projection and filter: undeclared columns never reach the result.
cat >"$work/filter.yaml" <<'YAML'
from: {name: items}
columns:
  - {field: title}
  - {field: rank}
  - {field: score}
where:
  op: '>='
  left: {field: rank}
  right: {value: 2}
orderBy:
  - {field: rank}
  - {field: id}
limit: 4
YAML
"$work/datatug" query run --db "$source" -f "$work/filter.yaml" --format json --no-policies >"$work/filter.json" 2>"$work/filter.err"
python3 - "$work/filter.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 4:
    sys.exit("filtered rows = %d, want 4: %r" % (len(rows), rows))
for row in rows:
    if set(row) - {"$key", "title", "rank", "score"}:
        sys.exit("an undeclared column was returned: %r" % (row,))
PY

# Native values: full int64 digits beyond 2^53, blobs/null/empty blob, and more
# than one storage class in one column.
"$work/datatug" query run --db "$source" --from bigints --format json --no-policies >"$work/bigints.json" 2>"$work/bigints.err"
for exact in 9223372036854775807 -9223372036854775808 9007199254740993; do
	grep -q -- "$exact" "$work/bigints.json" || {
		cat "$work/bigints.json" >&2
		echo "the exact int64 $exact was lost" >&2
		exit 1
	}
done
"$work/datatug" query run --db "$source" --from blobs --format json --no-policies >"$work/blobs.json" 2>"$work/blobs.err"
python3 - "$work/blobs.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if [row["payload"] for row in rows] != ["AP8Q", None, ""]:
    sys.exit("blob/null/empty-blob values = %r (blobs are base64 in JSON)" % [row["payload"] for row in rows])
PY
"$work/datatug" query run --db "$source" --from mixed --format json --no-policies >"$work/mixed.json" 2>"$work/mixed.err"
python3 - "$work/mixed.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
kinds = {type(row["declared_integer"]).__name__ for row in rows}
if not {"str", "float", "NoneType"} <= kinds:
    sys.exit("one column did not keep its mixed storage classes: %r" % (kinds,))
PY

# A native aggregate is compiled to SQL, never to a local scan.
cat >"$work/aggregate.yaml" <<'YAML'
from: {name: items}
columns:
  - {field: rank}
  - {aggregate: {function: count, args: [{star: true}]}, as: total}
  - {aggregate: {function: max, args: [{field: id}]}, as: last_id}
where:
  op: '>='
  left: {field: rank}
  right: {value: 1}
groupBy: [{field: rank}]
orderBy: [{field: rank}]
limit: 10
YAML
"$work/datatug" query run --db "$source" -f "$work/aggregate.yaml" --format json --no-policies >"$work/aggregate.json" 2>"$work/aggregate.err"
python3 - "$work/aggregate.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 3:
    sys.exit("aggregate rows = %d, want 3 ranks: %r" % (len(rows), rows))
if rows[0]["total"] != 3 or rows[0]["last_id"] != 10:
    sys.exit("rank-1 aggregate = %r" % (rows[0],))
PY

# A write is refused before I/O.
: >"$work/empty.db"
set +e
"$work/datatug" db copy --from "sqlite://$work/empty.db" --to "$source" >"$work/write.out" 2>"$work/write.err"
write_status=$?
set -e
[ "$write_status" != 0 ] || { echo "a copy into libSQL succeeded" >&2; exit 1; }
grep -qi 'read-only' "$work/write.err" || { cat "$work/write.err" >&2; echo "the write refusal did not say read-only" >&2; exit 1; }

# A bad token is refused by the server, and neither it nor the endpoint reaches
# the message.
set +e
DATATUG_LIBSQL_BEARER="not-a-valid-token" "$work/datatug" query run --db "$source" --from items --limit 1 --format json --no-policies >"$work/badauth.out" 2>"$work/badauth.err"
badauth_status=$?
set -e
[ "$badauth_status" != 0 ] || { echo "a bad token was accepted" >&2; exit 1; }
[ ! -s "$work/badauth.out" ] || { cat "$work/badauth.out" >&2; echo "a refused read printed rows" >&2; exit 1; }
if grep -q -e 'not-a-valid-token' -e '127.0.0.1' "$work/badauth.err"; then
	cat "$work/badauth.err" >&2
	echo "the bad-token refusal leaked the token or the endpoint" >&2
	exit 1
fi

# Unsupported native SQL fails honestly rather than through a SELECT-prefix
# pseudo-parser: no rows, a database error, and no claim of a partial result.
set +e
"$work/datatug" query run --db "$source" --sql 'select * from items' --format json --no-policies >"$work/sql.out" 2>"$work/sql.err"
sql_status=$?
set -e
[ "$sql_status" != 0 ] || { echo "a native SQL read succeeded" >&2; exit 1; }
[ ! -s "$work/sql.out" ] || { cat "$work/sql.out" >&2; echo "a refused native SQL read printed rows" >&2; exit 1; }
grep -qi 'native SQL is not supported' "$work/sql.err" || { cat "$work/sql.err" >&2; echo "the native SQL refusal was not explicit" >&2; exit 1; }

# Cancellation: hold the first request open so the read is definitely in flight,
# then interrupt the CLI. A cancelled read is not a successful partial result.
# Python owns the plain TCP listener and the child process: the listener ACKs
# only after it has read the complete request headers (and the declared body),
# the parent then signals that exact child PID, and the listener observes the
# client disconnect. It sends no protocol data and forwards nothing, so no
# adapter protocol is copied here. Every wait is bounded and each resource is
# closed exactly once; a timeout kill is a failure, never a cancellation pass.
if ! python3 - "$work/datatug" <<'PY'
import signal
import socket
import subprocess
import sys
import threading

cli = sys.argv[1]
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0))
srv.listen(1)
srv.settimeout(20)
request_ready = threading.Event()
client_gone = threading.Event()
server_failure = []


def serve():
    conn = None
    try:
        conn, _ = srv.accept()
        conn.settimeout(0.2)
        request = b""
        while b"\r\n\r\n" not in request:
            chunk = conn.recv(4096)
            if not chunk:
                raise RuntimeError("the client closed before sending complete request headers")
            request += chunk
        head, _, body = request.partition(b"\r\n\r\n")
        length = 0
        for line in head.split(b"\r\n")[1:]:
            name, _, value = line.partition(b":")
            if name.strip().lower() == b"content-length":
                try:
                    length = int(value.strip())
                except ValueError:
                    length = 0
        while len(body) < length:
            chunk = conn.recv(4096)
            if not chunk:
                break
            body += chunk
        # The ACK: the full headers, and the declared body bytes, are in.
        request_ready.set()
        while True:
            try:
                chunk = conn.recv(4096)
            except socket.timeout:
                continue
            if not chunk:
                client_gone.set()
                return
    except Exception as exc:  # noqa: BLE001 - reported as a test failure
        server_failure.append(repr(exc))
        request_ready.set()
    finally:
        if conn is not None:
            try:
                conn.close()
            except OSError:
                pass


thread = threading.Thread(target=serve, daemon=True)
thread.start()
port = srv.getsockname()[1]
proc = subprocess.Popen(
    [cli, "query", "run", "--db", "libsql+http://127.0.0.1:%d" % port,
     "--from", "items", "--format", "json", "--no-policies"],
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
)

status = 1
try:
    if not request_ready.wait(20):
        raise RuntimeError("the CLI sent no complete request within 20s")
    if server_failure:
        raise RuntimeError("the cancellation listener failed: " + server_failure[0])

    # Signal the exact child, never a process group and never a guessed PID.
    proc.send_signal(signal.SIGINT)
    try:
        out, err = proc.communicate(timeout=20)
    except subprocess.TimeoutExpired:
        proc.kill()
        out, err = proc.communicate()
        raise RuntimeError("the CLI ignored cancellation and had to be killed")

    if proc.returncode == 0:
        raise RuntimeError("a cancelled read reported success")
    if out.strip():
        raise RuntimeError("a cancelled read printed rows: %r" % out[:200])
    if "cancel" not in err.lower():
        raise RuntimeError("the cancellation was not reported safely: %r" % err[:200])
    if not client_gone.wait(10):
        raise RuntimeError("the server did not observe the client disconnect after cancellation")
    print("cancellation: complete request held, exact PID interrupted, client disconnected, no rows")
    status = 0
finally:
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
    try:
        srv.close()
    except OSError:
        pass
    thread.join(timeout=5)

sys.exit(status)
PY
then
	echo "the cancellation check failed" >&2
	exit 1
fi

# The same generic schema reader a saved query uses also feeds a scan: the
# project records only a descriptor naming the operator's variable.
scanned="$work/scanned"
"$work/datatug" scan -d "$scanned" -D libsql --dsn-env DATATUG_TEST_LIBSQL_URL --db main --env local >"$work/scan.out" 2>"$work/scan.err" || {
	cat "$work/scan.err" >&2
	echo "the built CLI could not scan the libSQL database" >&2
	exit 1
}
python3 - "$scanned/environments/local/catalogs/main/main.db.json" "$scanned/connections/local/main.json" <<'PY'
import json, sys
catalog = json.load(open(sys.argv[1]))
if catalog.get("driver") != "libsql" or catalog.get("path") != "connections/local/main.json":
    sys.exit("the scan did not record the descriptor: %r" % (catalog,))
if "host" in catalog:
    sys.exit("the catalog records a host: %r" % (catalog,))
descriptor = json.load(open(sys.argv[2]))
if descriptor != {"dsnEnv": "DATATUG_TEST_LIBSQL_URL"}:
    sys.exit("the descriptor does not name only the variable: %r" % (descriptor,))
PY
columns_file="$scanned/dbmodels/main/main/tables/items/main.items.columns.json"
[ -f "$columns_file" ] || { find "$scanned" -type f >&2; echo "the scan records no items columns under main" >&2; exit 1; }
[ ! -e "$scanned/dbmodels/main/default" ] || { find "$scanned/dbmodels" -type f >&2; echo "the scan persisted the fixed default schema instead of SQLite's main" >&2; exit 1; }
python3 - "$columns_file" <<'PY'
import json, sys
columns = {column["name"] for column in json.load(open(sys.argv[1]))["columns"]}
missing = {"id", "title", "rank", "score", "done", "code", "payload"} - columns
if missing:
    sys.exit("the scan records no columns %s" % sorted(missing))
PY
if grep -rq -e "$DATATUG_LIBSQL_BEARER" -e '127.0.0.1' "$scanned" "$work/from.err"; then
	echo "a scanned project or a message holds the token or the endpoint" >&2
	exit 1
fi
echo "scan recorded the descriptor and native SQLite columns"

# The scanned project must serve a normal saved query: the source descriptor,
# catalog and table metadata all came from the scan. The query uses TugQL's
# supported schema qualifier, "main", for the scanned SQLite relation
# ("main.items" is a relation id, not a TugQL name: `name: main.items` would ask
# SQLite for a table literally named "main.items"). The bare /items policy must
# match the same relation.
mkdir -p "$scanned/queries/items" "$scanned/policies"
cat >"$scanned/queries/items/scanned-items.query.json" <<'JSON'
{"id":"items/scanned-items","title":"Scanned items","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"main"}]}
JSON
cat >"$scanned/queries/items/scanned-items.query.tugql.yaml" <<'YAML'
from: {schema: main, name: items}
columns:
  - {field: title}
  - {field: rank}
  - {field: code}
orderBy:
  - {field: rank}
  - {field: code}
limit: 10
YAML
cat >"$scanned/policies/global.yaml" <<'YAML'
apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: libsql-scanned
default: deny
scopes:
  - path: /items
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: code}
          right: {param: currentUser}
        fields: [title, rank, code]
YAML
"$work/datatug" query run --project "$scanned" --query items/scanned-items --env local --as a --format json >"$work/scanned-query.json" 2>"$work/scanned-query.err" || {
	cat "$work/scanned-query.err" >&2
	echo "the built CLI could not query the scanned main.items relation through a bare /items policy" >&2
	exit 1
}
python3 - "$work/scanned-query.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 1:
    sys.exit("the scanned-reference query returned %d rows, want exactly the one allowed row: %r" % (len(rows), rows))
row = rows[0]
if row.get("code") != "a" or row.get("title") != "Alpha":
    sys.exit("the scanned-reference query returned the wrong row: %r" % (row,))
if set(row) - {"$key", "title", "rank", "code"}:
    sys.exit("a denied row or column reached the scanned-reference result: %r" % (row,))
PY
echo "scanned project query matched the bare /items policy and withheld denied rows and columns"

# Saved query through the descriptor, with row and column policies enforced.
project_dir="$work/project"
mkdir -p "$project_dir/environments/local/catalogs/main" "$project_dir/connections/local" "$project_dir/queries/items" "$project_dir/policies"
cat >"$project_dir/datatug-project.json" <<'JSON'
{"id":"libsql-project","title":"libSQL project","access":"private"}
JSON
cat >"$project_dir/environments/local/local.env.json" <<'JSON'
{"id":"local","dbServers":[{"driver":"libsql","catalogs":["main"]}]}
JSON
cat >"$project_dir/connections/local/main.json" <<'JSON'
{"dsnEnv":"DATATUG_TEST_LIBSQL_URL"}
JSON
cat >"$project_dir/environments/local/catalogs/main/main.db.json" <<'JSON'
{"driver":"libsql","path":"connections/local/main.json","dbModel":"main"}
JSON
cat >"$project_dir/queries/items/by-rank.query.json" <<'JSON'
{"id":"items/by-rank","title":"Items by rank","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"main"}]}
JSON
cat >"$project_dir/queries/items/by-rank.query.tugql.yaml" <<'YAML'
from: {name: items}
columns:
  - {field: title}
  - {field: rank}
  - {field: code}
where:
  op: '>='
  left: {field: rank}
  right: {value: 2}
orderBy:
  - {field: rank}
  - {field: code}
limit: 4
YAML
cat >"$project_dir/policies/global.yaml" <<'YAML'
apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: libsql
default: deny
scopes:
  - path: /items
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: code}
          right: {param: currentUser}
        fields: [title, rank, code]
YAML

"$work/datatug" query run --project "$project_dir" --query items/by-rank --env local --as a --format json >"$work/saved.json" 2>"$work/saved.err" || {
	cat "$work/saved.err" >&2
	echo "the built CLI could not run the saved libSQL query through its descriptor" >&2
	exit 1
}
python3 - "$work/saved.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 1:
    sys.exit("the saved query returned %d rows, want the one owned row: %r" % (len(rows), rows))
row = rows[0]
if row.get("code") != "a" or row.get("title") != "Alpha":
    sys.exit("the saved policy returned the wrong row: %r" % (row,))
if set(row) - {"$key", "title", "rank", "code"}:
    sys.exit("a denied row or column reached the result: %r" % (row,))
PY
if grep -rq -e "$DATATUG_LIBSQL_BEARER" -e '127.0.0.1' "$project_dir"; then
	echo "the saved project holds the token or the endpoint" >&2
	exit 1
fi
echo "saved descriptor and row/column policies passed"

echo "libsql journey passed: owned disposable libSQL server, reader token only"
