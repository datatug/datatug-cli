# CLI for DataTug agent

You can download DataTug agent or build it from source codes.

## How to run

To display help: 
> datatug --help

## Commands:

- init
- scan
- serve

### Command: `init`

### Command: `scan`

### Command: `serve`

### Recover an interrupted session run

`session send --op <original-operation-id>` reads accepted work and never repeats its provider call. Inspect an uncertain run and the current session before deciding whether to resolve it:

```sh
datatug session --project my-project run run-id
datatug session --project my-project show session-id
```

To explicitly mark an accepted **client** run as outcome unknown, save the following as `resolve-unknown.json`. Replace the session/run IDs, kind, and both versions with the values just read. Use a unique operation ID for this decision; keep it unchanged when recovering its acknowledgement.

```json
{
  "opId": "resolve-interrupted-001",
  "action": "reportClientRun",
  "sessionId": "session-id",
  "runId": "run-id",
  "kind": "chat",
  "expectedVersion": 2,
  "expectedRunVersion": 1,
  "runState": "outcome-unknown",
  "errorCode": "CLIENT_OUTCOME_UNKNOWN"
}
```

```sh
datatug session --project my-project command --input resolve-unknown.json
```

This records an explicit unknown outcome and releases the session's pending-run count. It executes no source or provider work and attaches no result. Server-owned runs reject this command. An intentional rerun requires a new operation ID.
