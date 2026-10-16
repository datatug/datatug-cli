#!/usr/bin/env bash
# check-journey-oracle.sh runs the Oracle journey of `datatug db copy --to sqlscript:...?dialect=oracle`.
#
# For each dataset it
#   1. exports the SQLite file to an Oracle script with the datatug binary built from this checkout, twice, and checks
#      the two scripts are byte for byte the same;
#   2. loads the script into an empty schema of a real Oracle Database Free with SQL*Plus inside the server's own
#      container, the way the script's header says (NLS_LANG=.AL32UTF8 sqlplus -L -S user/password@service @file);
#   3. loads it a second time into the same schema, which must stop at the marker table and change nothing;
#   4. runs the cross-engine verifier (internal/crossverify) over the SQLite file and that schema: tables, columns, keys,
#      indexes, row counts, the identity counters and every value, read through the pure Go Oracle driver
#      (github.com/sijms/go-ora/v2: no client library).
# It fails on any difference, any skipped test and any missing pass, and prints one line per dataset.
#
# Extra checks, once per run (fixture and fuzz datasets):
#   * a client in another character set: the same script loaded with NLS_LANG=...WE8MSWIN1252 and ...US7ASCII must stop
#     at its first statement and leave no table (the character set check of the script);
#   * stop at the first error: a script with a failing statement in the middle stops there (WHENEVER SQLERROR), leaves
#     some tables, and no completion marker row;
#   * atomicity: the same script loaded by a client that goes on after an error (the WHENEVER lines removed) creates
#     every table but gets no marker row, because the block that writes it counts the tables first; the control, the
#     same script with the counts removed from that block, does get a marker row;
#   * the threats are real: with DEFINE ON a "&x" in a literal is a prompt (and the row is silently lost), a line over
#     4,999 characters is cut and its rest ignored, and a raw newline and "/" inside a literal ends the statement; the
#     script writes none of them, and the fuzz script loads with its SET DEFINE OFF line (control: without it the load
#     is not complete).
#
# Datasets: the edge-case fixture (pkg/dbcopy/faithful/testdata/oracle_edge.sql), a fuzz fixture of hostile strings, bytes
# and names (TestWriteOracleFuzzFixture), a cascade fixture of random foreign key graphs (TestWriteOracleCascadeFixture:
# Oracle takes cycles and several cascade paths, so every action must load as it is carried) and the demo databases
# pinned in scripts/journey-datasets.txt, fetched from the public demo-db repositories at their pinned commits and
# checked against their SHA-256. JOURNEY_DATASETS (default: "fixture fuzz cascades chinook northwind pubs sakila employees
# adventureworks") picks a subset; JOURNEY_SQLITE_DIR=<dir> reads <dir>/<id>.sqlite instead of downloading.
#
# The server is the image ORACLE_IMAGE (default: Oracle Database Free as built by gvenzl, pinned by the digest of its
# multi-architecture index), started on a private loopback port with a throwaway password, and removed when the script
# ends. The image runs natively on arm64 and amd64.
#
# Licence: the database inside the image is Oracle Database Free, used under the Oracle Free Use Terms and Conditions
# (FUTC), which allow development and testing use. The founder's decision D10 (2026-10-16) lets CI run it for automated
# testing only: the image is pulled and thrown away with the runner and is never redistributed; the build scripts of the
# gvenzl image are Apache-2.0.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$PWD
image=${ORACLE_IMAGE:-gvenzl/oracle-free:23-slim@sha256:6d61d267a3b978c24c5ac1790e62e927416a0aec446bd86e4b3a1527562757bd}
datasets=${JOURNEY_DATASETS:-fixture fuzz cascades chinook northwind pubs sakila employees adventureworks}
password=Jy1only$$x$RANDOM
work=$(mktemp -d "${TMPDIR:-/tmp}/datatug-journey-oracle.XXXXXX")
name=datatug-journey-oracle-$$
service=FREEPDB1
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

echo "building datatug"
CGO_ENABLED=${CGO_ENABLED:-1} go build -o "$work/datatug" .

echo "starting $image"
docker run -d --name "$name" -e ORACLE_PASSWORD="$password" -p 127.0.0.1::1521 "$image" >/dev/null
ready=0
for _ in $(seq 1 300); do
	if docker logs "$name" 2>&1 | grep -q 'DATABASE IS READY TO USE'; then ready=1; break; fi
	sleep 3
