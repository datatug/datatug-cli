#!/usr/bin/env bash
# typesense-cli-journey.sh runs INSIDE scripts/check-journey-typesense.sh: the
# fixture exports a loopback Typesense source and two scoped keys. This script
# maps them to the CLI's own variables and never prints a key.
#
# It builds datatug from this checkout and proves, in a separate CLI process:
# structured --from paging, native search from a file and stdin, search-only and
# metadata key roles, an env-only scan descriptor, a saved query with real
# DataTug policies, an invalid-key refusal that leaks nothing, an unsupported
# read refused before I/O, and a bounded cancellation.
set -euo pipefail

cd "$(dirname "$0")/../.."

: "${DATATUG_TEST_TYPESENSE_URL:?this script runs only from scripts/check-journey-typesense.sh}"
: "${DATATUG_TEST_TYPESENSE_SEARCH_KEY:?this script runs only from scripts/check-journey-typesense.sh}"
: "${DATATUG_TEST_TYPESENSE_METADATA_KEY:?this script runs only from scripts/check-journey-typesense.sh}"
: "${DATATUG_TEST_TYPESENSE_SEMANTICS_URL:?this script runs only from scripts/check-journey-typesense.sh}"
case "$DATATUG_TEST_TYPESENSE_URL" in
	typesense+http://127.0.0.1:*)
		;;
	*)
		echo "the fixture URL is not a loopback Typesense source" >&2
		exit 1
		;;
esac

source=$DATATUG_TEST_TYPESENSE_URL
semantics_source=$DATATUG_TEST_TYPESENSE_SEMANTICS_URL
search_key=$DATATUG_TEST_TYPESENSE_SEARCH_KEY
metadata_key=$DATATUG_TEST_TYPESENSE_METADATA_KEY
case "$source" in
	*/books)
		;;
	*)
		echo "the fixture URL must name the owned books collection" >&2
		exit 1
		;;
esac
archive_source=${source%/books}/archive
export DATATUG_TYPESENSE_API_KEY="$search_key"

work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-typesense-cli.XXXXXX")
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "building datatug from this checkout"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .

# Structured --from: an explicit positive limit and offset page the engine's own
# relevance order (the books schema's default_sorting_field is rank, descending).
"$work/datatug" query run --db "$source" --from books --limit 3 --offset 2 --format json --no-policies >"$work/from.json" 2>"$work/from.err" || {
	cat "$work/from.err" >&2
	echo "the built CLI could not read a bounded Typesense page" >&2
	exit 1
}
python3 - "$work/from.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 3:
    sys.exit("rows = %d, want 3: %r" % (len(rows), rows))
keys = [row["$key"] for row in rows]
if keys != ["010", "009", "008"]:
    sys.exit("limit+offset keys = %r, want 010,009,008 in native rank order" % (keys,))
if rows[0].get("title") != "Ender's Game":
    sys.exit("first row = %r" % (rows[0],))
PY
echo "structured --from returned the requested native page"

cat >"$work/search.json" <<'JSON'
{"collection":"books","q":"*","filterBy":"genre:=science fiction","sortBy":[{"field":"seq"}],"facetBy":["genre","year"],"maxFacetValues":10,"limit":5}
JSON

"$work/datatug" query typesense --db "$source" --request "$work/search.json" >"$work/native-file.json" 2>"$work/native-file.err" || {
	cat "$work/native-file.err" >&2
	echo "the built CLI could not run the native Typesense search from a file" >&2
	exit 1
}
grep -qF -- 'access: native Typesense search' "$work/native-file.err" || {
	cat "$work/native-file.err" >&2
	echo "the native command did not state the scoped-ACL boundary" >&2
	exit 1
}
grep -qF -- 'not applied' "$work/native-file.err" || {
	cat "$work/native-file.err" >&2
	echo "the native command did not say DataTug policies are not applied" >&2
	exit 1
}
python3 - "$work/native-file.json" <<'PY'
import json, sys
result = json.load(open(sys.argv[1]))
if result.get("collection") != "books":
    sys.exit("collection = %r" % (result.get("collection"),))
if (result.get("found"), result.get("outOf"), result.get("returned")) != (10, 12, 5):
    sys.exit("native found/outOf/returned = %r, want 10/12/5" % ((result.get("found"), result.get("outOf"), result.get("returned")),))
