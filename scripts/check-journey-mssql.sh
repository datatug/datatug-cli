#!/usr/bin/env bash
# check-journey-mssql.sh runs the SQL Server journey of `datatug db copy --to sqlscript:...?dialect=mssql`.
#
# For each dataset it
#   1. exports the SQLite file to a T-SQL script with the datatug binary built from this checkout, twice, and
#      checks the two scripts are byte for byte the same;
#   2. loads the script into an empty database of a real SQL Server with sqlcmd inside the server's own container,
#      the way the script's header says (-X -x -b -f 65001 -i <file>: one transaction, stop at the first error);
#   3. loads it a second time into the same database, which must stop at the marker table and change nothing;
#   4. runs the cross-engine verifier (internal/crossverify) over the SQLite file and that database: tables,
#      columns, keys, indexes, row counts, the identity counters and every value, read through the Go SQL Server
#      driver (github.com/microsoft/go-mssqldb).
# It fails on any difference, any skipped test and any missing pass, and prints one line per dataset.
#
# Extra checks, once per run:
#   * atomicity: a script with a failing batch in the middle is loaded WITHOUT -b (so sqlcmd goes on after the
#     error) and must leave no table behind: the guard of every batch and XACT_ABORT roll the whole load back;
#   * the threat is real: without -x sqlcmd replaces $(NAME) inside a literal with the environment variable NAME
#     (positive control for the fuzz dataset, whose values hold "$(" and are written so it never reaches a line);
#   * the fuzz script loads with sqlcmd run without -x and -X, because no value holds "$(" or starts a line.
#
# Datasets: the edge-case fixture (pkg/dbcopy/faithful/testdata/mssql_edge.sql), a fuzz fixture of hostile strings,
# bytes and names (TestWriteMSSQLFuzzFixture), a cascade fixture of random foreign key graphs
# (TestWriteMSSQLCascadeFixture: SQL Server refuses a key that makes a cycle or a second cascade path, error 1785)
# and the demo databases pinned in scripts/journey-datasets.txt, fetched from the public demo-db repositories at
# their pinned commits and checked against their SHA-256. JOURNEY_DATASETS (default: "fixture fuzz cascades
# chinook northwind pubs sakila employees adventureworks") picks a subset; JOURNEY_SQLITE_DIR=<dir> reads
# <dir>/<id>.sqlite instead of downloading.
#
# The server is the image MSSQL_IMAGE (default: SQL Server 2022 pinned by digest), started on a private loopback
# port with a throwaway password, and removed when the script ends. On an arm64 host the image runs as linux/amd64
# under emulation (Rosetta or QEMU), which is slow: AdventureWorks takes minutes.
#
# Licence: the image is Microsoft SQL Server Developer edition. ACCEPT_EULA=Y accepts its end-user licence for
# automated testing only (the founder's decision D10, 2026-10-16); the image is never redistributed.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD
image=${MSSQL_IMAGE:-mcr.microsoft.com/mssql/server:2022-latest@sha256:cf8f0ab2ebe61feec2eed2629061a07d247f791c2a8ddda74a4ac9f06a291718}
datasets=${JOURNEY_DATASETS:-fixture fuzz cascades chinook northwind pubs sakila employees adventureworks}
password=Jy-only-$$-$RANDOM-aA9
work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-mssql.XXXXXX")
name=datatug-journey-mssql-$$
sqlcmd_path=/opt/mssql-tools18/bin/sqlcmd
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

platform=()
case $(uname -m) in arm64 | aarch64) platform=(--platform linux/amd64) ;; esac

echo "building datatug"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .

echo "starting $image"
docker run -d --name "$name" "${platform[@]}" -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD="$password" -e MSSQL_PID=Developer -p 127.0.0.1::1433 "$image" >/dev/null
# sqlcmd with -C trusts the server's self-signed certificate; the loopback port and the throwaway password are the whole exposure.
sqlcmd_root() { docker exec -i "$name" "$sqlcmd_path" -C -S localhost -U sa -P "$password" -X "$@"; }
ready=0
for _ in $(seq 1 240); do
	if sqlcmd_root -b -Q 'SELECT 1' >/dev/null 2>&1; then ready=1; break; fi
	sleep 2
