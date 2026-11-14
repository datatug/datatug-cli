# Mirror Firestore into inGitDB (`db mirror`)

`datatug db mirror` copies a chosen part of a Firestore database into an inGitDB
database, **read-only**, laid out exactly as the inGitDB definitions of a
*mirror* say. It is the way to back up Firestore, or a slice of it, into files:
`db copy` and `db export` need a schema the source can describe, and Firestore
has none.

## What it needs

1. **A mirror**: an inGitDB database whose collection definitions follow the
   Firestore collection paths. The documents of
   `teams/{team}/ext/{module}/events/{event}` are the collection `events` defined
   under `ext` under `teams` (its id is `teams/ext/events`). Collection groups
   are read across every parent, at any depth. Each definition used by the mirror
   keeps one record per file (`record_file.type: map[string]any`), names the file
   `{key}.json` (any prefix or suffix around `{key}` is kept) and uses the `json`
   format; a definition that does not is reported as `unsupported` and not copied.
2. **A mirror policy** in the DataTug project (one YAML file; `format:
   datatug-mirror-policy/v1`). It says what may leave Firestore:

   ```yaml
   format: datatug-mirror-policy/v1
   definitions: ../firestore          # the mirror (relative to this file)
   output: ../data/prod-backup        # optional; default data/<policy name> in the project
   copy:                              # collections that are copied
     - teams
     - teams/ext
     - teams/ext/events
     - ext/customers
   exclude:                           # collections that are never read
     - ext/secrets
   mask:                              # fields replaced before a record is written
     ext/customers: [email, name, contacts.*.phone]
   ```

   **A defined collection the policy does not name is not copied**, and every
   run lists it as `undeclared`, so a collection added to the mirror later is
   never copied by accident. Every name must be a defined collection, and a
   collection cannot be both copied and excluded. A mask path is dot-separated
   (`members.*.displayName`: `*` is every key of a map or every item of a
   list); a masked string becomes `"***"`, any other masked value is removed.
   Unknown keys in the policy are an error.

## Commands

```sh
export DATATUG_FIRESTORE_SOURCE_URL='firestore://my-project'     # or firestore://PROJECT/DATABASE

# Count the documents of each selected collection; nothing is written.
datatug db mirror --from env:DATATUG_FIRESTORE_SOURCE_URL --policy mirrors/backup.yaml --dry-run

# Copy one space (the documents under spaces/<ID>) into its own folder.
datatug db mirror --from env:DATATUG_FIRESTORE_SOURCE_URL --policy mirrors/backup.yaml \
  --space s1 --to data/space-s1

# Copy everything the policy allows.
datatug db mirror --from env:DATATUG_FIRESTORE_SOURCE_URL --policy mirrors/backup.yaml --progress
ingitdb validate --path data/backup
```

`--from` also takes `firestore://PROJECT[/DATABASE]` directly, or a
`datatug://PROJECT/DB?env=ENV` reference to a project source whose connection
descriptor names the environment variable (`{"dsnEnv": "DATATUG_FIRESTORE_SOURCE_URL"}`).

| Flag | Meaning |
|------|---------|
| `--policy` | The mirror policy (required). |
| `--definitions` | The mirror folder, instead of the policy's `definitions`. |
| `--to` | Output folder (or `ingitdb://folder`), instead of the policy's `output`. |
| `--collections` | Comma-separated collections of the policy's `copy` list; `teams/**` adds every collection below `teams`. An excluded or undeclared name is an error. |
| `--space ID` | Only the documents under `spaces/ID` (the same as `--path spaces/ID`). |
| `--path P` | Only the documents at or below the Firestore path `P`: a document (`teams/t1`) or a collection (`teams/t1/ext`). A path no defined collection is under is an error. |
| `--dry-run` | Count, with Firestore count queries, and write nothing. |
| `--max-reads N` | Stop before more than `N` billed reads (default 10000; `0` is no limit). |
| `--page-size N` | Documents per query page (default 300). |
| `--progress` | One line on stderr per page read. |
| `--json` | The run's report as JSON on stdout. |

Exit codes: `0` complete; `1` a read or write failed; `2` usage or policy error;
`3` finished but incomplete (the read budget was reached, documents were
rejected, a selected collection is unsupported, or validation found problems);
`4` the source cannot be opened.

## What a run writes

- One JSON file per document, at the path its definition gives, with sorted
  keys and a fixed format, so **a re-run over unchanged data changes no file**
  (only changed records are rewritten). A document whose parent document does
  not exist is still written at its path, and counted as `below a missing parent`.
- The definitions of the mirror (only the definition files; the mirror's own
  example records are not copied), so the output is a complete inGitDB database.
- `.datatug-mirror/report.json`: per collection, the action, query, records,
  rejected documents and masked fields, plus the root collections the mirror
  does not define, documents read outside the selection, and output files whose
  document no longer exists (`stale`: reported, never deleted).
- `.datatug-mirror/rejected/<path>.json`: each document that does not fit its
  definition (an undeclared field, a missing required field, a wrong type), with
  the reasons in the report. Rejected documents are not written into the database.

The output is then checked by inGitDB's own validator (the checks of `ingitdb
validate`), and its findings are reported.

Values keep their Firestore type without loss: timestamps are RFC 3339 strings
in UTC (the definition's `datetime` type says what they are), and the types JSON
has no form for are tagged objects: `{"$bytes": "<base64>"}`,
`{"$ref": "teams/t1"}`, `{"$geo": {"latitude": 1, "longitude": 2}}`,
`{"$vector": [..]}`, and `{"$float": "NaN" | "Infinity" | "-Infinity"}`.
Whole doubles keep a `.0`, so an int and a double stay distinct.

## Safety

- **Read-only.** The command reads through four calls only: list root
  collections, run a query page, get one document, and count. The package has no
  write call: a test parses its source and fails on any call named like a
  Firestore write method (`Set`, `Create`, `Update`, `Delete`, `Commit`,
  `Batch`, `BulkWriter`, `RunTransaction`, `Add`, `NewDoc`, `BatchWrite`), and
  another runs a whole mirror against a Firestore server that refuses and counts
  every write RPC, and asserts the count is zero. Use a read-only identity as
  well (for example `roles/datastore.viewer`).
- **Credentials only from the environment**: Google Application Default
  Credentials (`gcloud auth application-default login`, or
  `GOOGLE_APPLICATION_CREDENTIALS`), and the source URL from `--from` or the
  variable it names. Nothing is stored in the project.
- **Out of git by default.** The output must be outside the mirror, and when it
  is inside a git repository, git must ignore it (`data/` in `.gitignore`), or
  the run refuses to start.
- **Bounded cost.** Each page and each count is a billed read; `--max-reads`
  stops a run before the budget is passed, and `--dry-run` shows the size of a
  copy first.

Every query of a collection below the root is a collection-group query ordered
by document name, with a document-name range for `--space` and `--path`. The
emulator enforces no index; if a production database lacks an index such a query
needs, Firestore refuses it with `FAILED_PRECONDITION` and a link that creates
the index, and the run stops with exit code 1 (the records written so far stay;
re-run once the index is built). Try `--dry-run` first: it runs the same queries as counts.
For tests, set `FIRESTORE_EMULATOR_HOST` and use a `demo-` project; the run
prints that it reads the emulator.