if result.get("partial") or result.get("complete") or not result.get("truncated"):
    sys.exit("truncation flags = partial %r, complete %r, truncated %r; want a truncated but not partial result"
             % (result.get("partial"), result.get("complete"), result.get("truncated")))
if result.get("provenance") != "typesense-native-search":
    sys.exit("provenance = %r" % (result.get("provenance"),))
hits = result.get("hits") or []
ids = [hit.get("id") for hit in hits]
if ids != ["001", "002", "003", "005", "007"]:
    sys.exit("hit ids = %r, want the native seq order and the preserved string id 001" % (ids,))
first = hits[0]["document"]
if first.get("big") != 9007199254740993:
    sys.exit("big = %r, want the exact integer above 2^53" % (first.get("big"),))
if first.get("tags") != ["classic", "desert"]:
    sys.exit("tags = %r, want the native array" % (first.get("tags"),))
if hits[2]["document"].get("note") != "a b (c) && d":
    sys.exit("a stored string note did not survive: %r" % (hits[2]["document"].get("note"),))
# Typesense keeps an optional string value but may omit an optional field whose
# stored value was null. Assert the supported semantics: never invent a non-null
# value, and accept either an omitted field or an explicit null.
if "note" in hits[3]["document"] and hits[3]["document"]["note"] is not None:
    sys.exit("an optional field stored as null came back as %r" % (hits[3]["document"].get("note"),))
facets = {facet["field"]: facet for facet in result.get("facets") or []}
genre = facets.get("genre") or {}
counts = {count["value"]: count["count"] for count in genre.get("counts") or []}
if counts.get("science fiction") != 10:
    sys.exit("genre facet = %r, want 10 science fiction hits" % (counts,))
year = facets.get("year") or {}
stats = year.get("stats") or {}
if int(stats.get("min", -1)) != 1951 or int(stats.get("max", -1)) != 1989:
    sys.exit("year facet stats = %r, want min 1951 and max 1989" % (stats,))
PY
echo "native file request kept native counts, ids, arrays, big integers and facets (a null optional field may be omitted)"

# The same document on stdin is the documented pipe form.
"$work/datatug" query typesense --db "$source" --request - <"$work/search.json" >"$work/native-stdin.json" 2>"$work/native-stdin.err" || {
	cat "$work/native-stdin.err" >&2
	echo "the built CLI could not run the native Typesense search from stdin" >&2
	exit 1
}
python3 - "$work/native-file.json" "$work/native-stdin.json" <<'PY'
import json, sys
file_result = json.load(open(sys.argv[1]))
stdin_result = json.load(open(sys.argv[2]))
for key in ("collection", "found", "outOf", "returned", "partial", "complete", "truncated", "provenance"):
    if file_result.get(key) != stdin_result.get(key):
        sys.exit("stdin %s = %r, file = %r" % (key, stdin_result.get(key), file_result.get(key)))
if [hit.get("id") for hit in stdin_result.get("hits") or []] != [hit.get("id") for hit in file_result.get("hits") or []]:
    sys.exit("stdin hits differ from the file request")
PY
echo "native stdin request matched the file request"

# A search-only key reads documents, but collection metadata is forbidden for it.
set +e
DATATUG_TYPESENSE_API_KEY="$search_key" "$work/datatug" db schema --db "$source" --format json >"$work/schema-search.json" 2>"$work/schema-search.err"
schema_search_status=$?
set -e
[ "$schema_search_status" != 0 ] || {
	echo "a search-only key listed collection metadata" >&2
	exit 1
}
[ ! -s "$work/schema-search.json" ] || {
	cat "$work/schema-search.json" >&2
	echo "a refused metadata read printed a listing" >&2
	exit 1
}
grep -q 'metadata is not permitted' "$work/schema-search.err" || {
	cat "$work/schema-search.err" >&2
	echo "the search-only metadata refusal was not the metadata-forbidden classification" >&2
	exit 1
}
if grep -q -e "$search_key" -e '127.0.0.1' "$work/schema-search.err"; then
	cat "$work/schema-search.err" >&2
	echo "the metadata refusal leaked the key or the endpoint" >&2
	exit 1