done
[ "$ready" = 1 ] || { echo "the Oracle database did not come up" >&2; docker logs "$name" >&2; exit 1; }
port=$(docker port "$name" 1521/tcp | head -1 | sed 's/.*://')

# sql_as USER: runs the SQL on stdin as USER with SQL*Plus inside the container (silent, no feedback).
sql_as() {
	local user=$1
	{ printf 'set heading off feedback off pagesize 0 linesize 1000 define off\nwhenever sqlerror exit failure\n'; cat; printf '\nexit\n'; } |
		docker exec -i -e NLS_LANG=.AL32UTF8 "$name" sqlplus -L -S "$user/$password@//localhost/$service" | tr -d '\r'
}
sql_root() { sql_as system; }
scalar() { # USER SQL -> the first value
	local user=$1; shift
	printf '%s;\n' "$*" | sql_as "$user" | tr -d '[:space:]' | head -c 200
}
sql_root <<<"select banner_full from v\$version;" | head -2 | sed 's/^/server version /'
docker exec "$name" sqlplus -V | tr -s '\n' ' ' | sed 's/^ //; s/^/client /'
echo
printf '%s\n' "select 'characterset ' || value from nls_database_parameters where parameter = 'NLS_CHARACTERSET';" "select 'compatible ' || value from v\$parameter where name = 'compatible';" "select 'max_string_size ' || value from v\$parameter where name = 'max_string_size';" | sql_root | sed 's/^/server /'

create_user() { # id
	sql_root <<<"create user $1 identified by \"$password\" quota unlimited on users;
grant create session, create table, create sequence to $1;" >/dev/null
}

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

# load FILE USER [NLS_LANG]: the script is copied into the container and run from the file, as the header says. The
# output is on stdout, the exit status of SQL*Plus is the result.
load() {
	local file=$1 user=$2 nls=${3:-.AL32UTF8} base
	base=$(basename "$file")
	docker cp "$file" "$name:/tmp/$base" >/dev/null
	docker exec -e NLS_LANG="$nls" "$name" sqlplus -L -S "$user/$password@//localhost/$service" "@/tmp/$base" </dev/null
}

tables_in() { scalar "$1" "select count(*) from user_tables"; }
marker_rows() { scalar "$1" "select count(*) from \"_demodb_import\""; }