done
[ "$ready" = 1 ] || { echo "the SQL Server did not come up" >&2; docker logs "$name" >&2; exit 1; }
port=$(docker port "$name" 1433/tcp | head -1 | sed 's/.*://')
sqlcmd_root -h -1 -W -Q "SET NOCOUNT ON; SELECT @@VERSION" | head -1 | sed 's/^/server version /'
sqlcmd_root -h -1 -W -Q "SET NOCOUNT ON; SELECT SERVERPROPERTY('Collation')" | head -1 | sed 's/^/server collation /'

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

# load FILE DB [extra sqlcmd flags...]: the script is copied into the container and read with -i, as the header says
# (the byte order mark is skipped by -i and would reach the server from a redirect).
load() {
	local file=$1 db=$2; shift 2
	docker cp "$file" "$name:/tmp/$db.sql" >/dev/null
	docker exec "$name" "$sqlcmd_path" -C -S localhost -U sa -P "$password" -d "$db" -f 65001 "$@" -i "/tmp/$db.sql"
}

create_db() { sqlcmd_root -b -Q "CREATE DATABASE [$1]"; }

tables_in() { sqlcmd_root -d "$1" -h -1 -W -Q "SET NOCOUNT ON; SELECT COUNT(*) FROM sys.tables" | head -1 | tr -d '[:space:]'; }

