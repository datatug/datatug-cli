# Run local Checks in automation

`checks run` executes existing saved Detective Checks through the existing
saved-query runner, records private evidence, emits one safe JSON summary and
exits. Bare `checks` still opens the interactive workspace. No listener or
daemon starts, including when `DATATUG_CLIENT` names an agent mod.

Use only trusted project files. This preview supports Checks without input
declarations, a local filesystem project, and an explicitly selected environment.
It requires `--local-owner` consent to unrestricted local-owner execution.
Projects or deployment profiles with access policies are rejected: named
principals and absent policies are not consent, and secured Check asset grants
are not yet supported. Keep credentials in private source configuration; do not
place them in arguments or project files committed to a repository.

The checked-in `testdata/checks-demo` contains only synthetic data in SQLite,
portable relative source configuration, saved TugQL queries, and real Check
validators. With a built CLI, this command is runnable from any working directory:

```sh
project=/absolute/path/to/datatug-cli/testdata/checks-demo
private_dir=$(mktemp -d)
chmod 700 "$private_dir"
DATATUG_TELEMETRY=0 datatug checks run --project "$project" --env local \
  --check pass --local-owner --evidence-dir "$private_dir" \
  --timeout 30s --warnings fail --format json
```

`pass` expects one row, `fail` expects two rows, `warning` expects two rows with
warning severity, `unknown` has no validators, and `source-error` selects an
absent table. Repeat `--check` for sequential execution, at most 100 Checks.
Business outcomes continue through the requested list; infrastructure errors,
timeout, cancellation or invalid configuration stop dispatch. Every explicit
rerun creates new operation identities; there are no automatic retries.

`--timeout` defaults to 30 seconds, must be positive and at most one hour, and
covers private store lock acquisition and execution. Cancellation and SIGTERM
propagate to existing query execution. Synchronous filesystem operations and
library/driver shutdown must return cooperatively. The evidence directory must
already exist, use an absolute path, have no group/other permission bits, and be
physically outside the project, including symlink aliases. Do not share it with
untrusted processes. Preserve it privately to investigate uncertain executions;
never upload it as a CI artifact. Delete a temporary directory when evidence is
no longer needed.

The public envelope has `schemaVersion: "datatug.checks.run/v1"`, `status`,
`exitCode`, `checks`, `deepLink` and `deepLinkReason`. Each Check contains only its
one-based `index`, status, executionStatus, outcome, severity, and durationMs.
It deliberately excludes IDs, titles, definitions, inputs, rows, paths, private
run references, fingerprints and raw errors. `executionStatus` and `outcome`
retain the existing engine semantics; warning policy affects CI disposition.
A Check whose source cannot execute is an infrastructure error even if its
unavailable-evidence policy changes the business outcome.

| Status | Exit | Meaning |
| --- | --- | --- |
| pass | 0 | Completed with true validators |
| fail | 1 | Completed with a business-false validator |
| warning | 1, or 0 with `--warnings ignore` | Completed with warning/info severity business false |
| unknown | 1 | No conclusive business result |
| configuration_error | 2 | Invalid flags, environment, definition or evidence location |
| not_found | 3 | Selected Check is absent |
| error | 4 | Execution, evidence recording or I/O failed |
| access_denied | 5 | Explicit owner consent absent or secured policy profile |
| timeout | 124 | Invocation deadline reached |
| cancelled | 130 | Caller cancellation, SIGINT or SIGTERM |

Within the array, `executionStatus` is `completed`, `error`, `skipped`, `pending`
or `running`; `outcome` is `pass`, `fail` or `unknown`; `severity` is `critical`,
`error`, `warning`, `info` or empty. Invalid engine enum strings are never copied.
The invocation status gives unknown precedence over fail, and fail precedence
over warning, so an ignored warning cannot hide a later unresolved result.

The only hosted link is `https://datatug.app`, with
`deepLinkReason: "hosted_result_reference_unsupported"`. This opens the hosted
home experience. It does not point to a CI result page, expose private evidence,
or claim hosted result synchronization. A permission-checked hosted result
reference remains a release blocker for integrations that require it.

Focused verification:

```sh
wb run -- go test ./apps/datatugapp/commands -run '^TestChecksRun' -count=1
wb run -- go test ./pkg/detective -run '^TestNewLocalStoreContext' -count=1
wb run -- go test ./pkg/server/endpoints -run '^TestWatchExecutor' -count=1
```