failures=0
printf '%-15s %-8s %-9s %-11s %-9s %s\n' dataset tables rows values warnings result
for ds in $datasets; do
	case $ds in
	fixture)
		DATATUG_WRITE_EDGE_FIXTURE_ORACLE="$work/fixture.sqlite" go test -count=1 -run '^TestWriteOracleEdgeFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT ;;
	fuzz)
		# thousands of rows of hostile text, bytes and names: what ends a literal, starts a SQL*Plus command or a line
		DATATUG_WRITE_FUZZ_FIXTURE_ORACLE="$work/fuzz.sqlite" go test -count=1 -run '^TestWriteOracleFuzzFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT ;;
	cascades)
		DATATUG_WRITE_CASCADE_FIXTURE_ORACLE="$work/cascades.sqlite" go test -count=1 -run '^TestWriteOracleCascadeFixture$' ./pkg/dbcopy/faithful/ >/dev/null
		license=MIT ;;
	*)
		fetch "$ds"
		license=$(license_of "$ds") ;;
	esac
	file="$work/$ds.sqlite"
	script="$work/$ds.oracle.sql"
	url="sqlscript://$script?dialect=oracle&name=$ds&license=$license&edition=DemoDB%20contract%20edition"
	"$work/datatug" db copy --from "sqlite://$file" --to "$url" --json >"$work/$ds.report.json"
	"$work/datatug" db copy --from "sqlite://$file" --to "${url/$ds.oracle.sql/$ds.again.sql}" --json >/dev/null
	cmp -s "$script" "$work/$ds.again.sql" || { echo "$ds: two exports are not byte for byte the same" >&2; failures=$((failures + 1)); continue; }
	user=j_$ds
	create_user "$user"
	if ! load "$script" "$user" >"$work/$ds.load.out" 2>&1; then
		echo "$ds: SQL*Plus stopped at an error:" >&2
		grep -v '^$' "$work/$ds.load.out" | head -8 >&2
		failures=$((failures + 1)); continue
	fi
	# SQL*Plus does not stop at its own errors (SP2-xxxx: an overlong line, a prompt for a variable): the marker row is the proof.
	if grep -q 'SP2-' "$work/$ds.load.out" || [ "$(marker_rows "$user")" != 1 ]; then
		echo "$ds: the load did not complete (no marker row, or an SP2 error):" >&2
		grep -v '^$' "$work/$ds.load.out" | head -8 >&2
		failures=$((failures + 1)); continue
	fi
	# A second load into the same schema must stop at once, at the marker table, and change nothing.
	before=$(tables_in "$user")
	if load "$script" "$user" >"$work/$ds.reload.out" 2>&1; then
		echo "$ds: the script loaded twice into one schema" >&2; failures=$((failures + 1)); continue
	fi
	grep -q 'ORA-00955' "$work/$ds.reload.out" || { echo "$ds: the second load did not stop at the marker table:" >&2; head -5 "$work/$ds.reload.out" >&2; failures=$((failures + 1)); continue; }
	[ "$(tables_in "$user")" = "$before" ] || { echo "$ds: the second load changed the schema" >&2; failures=$((failures + 1)); continue; }
	out="$work/$ds.verify.txt"
	DATATUG_JOURNEY_SQLITE="$file" DATATUG_JOURNEY_ORACLE_DSN="oracle://$user:$password@127.0.0.1:$port/$service" \
		go test -count=1 -v -timeout 40m -run '^TestJourneyOracle$' ./internal/crossverify/ >"$out" 2>&1 || true
	if grep -q -- '--- SKIP' "$out" || ! grep -q -- '--- PASS: TestJourneyOracle' "$out"; then
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
		total=$(tables_in "$user")
		# A client in another character set: the first statement of the script stops the load, and nothing is created.
		for nls in AMERICAN_AMERICA.WE8MSWIN1252 AMERICAN_AMERICA.US7ASCII; do
			create_user "j_nls_${nls##*.}"
			if load "$script" "j_nls_${nls##*.}" "$nls" >"$work/nls.out" 2>&1; then
				echo "character set: the load with NLS_LANG=$nls did not stop" >&2; failures=$((failures + 1)); continue 2
			fi
			grep -q 'ORA-20003' "$work/nls.out" || { echo "character set: NLS_LANG=$nls did not stop at the check of the script:" >&2; head -5 "$work/nls.out" >&2; failures=$((failures + 1)); continue 2; }
			[ "$(tables_in "j_nls_${nls##*.}")" = 0 ] || { echo "character set: tables were created before the check" >&2; failures=$((failures + 1)); continue 2; }
		done
		echo "    character set: a client in WE8MSWIN1252 or US7ASCII stops at the first statement and leaves no table"
		# Stop at the first error: a statement that fails right after the marker table was created.
		awk '{ print } /^CREATE TABLE "_demodb_import"/ && !done { print "INSERT INTO \"no_such_table\" VALUES (1);"; done = 1 }' "$script" >"$work/broken.sql"
		cmp -s "$script" "$work/broken.sql" && { echo "stop: the script could not be broken" >&2; failures=$((failures + 1)); continue; }
		create_user j_stop
		if load "$work/broken.sql" j_stop >"$work/stop.out" 2>&1; then echo "stop: the broken script loaded" >&2; failures=$((failures + 1)); continue; fi
		grep -q 'ORA-00942' "$work/stop.out" || { echo "stop: the failing statement did not fail" >&2; failures=$((failures + 1)); continue; }
		[ "$(tables_in j_stop)" = 1 ] && [ "$(marker_rows j_stop)" = 0 ] || { echo "stop: the load went on after the error ($(tables_in j_stop) tables)" >&2; failures=$((failures + 1)); continue; }
		echo "    stop: a failing statement stops the load at once (WHENEVER SQLERROR): the marker table only, no marker row"
		# Atomicity: a client that goes on after an error (no WHENEVER lines) still gets no marker row, as the first data
		# statement fails and its table stays short. The control: without the counts in the block that writes the marker,
		# the same load does get a marker row.
		grep -v '^WHENEVER ' "$script" | awk '/^INSERT INTO "/ && !done { sub(/^INSERT INTO "/, "INSERT INTO \"nope_"); done = 1 } { print }' >"$work/continue.sql"
		create_user j_continue
		load "$work/continue.sql" j_continue >"$work/continue.out" 2>&1 || true
		grep -q 'ORA-00942' "$work/continue.out" && grep -q 'ORA-20000' "$work/continue.out" || { echo "atomicity: the failing insert or the count check did not raise" >&2; grep -v '^$' "$work/continue.out" | head -6 >&2; failures=$((failures + 1)); continue; }
		[ "$(tables_in j_continue)" = "$total" ] || { echo "atomicity: the client did not go on after the error ($(tables_in j_continue) of $total tables)" >&2; failures=$((failures + 1)); continue; }
		[ "$(marker_rows j_continue)" = 0 ] || { echo "atomicity: a load that lost rows got a marker row" >&2; failures=$((failures + 1)); continue; }
		grep -v -E '^  (SELECT COUNT|IF n <>)' "$work/continue.sql" >"$work/uncounted.sql"
		create_user j_uncounted
		load "$work/uncounted.sql" j_uncounted >"$work/uncounted.out" 2>&1 || true
		[ "$(marker_rows j_uncounted)" = 1 ] || { echo "atomicity: without the counts the load got no marker row, so the control proves nothing" >&2; failures=$((failures + 1)); continue; }
		echo "    atomicity: a client that goes on after an error creates all $total tables and gets no marker row; control: without the counts the marker row is written"
		;;
	fuzz)
		# Positive controls: the hazards are real.
		cat >"$work/threat-define.sql" <<'SQL'
WHENEVER SQLERROR EXIT FAILURE ROLLBACK
CREATE TABLE t_define (v VARCHAR2(20));
INSERT INTO t_define VALUES ('a &x b');
COMMIT;
SQL
		create_user j_threat
		load "$work/threat-define.sql" j_threat >"$work/threat.out" 2>&1 || true
		grep -q 'SP2-0546' "$work/threat.out" && [ "$(scalar j_threat 'select count(*) from t_define')" = 0 ] || { echo "threat control: a & in a literal was not a prompt that loses the row with DEFINE ON" >&2; failures=$((failures + 1)); continue; }
		echo "    threat control: with DEFINE ON a & in a literal is a prompt, SQL*Plus goes on, and the row is lost (exit status 0)"
		{ printf 'WHENEVER SQLERROR EXIT FAILURE ROLLBACK\nSELECT LENGTH('"'"; head -c 6000 /dev/zero | tr '\0' x; printf "') FROM DUAL;\nSELECT 1 FROM DUAL;\n"; } >"$work/threat-long.sql"
		load "$work/threat-long.sql" j_threat >"$work/threat-long.out" 2>&1 || true
		grep -q 'SP2-0027' "$work/threat-long.out" || { echo "threat control: a line of 6,000 characters was not refused" >&2; failures=$((failures + 1)); continue; }
		echo "    threat control: a line over the SQL*Plus limit is cut, its rest is ignored, and SQL*Plus exits with status 0"
		printf "WHENEVER SQLERROR EXIT FAILURE ROLLBACK\nSELECT 'a\n/\nb' FROM DUAL;\nSELECT 1 FROM DUAL;\n" >"$work/threat-slash.sql"
		if load "$work/threat-slash.sql" j_threat >"$work/threat-slash.out" 2>&1; then echo "threat control: a raw newline and / inside a literal did not end the statement" >&2; failures=$((failures + 1)); continue; fi
		grep -q 'ORA-01756' "$work/threat-slash.out" || { echo "threat control: the / inside a literal was not read as the end of the statement" >&2; failures=$((failures + 1)); continue; }
		echo "    threat control: a raw newline and / inside a literal end the statement (the script never writes one)"
		# The script has its SET DEFINE OFF line: without it the load is not complete.
		grep -v '^SET DEFINE OFF$' "$script" >"$work/nodefine.sql"
		cmp -s "$script" "$work/nodefine.sql" && { echo "define control: the script has no SET DEFINE OFF line" >&2; failures=$((failures + 1)); continue; }
		create_user j_nodefine
		load "$work/nodefine.sql" j_nodefine >"$work/nodefine.out" 2>&1 || true
		grep -q 'SP2-0546' "$work/nodefine.out" && [ "$(marker_rows j_nodefine)" = 0 ] || { echo "define control: without SET DEFINE OFF the load was complete" >&2; failures=$((failures + 1)); continue; }
		echo "    define control: without SET DEFINE OFF the load prompts for the variables of the values and loses rows: no marker row"
		;;
	esac
done
if [ "$failures" -ne 0 ]; then echo "$failures dataset(s) failed" >&2; exit 1; fi
echo "Oracle journey passed"
