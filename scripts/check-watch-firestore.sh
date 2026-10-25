#!/usr/bin/env bash
# Own one isolated, loopback-only, in-memory Firestore emulator for this command.
set -euo pipefail
watch_scratch=$(mktemp -d "${TMPDIR:-/tmp}/datatug-watch-firestore.XXXXXX")
watch_artifacts=${WATCH_FIRESTORE_ARTIFACTS:-$watch_scratch}
mkdir -p "$watch_artifacts"
watch_jar=${WATCH_FIRESTORE_EMULATOR_JAR:-$watch_scratch/firestore-v1.22.0.jar}
watch_java=${WATCH_FIRESTORE_JAVA:-java}
watch_pid=''
watch_cleanup() {
  if [ -n "$watch_pid" ]; then
    kill "$watch_pid" 2>/dev/null || true
    wait "$watch_pid" 2>/dev/null || true
  fi
  rm -rf "$watch_scratch"
}
trap watch_cleanup EXIT INT TERM
if [ ! -f "$watch_jar" ]; then
  curl --fail --location --retry 2 --max-time 120 \
    https://storage.googleapis.com/firebase-preview-drop/emulator/cloud-firestore-emulator-v1.22.0.jar \
    --output "$watch_jar"
fi
python3 - "$watch_jar" <<'PY'
import hashlib, pathlib, sys
assert hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest() == '9b6498b7f62714d67f48f59b3818883cd682dbcd46b9f59511de81c97bb5166c', 'Firestore emulator digest mismatch'
PY
watch_port=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    print(sock.getsockname()[1])
PY
)
export FIRESTORE_EMULATOR_HOST="127.0.0.1:$watch_port"
export WATCH_FIRESTORE_GATE_REQUIRED=1
export WATCH_FIRESTORE_TEST_PROJECT=demo-datatug-watch-ci
printf 'Owner: check-watch-firestore.sh PID %s\nProject: %s\nEndpoint: %s\n' "$$" "$WATCH_FIRESTORE_TEST_PROJECT" "$FIRESTORE_EMULATOR_HOST" > "$watch_artifacts/lifecycle.txt"
"$watch_java" -jar "$watch_jar" --host 127.0.0.1 --port "$watch_port" --project_id "$WATCH_FIRESTORE_TEST_PROJECT" > "$watch_artifacts/emulator.log" 2>&1 &
watch_pid=$!
watch_ready=0
for ((watch_attempt=0; watch_attempt<200; watch_attempt++)); do
  if ! kill -0 "$watch_pid" 2>/dev/null; then
    cat "$watch_artifacts/emulator.log" >&2
    exit 1
  fi
  if curl --silent --max-time 1 "http://$FIRESTORE_EMULATOR_HOST/" >/dev/null; then
    watch_ready=1
    break
  fi
  sleep 0.1
done
if [ "$watch_ready" != 1 ]; then
  cat "$watch_artifacts/emulator.log" >&2
  exit 1
fi
# The required marker makes missing emulator configuration fatal, never SKIP.
go test -race ./pkg/watch -run '^TestFirestore(Store|Events|Registration)' -count=1 -timeout=180s -json > "$watch_artifacts/tests.jsonl"
python3 - "$watch_artifacts/tests.jsonl" <<'PY'
import json, pathlib, sys
rows = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines()]
tests = [row for row in rows if row.get('Test', '').startswith(('TestFirestoreStore', 'TestFirestoreEvents'))]
assert tests and not any(row['Action'] == 'skip' for row in tests), 'Firestore gate did not execute'
passed = {row['Test'] for row in tests if row['Action'] == 'pass'}
assert len(passed) >= 7, 'Missing Firestore acceptance cases'
assert {'TestFirestoreEventsTypedAtomicPrivacyPaginationAndAuthority', 'TestFirestoreEventsSameInstantLanesAndDurableRetention'} <= passed, 'Missing Work event acceptance cases'
# Keep the original Store/Events minimum independent of the registration cases.
registration = [row for row in rows if row.get('Test', '').startswith('TestFirestoreRegistration')]
assert registration and not any(row['Action'] == 'skip' for row in registration), 'Registration gate did not execute'
registration_passed = {row['Test'] for row in registration if row['Action'] == 'pass'}
assert {
    'TestFirestoreRegistrationExistingRunnerDoesNotScanQuota',
    'TestFirestoreRegistrationNewScopeAndProviderFailuresRemainAtomic',
    'TestFirestoreRegistrationRetryRechecksCurrentAuthority',
    'TestFirestoreRegistrationThreeExistingRunnersConcurrent',
    'TestFirestoreRegistrationConcurrentHundredRunnerCapacity',
} <= registration_passed, 'Missing Watch registration acceptance cases'

PY
printf 'Firestore Watch storage gate passed; emulator teardown owned by this command.\n'
