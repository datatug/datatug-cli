#!/usr/bin/env bash
set -euo pipefail
if [[ "$(uname -s)" != Linux ]]; then
  echo 'ClickHouse integration requires the approved Linux environment.' >&2
  exit 1
fi
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
fixture="datatug-cli-clickhouse-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}-$$"
umask 077
diagnostics=$(mktemp -d /tmp/dalgo-clickhouse-diagnostics.XXXXXX)

# Each command has a deadline and a 2 MiB per-file ceiling. Diagnostics never
# inspect environment/configuration or print process arguments. Excerpts are
# capped at 60 lines of 1024 bytes and omit likely credential/stack-dump lines.
capture() {
  local destination=$1
  shift
  (ulimit -f 2048; timeout --kill-after=1s 5s "$@" >"$destination" 2>&1)
}
excerpt() {
  [[ -f "$1" ]] || return 0
  tail -n 60 "$1" | LC_ALL=C awk '
    {
      if (tolower($0) ~ /password|passwd|authorization|access_token|synthetic-reader|secret_key|secret[[:space:]]*[:=]|stack trace|stacktrace/ || $0 ~ /^[0-9]+\. /) {
        print "[diagnostic line redacted]"
      } else {
        print substr($0, 1, 1024)
      }
    }' >&2
}
diagnostic() {
  local name=$1
  shift
  echo "--- ClickHouse fixture: $name ---" >&2
  if ! capture "$diagnostics/$name" "$@"; then
    echo 'Diagnostic command failed or reached its output/time limit.' >&2
  fi
  excerpt "$diagnostics/$name"
}
global_pool_diagnostics() {
  echo '--- ClickHouse fixture: global pool atomic metrics ---' >&2
  # Port 9363 belongs only to this container and has no Docker publication.
  # Capture the HTTP response privately under the existing 2 MiB/5s ceiling;
  # never print the raw response, headers, labels or an HTTP error body.
  if ! capture "$diagnostics/global-pool-response" docker exec "$fixture" bash -c '
    exec 3<>/dev/tcp/127.0.0.1/9363 || exit 1
    printf "GET /metrics HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n" >&3
    cat <&3
  '; then
    echo 'Global pool metrics request failed or reached its output/time limit.' >&2
    return 0
  fi
  if ! LC_ALL=C awk '
    NR == 1 {
      sub(/\r$/, "")
      if ($0 !~ /^HTTP\/1\.[01] 200([[:space:]]|$)/) bad = 1
      next
    }
    !body {
      sub(/\r$/, "")
      if ($0 == "") body = 1
      next
    }
    {
      sub(/\r$/, "")
      if ($0 ~ /^ClickHouseMetrics_GlobalThread(Active|Scheduled)?([[:space:]]|[{]|$)/) {
        if (NF != 2 || $1 !~ /^ClickHouseMetrics_GlobalThread(Active|Scheduled)?$/ || $2 !~ /^[0-9]+$/ || ++counts[$1] != 1) bad = 1
        values[$1] = $2
      }
    }
    END {
      split("ClickHouseMetrics_GlobalThread ClickHouseMetrics_GlobalThreadActive ClickHouseMetrics_GlobalThreadScheduled", names, " ")
      for (i = 1; i <= 3; i++) if (counts[names[i]] != 1) bad = 1
      if (bad || !body) exit 1
      for (i = 1; i <= 3; i++) print names[i], values[names[i]]
    }
  ' "$diagnostics/global-pool-response" >"$diagnostics/global-pool-metrics"; then
    echo 'Global pool metrics response was malformed or missing unique numeric counters.' >&2
    return 0
  fi
  excerpt "$diagnostics/global-pool-metrics"
}
failure_diagnostics() {
  diagnostic state docker inspect --format 'status={{.State.Status}} running={{.State.Running}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} error={{.State.Error}}' "$fixture"
  diagnostic stats docker stats --no-stream --format 'memory={{.MemUsage}} memory_percent={{.MemPerc}} pids={{.PIDs}} cpu={{.CPUPerc}}' "$fixture"
  diagnostic threads docker top "$fixture" -eo pid,ppid,nlwp,comm
  global_pool_diagnostics
  # The daemon can enumerate our container's threads even if SQL is blocked or
  # docker exec cannot create a process. Never request command-line arguments.
  echo '--- ClickHouse fixture: thread names and wait channels ---' >&2
  if capture "$diagnostics/thread-details" docker top "$fixture" -eLo pid,ppid,lwp,stat,comm,wchan:32; then
    LC_ALL=C awk 'NR > 1 { counts[$5 " state=" $4 " wait=" $6]++ }
      END { for (name in counts) print counts[name], name }' "$diagnostics/thread-details" |
      LC_ALL=C sort -nr >"$diagnostics/thread-counts"
    excerpt "$diagnostics/thread-counts"
  else
    echo 'Thread enumeration failed or reached its output/time limit.' >&2
    excerpt "$diagnostics/thread-details"
  fi
  # OS-only metadata remains useful when even system.processes cannot execute.
  # Read only selected process/cgroup counters, never cmdline, environ or config.
  diagnostic proc-status docker exec "$fixture" sh -c '
    for process in /proc/[0-9]*; do
      IFS= read -r name < "$process/comm" || continue
      case "$name" in clickhouse|clickhouse-serv|clickhouse-clie|entrypoint.sh) ;; *) continue ;; esac
      printf "process=%s name=%s\n" "${process##*/}" "$name"
      while IFS= read -r line; do
        case "$line" in
          State:*|PPid:*|Threads:*|VmRSS:*|voluntary_ctxt_switches:*|nonvoluntary_ctxt_switches:*) printf "%s\n" "$line" ;;
        esac
      done < "$process/status"
      if IFS= read -r channel < "$process/wchan"; then printf "wait=%s\n" "$channel"; fi
    done
    # File presence records metadata persistence without SQL or metadata contents.
    # It does not establish that DDL or row insertion completed successfully.
    for object in /var/lib/clickhouse/metadata/explore.sql /var/lib/clickhouse/metadata/forbidden.sql /var/lib/clickhouse/metadata/forbidden/secret.sql /var/lib/clickhouse/metadata/explore/events.sql; do
      if [ -e "$object" ]; then printf "metadata-present=%s\n" "$object";
      else printf "metadata-missing=%s\n" "$object"; fi
    done
    for counter in /sys/fs/cgroup/pids.current /sys/fs/cgroup/pids.max /sys/fs/cgroup/pids.events /sys/fs/cgroup/memory.events; do
      [ -r "$counter" ] || continue
      printf "%s\n" "$counter"
      while IFS= read -r line; do printf "%s\n" "$line"; done < "$counter"
    done
  '
  # Do not print system.processes.query: initialization includes a plaintext
  # synthetic password. Extract only an anchored DDL/INSERT verb+identifier;
  # no expressions, literals, settings, INSERT data or complete SQL can match.
  diagnostic active-statements docker exec "$fixture" clickhouse-client --max_threads=1 --max_execution_time=2 --query "SELECT query_id, query_kind, round(elapsed,3) AS elapsed, read_rows, written_rows, length(thread_ids) AS threads, extract(query, '(?is)^[[:space:]]*((?:CREATE[[:space:]]+(?:DATABASE|TABLE|USER|SETTINGS[[:space:]]+PROFILE)|INSERT[[:space:]]+INTO)[[:space:]]+[A-Za-z_][A-Za-z0-9_.]*)') AS statement_identifier FROM system.processes WHERE query_id != currentQueryID() ORDER BY elapsed DESC LIMIT 8 FORMAT TSVWithNames"
  diagnostic catalog-progress docker exec "$fixture" clickhouse-client --max_threads=1 --max_execution_time=2 --query "SELECT 'database' AS kind, name AS object, engine FROM system.databases WHERE name IN ('explore','forbidden') UNION ALL SELECT 'table' AS kind, concat(database,'.',name) AS object, engine FROM system.tables WHERE database IN ('explore','forbidden') LIMIT 10 FORMAT TSVWithNames"
  diagnostic thread-pools docker exec "$fixture" clickhouse-client --max_threads=1 --max_execution_time=2 --query "SELECT metric, value FROM system.metrics WHERE metric IN ('GlobalThread','GlobalThreadActive','GlobalThreadScheduled') OR startsWith(metric,'Background') ORDER BY if(startsWith(metric,'GlobalThread'),0,1), metric LIMIT 48 FORMAT TSVWithNames"
  echo '--- ClickHouse fixture: last readiness stderr ---' >&2
  excerpt "$diagnostics/readiness.err"
  echo '--- ClickHouse fixture: last readiness stdout ---' >&2
  excerpt "$diagnostics/readiness.out"
  diagnostic entrypoint docker logs --tail 60 "$fixture"
  echo '--- ClickHouse fixture: server error log ---' >&2
  if capture "$diagnostics/server-error-tail" docker exec "$fixture" tail -n 60 /var/log/clickhouse-server/clickhouse-server.err.log; then
    excerpt "$diagnostics/server-error-tail"
  else
    # docker exec may itself be refused at the PID ceiling. docker cp uses
    # the daemon and also works after the server has exited; copy only the
    # error log, never the giant trace log or configuration tree.
    excerpt "$diagnostics/server-error-tail"
    if ! capture "$diagnostics/server-error-copy-status" docker cp "$fixture:/var/log/clickhouse-server/clickhouse-server.err.log" "$diagnostics/server-error.log"; then
      echo 'Error-log copy failed or was incomplete at the 2 MiB/5s limit.' >&2
      excerpt "$diagnostics/server-error-copy-status"
    fi
    excerpt "$diagnostics/server-error.log"
  fi
}
cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "$status" != 0 ]]; then failure_diagnostics || true; fi
  if ! timeout --kill-after=2s 10s docker rm -f "$fixture" >/dev/null 2>&1; then
    echo 'ClickHouse fixture container cleanup failed.' >&2
    status=1
  fi
  rm -rf -- "$diagnostics"
  exit "$status"
}
trap cleanup EXIT INT TERM
# Exact published ClickHouse image repository, version and verified index digest.
docker run --detach --name "$fixture" --memory=2g --memory-swap=2g --cpus=2 --pids-limit=128 \
  --publish 127.0.0.1::9000 --publish 127.0.0.1::8123 --env CLICKHOUSE_SKIP_USER_SETUP=1 \
  --mount "type=bind,source=$root/testdata/clickhouse/init.sql,target=/docker-entrypoint-initdb.d/01-fixture.sql,readonly" \
  --mount "type=bind,source=$root/testdata/clickhouse/access.xml,target=/etc/clickhouse-server/config.d/adapter-access.xml,readonly" \
  clickhouse/clickhouse-server:26.10.3.7@sha256:739e099abd1157390960a59e4532d21781d8fde57e6e4151cb920ea02c30b1ce >/dev/null