fi
echo "search-only key read documents and was refused collection metadata"

# The metadata key discovers the real schema of its scoped collections, and the
# archive collection is outside that scope.
for pair in "books:$source" "semantics:$semantics_source"; do
	collection=${pair%%:*}
	collection_source=${pair#*:}
	DATATUG_TYPESENSE_API_KEY="$metadata_key" "$work/datatug" db schema --db "$collection_source" --format json >"$work/schema-$collection.json" 2>"$work/schema-$collection.err" || {
		cat "$work/schema-$collection.err" >&2
		echo "the metadata key could not read the $collection schema" >&2
		exit 1
	}
	python3 - "$work/schema-$collection.json" "$collection" <<'PY'
import json, sys
listing = json.load(open(sys.argv[1]))
want = sys.argv[2]
if not str(listing.get("provider", "")).startswith("typesense"):
    sys.exit("provider = %r" % (listing.get("provider"),))
names = [table.get("name") for table in listing.get("tables") or []]
if names != [want]:
    sys.exit("tables = %r, want exactly [%r]" % (names, want))
PY
done
set +e
DATATUG_TYPESENSE_API_KEY="$metadata_key" "$work/datatug" db schema --db "$archive_source" --format json >"$work/schema-archive.json" 2>"$work/schema-archive.err"
schema_archive_status=$?
set -e
[ "$schema_archive_status" != 0 ] || {
	echo "the metadata key listed a collection outside its scope" >&2
	exit 1
}
grep -q 'configured collection is absent from metadata' "$work/schema-archive.err" || {
	cat "$work/schema-archive.err" >&2
	echo "the out-of-scope collection was not refused explicitly" >&2
	exit 1
}
echo "metadata key discovered books and semantics and could not see archive"

# A scan records only a descriptor that names the operator's variable.
scanned="$work/scanned"
DATATUG_TYPESENSE_API_KEY="$metadata_key" "$work/datatug" scan -d "$scanned" -D typesense --dsn-env DATATUG_TEST_TYPESENSE_URL --db books --env local >"$work/scan.out" 2>"$work/scan.err" || {
	cat "$work/scan.err" >&2
	echo "the built CLI could not scan the Typesense collection" >&2
	exit 1
}
python3 - "$scanned/connections/local/books.json" "$scanned/environments/local/catalogs/books/books.db.json" <<'PY'
import json, sys
descriptor = json.load(open(sys.argv[1]))
if descriptor != {"dsnEnv": "DATATUG_TEST_TYPESENSE_URL"}:
    sys.exit("descriptor = %r, want only the environment variable name" % (descriptor,))
catalog = json.load(open(sys.argv[2]))
if catalog.get("driver") != "typesense" or catalog.get("path") != "connections/local/books.json":
    sys.exit("catalog does not record the descriptor: %r" % (catalog,))
if "host" in catalog or "port" in catalog:
    sys.exit("the catalog records an endpoint: %r" % (catalog,))
PY
columns_file=$(find "$scanned" -type f -name '*.columns.json' | sed -n '1p')
[ -n "$columns_file" ] || {
	find "$scanned" -type f >&2
	echo "the scan records no columns" >&2
	exit 1
}
python3 - "$columns_file" <<'PY'
import json, sys
columns = {column["name"]: column.get("dbType") for column in json.load(open(sys.argv[1]))["columns"]}
if columns.get("id") != "string":
    sys.exit("implicit id column = %r, want declared string" % (columns.get("id"),))
if columns.get("tags") != "string[]":
    sys.exit("tags column = %r, want the native string[] type" % (columns.get("tags"),))
if "title" not in columns:
    sys.exit("the scan records no title column: %r" % (columns,))
PY
if grep -rq -e "$metadata_key" -e '127.0.0.1' "$scanned"; then
	echo "the scanned project holds the key or the endpoint" >&2
	exit 1
fi
echo "scan recorded the env-only descriptor and native field types"

# A saved query goes through the policy-enforced path. The policy allows only
# the rows whose author is the current principal, so Frank Herbert's three books
# are eligible and every other author's books are denied.
project_dir="$work/project"
mkdir -p "$project_dir/environments/local/catalogs/books" "$project_dir/connections/local" "$project_dir/queries/books" "$project_dir/policies"
cat >"$project_dir/datatug-project.json" <<'JSON'
{"id":"typesense-project","title":"Typesense project","access":"private"}
JSON
cat >"$project_dir/environments/local/local.env.json" <<'JSON'
{"id":"local","dbServers":[{"driver":"typesense","catalogs":["books"]}]}
JSON
cat >"$project_dir/connections/local/books.json" <<'JSON'
{"dsnEnv":"DATATUG_TEST_TYPESENSE_URL"}
JSON
cat >"$project_dir/environments/local/catalogs/books/books.db.json" <<'JSON'
{"driver":"typesense","path":"connections/local/books.json","dbModel":"books"}
JSON
cat >"$project_dir/queries/books/by-author.query.json" <<'JSON'
{"id":"books/by-author","title":"Books by author","type":"TugQL","createdAt":"2026-01-01T00:00:00Z","targets":[{"catalog":"books"}]}
JSON
cat >"$project_dir/queries/books/by-author.query.tugql.yaml" <<'YAML'
from: {name: books}
columns:
  - {field: title}
  - {field: author}
  - {field: genre}
  - {field: year}
  - {field: seq}
orderBy:
  - {field: seq}
limit: 10
YAML
cat >"$project_dir/policies/global.yaml" <<'YAML'
apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: typesense-books
default: deny
scopes:
  - path: /books
    rules:
      - id: own
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: {field: author}
          right: {param: currentUser}
        fields: [title, author, genre, year, seq]
YAML

"$work/datatug" query run --project "$project_dir" --query books/by-author --env local --as "Frank Herbert" --format json >"$work/saved.json" 2>"$work/saved.err" || {
	cat "$work/saved.err" >&2
	echo "the built CLI could not run the saved Typesense query" >&2
	exit 1
}
python3 - "$work/saved.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
if len(rows) != 3:
    sys.exit("rows = %d, want the three eligible Frank Herbert books: %r" % (len(rows), rows))
if [row.get("$key") for row in rows] != ["001", "002", "003"]:
    sys.exit("keys = %r, want the preserved string ids 001,002,003" % ([row.get("$key") for row in rows],))
for row in rows:
    if row.get("author") != "Frank Herbert":
        sys.exit("a denied row reached the result: %r" % (row,))
    if set(row) - {"$key", "title", "author", "genre", "year", "seq"}:
        sys.exit("a denied column reached the result: %r" % (row,))
PY
if grep -q 'not applied' "$work/saved.err"; then
	cat "$work/saved.err" >&2
	echo "the saved query claimed the native no-policy boundary" >&2
	exit 1
fi
cp "$work/saved.json" "$work/saved-first.json"
"$work/datatug" query run --project "$project_dir" --query books/by-author --env local --as "Frank Herbert" --format json >"$work/saved-reopen.json" 2>"$work/saved-reopen.err" || {
	cat "$work/saved-reopen.err" >&2
	echo "the built CLI could not reopen the saved Typesense query" >&2
	exit 1
}
cmp -s "$work/saved-first.json" "$work/saved-reopen.json" || {
	diff -u "$work/saved-first.json" "$work/saved-reopen.json" >&2 || true
	echo "reopening the project changed the policy result" >&2
	exit 1
}
if grep -rq -e "$search_key" -e '127.0.0.1' "$project_dir"; then
	echo "the saved project holds the key or the endpoint" >&2
	exit 1
fi
echo "saved query enforced the row/column policy and stayed stable when reopened"

# A bad key is refused by the server, and neither it nor the endpoint reaches
# the message.
bad_key="not-a-typesense-key-7f3a91"
set +e
DATATUG_TYPESENSE_API_KEY="$bad_key" "$work/datatug" query typesense --db "$source" --request "$work/search.json" >"$work/badauth.out" 2>"$work/badauth.err"
badauth_status=$?
set -e
[ "$badauth_status" != 0 ] || {
	echo "a bad Typesense key was accepted" >&2
	exit 1
}
[ ! -s "$work/badauth.out" ] || {
	cat "$work/badauth.out" >&2
	echo "a refused native read printed a result" >&2
	exit 1
}
grep -q 'Typesense native search failed' "$work/badauth.err" || {
	cat "$work/badauth.err" >&2
	echo "the bad-key refusal was not the fixed native classification" >&2
	exit 1
}
if grep -q -e "$bad_key" -e '127.0.0.1' "$work/badauth.err"; then
	cat "$work/badauth.err" >&2
	echo "the bad-key refusal leaked the key or the endpoint" >&2
	exit 1
fi
echo "invalid key was refused without leaking the key or endpoint"

# Unsupported reads are refused before provider I/O: native SQL text and a copy
# into the read-only adapter.
set +e
"$work/datatug" query run --db "$source" --sql 'select * from books' --format json --no-policies >"$work/sql.out" 2>"$work/sql.err"
sql_status=$?
set -e
[ "$sql_status" != 0 ] || {
	echo "a native SQL read of Typesense succeeded" >&2
	exit 1
}
[ ! -s "$work/sql.out" ] || {
	cat "$work/sql.out" >&2
	echo "a refused native SQL read printed rows" >&2
	exit 1
}
grep -qi 'native SQL is not supported' "$work/sql.err" || {
	cat "$work/sql.err" >&2
	echo "the native SQL refusal was not explicit" >&2
	exit 1
}
: >"$work/empty.db"
set +e
"$work/datatug" db copy --from "sqlite://$work/empty.db" --to "$source" >"$work/write.out" 2>"$work/write.err"
write_status=$?
set -e
[ "$write_status" != 0 ] || {
	echo "a copy into Typesense succeeded" >&2
	exit 1
}
grep -qi 'read-only' "$work/write.err" || {
	cat "$work/write.err" >&2
	echo "the write refusal did not say read-only" >&2
	exit 1
}
echo "native SQL and writes were refused before provider I/O"

# Cancellation: hold the first search request open, then interrupt the exact CLI
# PID. The listener ACKs only after it has read complete request headers; it sends
# no protocol data. A cancelled read must not print rows or report success, and
# the client must disconnect. Every wait is bounded and every resource is closed
# once; a timeout kill is a failure, never a cancellation pass.
if ! python3 - "$work/datatug" "$work/search.json" <<'PY'
import signal
import socket
import subprocess
import sys
import threading

cli = sys.argv[1]
request_path = sys.argv[2]
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
    [cli, "query", "typesense",
     "--db", "typesense+http://127.0.0.1:%d/books" % port,
     "--request", request_path],
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
)


