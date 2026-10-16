#!/usr/bin/env bash
# check-journey-mysql.sh runs the MySQL journey of `datatug db copy --to sqlscript:...?dialect=mysql`.
#
# For each dataset it
#   1. exports the SQLite file to a MySQL script with the datatug binary built from this checkout, twice, and
#      checks the two scripts are byte for byte the same;
#   2. loads the script into an empty database of a real MySQL server with the mysql client inside the server's
#      own container (the way a person loads it: --skip-commands, stop at the first error);
#   3. runs the cross-engine verifier (internal/crossverify) over the SQLite file and that database: tables,
#      columns, keys, indexes, row counts and every value, read through the Go MySQL driver.
# It fails on any difference, any skipped test and any missing pass, and prints one line per dataset.
#
# Datasets: the edge-case fixture (pkg/dbcopy/faithful/testdata/mysql_edge.sql), a fuzz fixture of hostile
# strings, bytes and names (TestWriteMySQLFuzzFixture) and the demo databases pinned
# in scripts/journey-datasets.txt, fetched from the public demo-db repositories at their pinned commits and
# checked against their SHA-256. JOURNEY_DATASETS (default: "fixture fuzz chinook northwind pubs sakila employees
# adventureworks") picks a subset; JOURNEY_SQLITE_DIR=<dir> reads <dir>/<id>.sqlite instead of downloading.
#
# The server is the image MYSQL_IMAGE (default: mysql:8.4 pinned by digest), started on a private loopback port
# with a throwaway root password, and removed when the script ends.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD
image=${MYSQL_IMAGE:-mysql:8.4@sha256:24989179f58856db763f418010c1169fecc944e3ec1ea7e921572d83eb070f70}
datasets=${JOURNEY_DATASETS:-fixture fuzz chinook northwind pubs sakila employees adventureworks}
password=journey-only-$$-$RANDOM
work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-mysql.XXXXXX")
name=datatug-journey-mysql-$$
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

echo "building datatug"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .

echo "starting $image"
docker run -d --name "$name" -e MYSQL_ROOT_PASSWORD="$password" -p 127.0.0.1::3306 --tmpfs /var/lib/mysql:rw,size=4g "$image" >/dev/null
for _ in $(seq 1 120); do
	# the entrypoint runs a temporary server on a socket first; the real one logs "port: 3306"
	if docker logs "$name" 2>&1 | grep -q "ready for connections.*port: 3306"; then break; fi
	sleep 1
done
docker logs "$name" 2>&1 | grep -q "ready for connections.*port: 3306" || { echo "the MySQL server did not come up" >&2; docker logs "$name" >&2; exit 1; }
port=$(docker port "$name" 3306/tcp | head -1 | sed 's/.*://')
docker exec "$name" mysql -uroot -p"$password" -N -e 'select version()' 2>/dev/null | sed 's/^/server version /'

mysql_root() { docker exec -i "$name" mysql -uroot -p"$password" "$@" 2>/dev/null; }

fetch() { # id -> $work/<id>.sqlite
	local id=$1 commit file sha license want got
	if [ -n "${JOURNEY_SQLITE_DIR:-}" ]; then cp "$JOURNEY_SQLITE_DIR/$id.sqlite" "$work/$id.sqlite"; return; fi
	read -r _ commit file sha license < <(grep -E "^$id[[:space:]]" scripts/journey-datasets.txt)
	curl -fsSL --retry 3 -o "$work/$id.download" "https://raw.githubusercontent.com/demo-db/$id/$commit/data-source/$file"
	case $file in *.gz) gzip -dc "$work/$id.download" >"$work/$id.sqlite" ;; *) mv "$work/$id.download" "$work/$id.sqlite" ;; esac
	got=$(shasum -a 256 "$work/$id.sqlite" | cut -d' ' -f1)
	[ "$got" = "$sha" ] || { echo "$id: SHA-256 is $got, pinned $sha" >&2; exit 1; }
}

license_of() { awk -v id="$1" '$1 == id { print $5 }' scripts/journey-datasets.txt; }