ready=false
for attempt in {1..60}; do
  # The image starts a temporary loopback server for init scripts, then stops
  # it and execs the final server as PID 1. A row count on the temporary server
  # can pass before account/profile initialization and the final restart.
  if (ulimit -f 2048; timeout --kill-after=1s 2s docker exec "$fixture" sh -c '
    case "$(cat /proc/1/comm)" in
      clickhouse|clickhouse-serv) ;;
      *) echo "ClickHouse image initialization is still running." >&2; exit 1 ;;
    esac
    exec clickhouse-client --user fixture_reader --password synthetic-reader --query "SELECT count() FROM explore.events"
  ' >"$diagnostics/readiness.out" 2>"$diagnostics/readiness.err") && grep -qx 3 "$diagnostics/readiness.out"; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'ClickHouse synthetic fixture did not initialize' >&2; exit 1; }
mapping=$(docker port "$fixture" 9000/tcp)
[[ "$mapping" =~ ^127\.0\.0\.1:[0-9]+$ ]] || exit 1
export CLICKHOUSE_TEST_ADDRESS="$mapping"
http_mapping=$(docker port "$fixture" 8123/tcp)
[[ "$http_mapping" =~ ^127\.0\.0\.1:[0-9]+$ ]] || exit 1
export CLICKHOUSE_TEST_HTTP_ADDRESS="$http_mapping"
export DATATUG_TEST_CLICKHOUSE_URL="clickhouse+http://$http_mapping/explore"
export DATATUG_CLICKHOUSE_USERNAME=fixture_reader
export DATATUG_CLICKHOUSE_PASSWORD=synthetic-reader
export DATATUG_CLICKHOUSE_RELATIONS='{"events":[]}'
export DATATUG_CLICKHOUSE_NATIVE_SQL=true
export DATATUG_TEST_CLICKHOUSE_BINARY="$diagnostics/datatug"
export GOMAXPROCS=2
export GOFLAGS="-p=2"
go build -o "$DATATUG_TEST_CLICKHOUSE_BINARY" .
go test -race -tags=clickhouseintegration -run '^TestClickHouseCLIJourney$' -count=1 -timeout=3m ./apps/datatugapp/commands
echo 'ClickHouse CLI journey passed'
echo '--- ClickHouse fixture: final resource events ---' >&2
if ! capture "$diagnostics/final-resource-events" docker exec "$fixture" sh -c '
  [ -r /sys/fs/cgroup/pids.events ] && [ -r /sys/fs/cgroup/memory.events ] || exit 1
  while read -r name value; do
    case "$name" in max) printf "pids_max_events %s\n" "$value" ;; esac
  done < /sys/fs/cgroup/pids.events
  while read -r name value; do
    case "$name" in
      oom) printf "memory_oom_events %s\n" "$value" ;;
      oom_kill) printf "memory_oom_kill_events %s\n" "$value" ;;
    esac
  done < /sys/fs/cgroup/memory.events
'; then
  echo 'Final resource event counters could not be read within the diagnostic limits.' >&2
  exit 1
fi
if ! LC_ALL=C awk '
  NF != 2 || $1 !~ /^(pids_max_events|memory_oom_events|memory_oom_kill_events)$/ || $2 !~ /^[0-9]+$/ { bad = 1 }
  { counts[$1]++; values[$1] = $2 }
  END {
    split("pids_max_events memory_oom_events memory_oom_kill_events", names, " ")
    for (i = 1; i <= 3; i++) if (counts[names[i]] != 1) bad = 1
    if (bad) exit 1
    for (i = 1; i <= 3; i++) print names[i], values[names[i]]
  }
' "$diagnostics/final-resource-events" >"$diagnostics/validated-resource-events"; then
  echo 'Final resource event counters were missing, duplicated or malformed.' >&2
  exit 1
fi
excerpt "$diagnostics/validated-resource-events"
if ! LC_ALL=C awk '$2 != 0 { exit 1 }' "$diagnostics/validated-resource-events"; then
  echo 'ClickHouse fixture encountered a PID denial or OOM event despite passing Go tests.' >&2
  exit 1
fi