# urlencode percent-encodes a path for a query value (ASCII paths; anything outside [A-Za-z0-9._~/-] is escaped).
urlencode() {
	local s=$1 out= c i
	for ((i = 0; i < ${#s}; i++)); do
		c=${s:i:1}
		case $c in [a-zA-Z0-9._~/-]) out+=$c ;; *) out+=$(printf '%%%02X' "'$c") ;; esac
	done
	printf '%s' "$out"
}

# The hostile multi-line licence notice (a GO line, \! cmd, source, */, CR LF, U+2028, a byte order mark ...) that the fixture
# and fuzz datasets carry in their header through notice-file=: it must load, verify and change no line that is not a comment.
hostile_notice=$work/hostile_notice.txt
DATATUG_WRITE_HOSTILE_NOTICE="$hostile_notice" go test -count=1 -run '^TestWriteHostileNotice$' ./pkg/dbcopy/faithful/ >/dev/null
[ -s "$hostile_notice" ] || { echo "the hostile notice was not written" >&2; exit 1; }
pwned=/tmp/datatug-notice-pwned

# Positive control: the hazard is real. A line that starts with !! is a command of sqlcmd and runs a program.
printf '!! touch %s\nGO\n' "$pwned" >"$work/bang.sql"
docker cp "$work/bang.sql" "$name:/tmp/bang.sql" >/dev/null
docker exec "$name" "$sqlcmd_path" -C -S localhost -U sa -P "$password" -i /tmp/bang.sql >/dev/null 2>&1 || true
docker exec "$name" test -e "$pwned" || { echo "notice control: sqlcmd did not run !! touch, so the notice check proves nothing" >&2; exit 1; }
docker exec "$name" rm -f "$pwned"
echo "notice control: a line that starts with !! runs a program in this sqlcmd (no notice line starts that way)"

failures=0
printf '%-15s %-8s %-9s %-11s %-9s %s\n' dataset tables rows values warnings result
for ds in $datasets; do
	case $ds in
	fixture)
		DATATUG_WRITE_EDGE_FIXTURE_MSSQL="$work/fixture.sqlite" go test -count=1 -run '^TestWriteMSSQLEdgeFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT ;;
	fuzz)
		# thousands of rows of hostile text, bytes and names: what ends a literal, starts a sqlcmd command or a line
		DATATUG_WRITE_FUZZ_FIXTURE_MSSQL="$work/fuzz.sqlite" go test -count=1 -run '^TestWriteMSSQLFuzzFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT ;;
	cascades)
		DATATUG_WRITE_CASCADE_FIXTURE_MSSQL="$work/cascades.sqlite" go test -count=1 -run '^TestWriteMSSQLCascadeFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT ;;
	*)
		fetch "$ds"
		license=$(license_of "$ds") ;;
	esac
	file="$work/$ds.sqlite"
	script="$work/$ds.mssql.sql"
	notice_query=
	case $ds in fixture | fuzz) notice_query="&notice-file=$(urlencode "$hostile_notice")" ;; esac
	url="sqlscript://$script?dialect=mssql&name=$ds&license=$license&edition=DemoDB%20contract%20edition$notice_query"
	"$work/datatug" db copy --from "sqlite://$file" --to "$url" --json >"$work/$ds.report.json"
	"$work/datatug" db copy --from "sqlite://$file" --to "${url/$ds.mssql.sql/$ds.again.sql}" --json >/dev/null
	cmp -s "$script" "$work/$ds.again.sql" || { echo "$ds: two exports are not byte for byte the same" >&2; failures=$((failures + 1)); continue; }
	if [ -n "$notice_query" ]; then
		# The notice is in the header, whole, and it added no line that is not a comment: the script is the one
		# of the same export without a notice, plus comment lines.
		grep -q 'Copyright (c) 2024 Hostile Notice Co\.' "$script" || { echo "$ds: the notice is not in the script" >&2; failures=$((failures + 1)); continue; }
		plain_url=${url%&notice-file=*}
		"$work/datatug" db copy --from "sqlite://$file" --to "${plain_url/$ds.mssql.sql/$ds.nonotice.sql}" --json >/dev/null
		cmp -s <(grep -v '^--' "$script") <(grep -v '^--' "$work/$ds.nonotice.sql") || { echo "$ds: the notice added a line that is not a comment" >&2; failures=$((failures + 1)); continue; }
		echo "    notice: ${hostile_notice##*/} is in the header, and every line it added is a comment"
	fi
	db=journey_$ds
	create_db "$db"
	if ! load "$script" "$db" -X -x -b >"$work/$ds.load.out" 2>&1; then
		echo "$ds: sqlcmd stopped at an error:" >&2
		head -5 "$work/$ds.load.out" >&2
		failures=$((failures + 1)); continue
	fi
	# A second load into the same database must stop at once, at the marker table, and change nothing.
	if load "$script" "$db" -X -x -b >"$work/$ds.reload.out" 2>&1; then
		echo "$ds: the script loaded twice into one database" >&2; failures=$((failures + 1)); continue
	fi
	grep -q '_demodb_import' "$work/$ds.reload.out" || { echo "$ds: the second load did not stop at the marker table:" >&2; head -3 "$work/$ds.reload.out" >&2; failures=$((failures + 1)); continue; }
	if [ -n "$notice_query" ]; then
		# sqlcmd ran neither the "!!" command of the notice nor any other: the file it would have made is not there.
		if docker exec "$name" test -e "$pwned"; then echo "$ds: the notice ran a client command" >&2; failures=$((failures + 1)); continue; fi
		echo "    notice: loaded; no client command ran"
	fi
	out="$work/$ds.verify.txt"
	DATATUG_JOURNEY_SQLITE="$file" DATATUG_JOURNEY_MSSQL_DSN="sqlserver://sa:${password//!/%21}@127.0.0.1:$port?database=$db&encrypt=disable" \
		go test -count=1 -v -timeout 40m -run '^TestJourneyMSSQL$' ./internal/crossverify/ >"$out" 2>&1 || true
	if grep -q -- '--- SKIP' "$out" || ! grep -q -- '--- PASS: TestJourneyMSSQL' "$out"; then
		echo "$ds: the verifier did not pass:" >&2
		grep -E 'DIFF|FAIL|SKIP|error' "$out" | head -20 >&2
		failures=$((failures + 1)); continue
	fi
	line=$(grep -o '[0-9]* tables, [0-9]* rows, [0-9]* values compared, [0-9]* warnings' "$out" | head -1)
	read -r t _ r _ v _ _ w _ <<<"$line"
	printf '%-15s %-8s %-9s %-11s %-9s %s\n' "$ds" "${t%,}" "${r%,}" "${v%,}" "$w" PASS
	{ grep -o '"kind": "[a-z-]*"' "$work/$ds.report.json" || true; } | sort | uniq -c | sed 's/"kind": //; s/^/    warning /'

	case $ds in
	fixture)
		# Atomicity: a batch right after the first table is created is made to fail (division by zero), and the script is
		# loaded without -b, so sqlcmd goes on with every later batch. The guard and XACT_ABORT must have undone the
		# whole load; without the guard the later CREATE TABLE batches would run outside the transaction and stay.
		awk '{ print } /^CREATE TABLE / { n++ } /^GO$/ && n >= 2 && !done { print "IF @@TRANCOUNT=0 SET NOEXEC ON"; print "SELECT 1/0;"; print "GO"; done = 1 }' "$script" >"$work/broken.sql"
		cmp -s "$script" "$work/broken.sql" && { echo "atomicity: the script could not be broken" >&2; failures=$((failures + 1)); continue; }
		create_db journey_broken
		load "$work/broken.sql" journey_broken -X -x >"$work/broken.out" 2>&1 || true
		grep -q 'Divide by zero' "$work/broken.out" || { echo "atomicity: the failing batch did not fail" >&2; failures=$((failures + 1)); continue; }
		left=$(tables_in journey_broken)
		if [ "$left" != 0 ]; then echo "atomicity: $left table(s) were left behind by a failed load" >&2; failures=$((failures + 1)); continue; fi
		echo "    atomicity: a failed load without -b left no table behind"
		# The same break with the guard lines removed keeps data: the guard is what makes the difference.
		grep -v '^IF @@TRANCOUNT=0 SET NOEXEC ON$' "$work/broken.sql" >"$work/unguarded.sql"
		create_db journey_unguarded
		load "$work/unguarded.sql" journey_unguarded -X -x >"$work/unguarded.out" 2>&1 || true
		if [ "$(tables_in journey_unguarded)" = 0 ]; then echo "atomicity: without the guard the load left no table: the control proves nothing" >&2; failures=$((failures + 1)); continue; fi
		echo "    atomicity control: without the guard lines a failed load without -b keeps tables"
		;;
	fuzz)
		# Positive controls: the hazard of a value that holds "$(" is real. Without -x, sqlcmd replaces $(NAME) with the
		# environment variable NAME even inside a literal, and an undefined name stops the load under -b; with -x it does not.
		printf "SELECT N'\$(JOURNEY_PROBE)' AS v;\nGO\n" >"$work/threat.sql"
		docker cp "$work/threat.sql" "$name:/tmp/threat.sql" >/dev/null
		probe() { docker exec -e JOURNEY_PROBE=substituted "$name" "$sqlcmd_path" -C -S localhost -U sa -P "$password" -f 65001 "$@" -i /tmp/threat.sql 2>&1; }
		probe | grep -q substituted || { echo "threat control: sqlcmd did not substitute \$(NAME) without -x, so the control proves nothing" >&2; failures=$((failures + 1)); continue; }
		probe -x | grep -q 'JOURNEY_PROBE' || { echo "threat control: -x did not keep \$(NAME) as it is" >&2; failures=$((failures + 1)); continue; }
		echo "    threat control: without -x sqlcmd replaces \$(NAME) inside a literal (the script never writes \$( in a value or a name)"
		# This sqlcmd keeps a multi-line literal whole across a GO line. The script does not rely on that (SSMS and plain
		# splitters do cut a batch at a GO line inside a literal): no value starts a line (unit-tested on every script).
		printf "SELECT N'a\nGO\nb' AS v;\nGO\n" >"$work/go.sql"
		docker cp "$work/go.sql" "$name:/tmp/go.sql" >/dev/null
		if docker exec "$name" "$sqlcmd_path" -C -S localhost -U sa -P "$password" -X -x -b -f 65001 -i /tmp/go.sql >/dev/null 2>&1; then
			echo "    note: this sqlcmd does not cut a literal at a GO line; the script does not depend on it"
		else
			echo "    note: this sqlcmd cuts a literal at a GO line; no value of the script can start a line"
		fi
		# No value holds "$(" and none starts a line: the script loads without -x and without -X too.
		create_db journey_fuzz_plain
		if ! load "$script" journey_fuzz_plain -b >"$work/fuzz.plain.out" 2>&1; then echo "fuzz: the script does not load without -X -x:" >&2; head -5 "$work/fuzz.plain.out" >&2; failures=$((failures + 1)); continue; fi
		echo "    fuzz script loaded without -X and -x"
		;;
	esac
done
if [ "$failures" -ne 0 ]; then echo "$failures dataset(s) failed" >&2; exit 1; fi
echo "SQL Server journey passed"