failures=0
printf '%-15s %-8s %-9s %-11s %-9s %s\n' dataset tables rows values warnings result
for ds in $datasets; do
	if [ "$ds" = fixture ]; then
		DATATUG_WRITE_EDGE_FIXTURE="$work/fixture.sqlite" go test -count=1 -run '^TestWriteMySQLEdgeFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT
	elif [ "$ds" = fuzz ]; then
		# thousands of rows of hostile text, bytes and names: what ends a literal, starts a client command or a line
		DATATUG_WRITE_FUZZ_FIXTURE="$work/fuzz.sqlite" go test -count=1 -run '^TestWriteMySQLFuzzFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT
	else
		fetch "$ds"
		license=$(license_of "$ds")
	fi
	file="$work/$ds.sqlite"
	url="sqlscript://$work/$ds.mysql.sql?dialect=mysql&name=$ds&license=$license&edition=DemoDB%20contract%20edition"
	"$work/datatug" db copy --from "sqlite://$file" --to "$url" --json >"$work/$ds.report.json"
	"$work/datatug" db copy --from "sqlite://$file" --to "${url/$ds.mysql.sql/$ds.again.sql}" --json >/dev/null
	cmp -s "$work/$ds.mysql.sql" "$work/$ds.again.sql" || { echo "$ds: two exports are not byte for byte the same" >&2; failures=$((failures + 1)); continue; }
	db=journey_$ds
	mysql_root -e "DROP DATABASE IF EXISTS \`$db\`; CREATE DATABASE \`$db\` CHARACTER SET utf8mb4"
	if ! docker exec -i "$name" mysql -uroot -p"$password" --default-character-set=utf8mb4 --skip-commands --database "$db" <"$work/$ds.mysql.sql" 2>"$work/$ds.load.err"; then
		echo "$ds: the mysql client stopped at an error:" >&2
		grep -v 'Using a password' "$work/$ds.load.err" | head -5 >&2
		failures=$((failures + 1)); continue
	fi
	# A second load into the same database must stop at once, at the marker table, and change nothing.
	if docker exec -i "$name" mysql -uroot -p"$password" --default-character-set=utf8mb4 --skip-commands --database "$db" <"$work/$ds.mysql.sql" 2>"$work/$ds.reload.err"; then
		echo "$ds: the script loaded twice into one database" >&2; failures=$((failures + 1)); continue
	fi
	grep -q '_demodb_import' "$work/$ds.reload.err" || { echo "$ds: the second load did not stop at the marker table:" >&2; head -3 "$work/$ds.reload.err" >&2; failures=$((failures + 1)); continue; }
	out="$work/$ds.verify.txt"
	DATATUG_JOURNEY_SQLITE="$file" DATATUG_JOURNEY_MYSQL_DSN="root:$password@tcp(127.0.0.1:$port)/$db?charset=utf8mb4" \
		go test -count=1 -v -run '^TestJourneyMySQL$' ./internal/crossverify/ >"$out" 2>&1 || true
	if grep -q -- '--- SKIP' "$out" || ! grep -q -- '--- PASS: TestJourneyMySQL' "$out"; then
		echo "$ds: the verifier did not pass:" >&2
		grep -E 'DIFF|FAIL|SKIP|error' "$out" | head -20 >&2
		failures=$((failures + 1)); continue
	fi
	line=$(grep -o '[0-9]* tables, [0-9]* rows, [0-9]* values compared, [0-9]* warnings' "$out" | head -1)
	read -r t _ r _ v _ _ w _ <<<"$line"
	printf '%-15s %-8s %-9s %-11s %-9s %s\n' "$ds" "${t%,}" "${r%,}" "${v%,}" "$w" PASS
	{ grep -o '"kind": "[a-z-]*"' "$work/$ds.report.json" || true; } | sort | uniq -c | sed 's/"kind": //; s/^/    warning /'
done
if [ "$failures" -ne 0 ]; then echo "$failures dataset(s) failed" >&2; exit 1; fi
echo "MySQL journey passed"