def reap(proc):
    """Stop the exact child with finite waits; never block unboundedly."""
    if proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=5)
        return
    except subprocess.TimeoutExpired:
        pass
    proc.kill()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        pass


def drain(proc, timeout):
    """Read the child's pipes with a finite wait, reaping on timeout."""
    try:
        return proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        reap(proc)
        try:
            proc.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        raise RuntimeError("the CLI ignored cancellation and had to be killed")


status = 1
try:
    if not request_ready.wait(20):
        raise RuntimeError("the CLI sent no complete request within 20s")
    if server_failure:
        raise RuntimeError("the cancellation listener failed: " + server_failure[0])

    # Signal the exact child, never a process group and never a guessed PID.
    proc.send_signal(signal.SIGINT)
    out, err = drain(proc, 20)

    if proc.returncode == 0:
        raise RuntimeError("a cancelled read reported success")
    if out.strip():
        raise RuntimeError("a cancelled read printed rows: %r" % out[:200])
    if "cancel" not in err.lower():
        raise RuntimeError("the cancellation was not reported safely: %r" % err[:200])
    if not client_gone.wait(10):
        raise RuntimeError("the server did not observe the client disconnect after cancellation")
    thread.join(timeout=5)
    if thread.is_alive():
        raise RuntimeError("the cancellation listener did not stop after the read")
    print("cancellation: complete request held, exact PID interrupted, client disconnected, no rows")
    status = 0
finally:
    reap(proc)
    try:
        srv.close()
    except OSError:
        pass
    thread.join(timeout=5)
    if thread.is_alive():
        print("the cancellation listener did not stop after the read", file=sys.stderr)
        status = 1

sys.exit(status)
PY
then
	echo "the cancellation check failed" >&2
	exit 1
fi

echo "typesense CLI journey passed: built binary on an owned disposable server"
