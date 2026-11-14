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
   under `ext` under `teams` (its id is `teams/ext/events`). Each definition used
   by the mirror keeps one record per file (`record_file.type: map[string]any`),
   names the file `{key}.json` (any prefix or suffix around `{key}` is kept) and
   uses the `json` format; a definition that does not is reported as
   `unsupported` and not copied.
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
   paths:                             # optional: the only Firestore paths a collection is copied from
     teams/ext: [teams/*/ext/calendar]
     teams/ext/events: [teams/*/ext/calendar/events]
   mask:                              # fields replaced before a record is written
     ext/customers: [email, name, contacts.*.phone]
   ```

   **A defined collection the policy does not name is not copied**, and every
   run lists it as `undeclared`, so a collection added to the mirror later is
   never copied by accident. Every name must be a defined collection, and a
   collection cannot be both copied and excluded. Unknown keys in the policy
   are an error.

   **`paths`** pins a copied collection to Firestore paths. A pattern is a
   collection path (`teams/*/ext/calendar/events`) or a document path
   (`teams/*/ext/calendar`); `*` stands for any document id, and every other id
   must be the one in the document's path. Use it whenever a document's shape
   depends on an id: when each module of `teams/{team}/ext/{module}` keeps
   different fields, a definition describes one module, and `paths` keeps the
   records of the other modules out. A document of the collection at another
   path is **not copied and not dumped**: the report counts it as an *other
   parent* (`other_parents`, with its path and every unpinned id shown as `*`).
   A pattern must have the collection's own collection ids.

   **`mask`** paths are dot-separated (`members.*.displayName`: `*` is every key
   of a map or every item of a list). A masked value keeps its kind where it can,
   so a required column still holds one: a string becomes `"***"`, a map `{}`
   and a list `[]`; a number, a bool, a timestamp or another tagged value is
   removed. The policy check refuses a mask path that does not
   name a declared field: its first name must be a column of the collection
   (`emial` is an error), and each next name must be something the column's type
   holds (an item, `*` or an index, of a `[]T`; any key of a `map[K]V`; anything
   below an `any`; nothing below a scalar). The check goes as deep as the
   column types say; below `any` (or a map of `any`) a path cannot be checked.
   Document ids and map keys are not values and cannot be masked.

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
| `--space ID` | Only the documents under `spaces/ID` (the same as `--path spaces/ID`). It assumes a database that keeps each space's data under a root collection named `spaces`; for any other layout use `--path`. |
| `--path P` | Only the documents at or below the Firestore path `P`: a document (`teams/t1`) or a collection (`teams/t1/ext`). A path no defined collection (at a path its `paths` allow) is under is an error. |
| `--dry-run` | Count, with Firestore count queries, and write nothing. |
| `--max-reads N` | Stop before more than `N` billed reads (default 10000; `0` is no limit). |
| `--page-size N` | Documents per query page (default 300). |
| `--progress` | One line on stderr per page read. |
| `--json` | The run's report as JSON on stdout. |
| `--keep-rejected` | Also write each rejected document, masked, under `.datatug-mirror/rejected/`. |
| `--prune` | Delete record files whose document no longer exists, in the collections this run read completely. |
| `--restart` | Start over instead of resuming a run that stopped at its read budget. |
| `--snapshot` | Read every document as it was when the run started (see below). |

Exit codes: `0` complete; `1` a read or write failed; `2` usage or policy error;
`3` finished but incomplete (the read budget was reached, also by a dry run,
documents were rejected, a selected collection is unsupported, or validation
found problems); `4` the source cannot be opened.

## What is read, and paid for

Every page of a query is billed one read per document (one when it returns
none); a count is billed one read per 1000 documents counted; a document read is
one read. The plan reads as narrowly as the selection allows:

- When every parent id of a collection is fixed, by `--space`/`--path` or by a
  `paths` pattern, the run queries that one collection
  (`collection teams/t1/ext/calendar/events`), and when the document id is fixed
  too it reads that document. **Nothing outside it is read.**
- Otherwise it runs one collection-group query per collection id, shared by the
  defined collections with that id, ordered by document name, and limited by a
  document-name range to the longest fixed path prefix (`spaces/s1` for
  `--space s1`). The range is exact: `spaces/s1` never reads `spaces/s10` or
  `spaces/s1-old`. With `--space` or `--path`, **nothing outside that path is
  read**.
- **A collection-group query still reads, and bills, every same-named collection
  inside its range**, wherever its parents are: the `events` of another module,
  or an `events` collection the mirror does not define. Such documents are not
  copied: the report lists them as *other parents* or as *read outside the
  selection*, so the cost is visible. Pin the parent ids in `paths` where they
  are known to avoid paying for them; a dry run shows the counts first (its
  counts of a group are of every collection it reads).

`--max-reads` is checked before each request, so a run never passes it; a dry
run caps each count at what the budget pays for (and then says `≥N`). A run that
stops at the budget leaves `.datatug-mirror/resume.json`: the position of each
read and the counts so far. **The next run with the same source, policy and
selection continues from there**, so a budgeted backup makes progress run by run
instead of reading the same first documents again; `--restart` starts over. A
finished run removes the file.

`--snapshot` reads every document as it was when the run started, so documents
changed during a long run do not mix two moments; Firestore keeps old versions
for an hour (7 days with point-in-time recovery). A resumed run reuses the
first run's time while it is under 55 minutes old, and otherwise says that its
parts were read at different times. Counts are not read at the snapshot time.

## What a run writes

- One JSON file per document, at the path its definition gives, with sorted
  keys and a fixed format, so **a re-run over unchanged data changes no file**
  (only changed records are rewritten). A document whose parent document does
  not exist is still written at its path, and counted as `below a missing parent`.
- The definitions of the mirror (only the definition files; the mirror's own
  example records are not copied), so the output is a complete inGitDB database.
- `.datatug-mirror/report.json`: per collection, the action, reads, records,
  rejected documents, other parents and masked fields, plus the root
  collections the mirror does not define, documents read outside the selection,
  and record files whose document no longer exists (`stale`: reported; deleted
  only with `--prune`, and only for a collection this run read from its start to
  its end, so a run stopped by its budget deletes nothing it did not see).
- **Rejected documents** (an undeclared field, a missing required field, a wrong
  type, a value a column does not allow, a date that does not parse, an id too
  long for a file name, a name that differs only in case from another's) are
  not written into the database. The report keeps their path, the reasons and
  the names of the fields concerned, **never their values** (a reason that would
  quote a value, such as an enum check, names only the field). Only
  `--keep-rejected` writes the masked record under
  `.datatug-mirror/rejected/<path>.json`.

The output is then checked by inGitDB's own validator (the checks of `ingitdb
validate`), and its findings are reported, without values.

### File names

A Firestore id can hold any character but `/` and be up to 1500 bytes long; a
file name cannot. Every id (of a document and of each parent) is written by one
reversible encoding: `A-Z a-z 0-9 - _ . @ + = ,` stay as they are, and every
other byte (including the bytes of non-ASCII letters), a `.` at the start or the
end, and the first letter of a Windows device name (`CON`, `NUL`, `COM1`, ...)
become `~XX`, the byte in upper-case hex (`.hidden` is `~2Ehidden`, `a:b` is
`a~3Ab`, `Zürich` is `Z~C3~BCrich`, `CON` is `~43ON`). So no id can leave its
folder, be hidden, or clash with a reserved name, and the run reads its files
back to the same ids. Two ids that differ only in case (`c` and `C`) would be
one file on macOS and Windows: the second is rejected and named. An id whose
file name would be longer than 255 bytes is rejected too. Nothing aborts the run.

inGitDB takes a record's key from its file name as it is, so **a record's
inGitDB key is the encoded id**: the same as the Firestore id for ids made of
the characters above, `~2Ehidden` for `.hidden`. (inGitDB has no decoding of
keys; until it has one, compare encoded keys.)

### Values

Values keep their Firestore type: timestamps are RFC 3339 text in UTC with
exactly 6 fraction digits (Firestore keeps microseconds), so they sort as text.
In a column typed `datetime`, or an item of a `[]datetime` column, a timestamp is
that text; everywhere else (in an `any`, a map or a list) it is
`{"$time": "2026-11-01T09:00:00.000000Z"}`, so it is not read back as a string.
The types JSON has no form for are tagged objects too: `{"$bytes": "<base64>"}`,
`{"$ref": "teams/t1"}`, `{"$geo": {"latitude": 1, "longitude": 2}}`,
`{"$vector": [..]}`, and `{"$float": "NaN" | "Infinity" | "-Infinity"}`; a map of
the source whose only key is one of these tags is written as `{"$map": {...}}`.
Whole doubles keep a `.0`, so an int and a double stay distinct.

What is not kept: a string written into a `datetime` column by an application
stays a string (and must parse, or the document is rejected); integers beyond
2^53 are exact in the file, but most JSON readers decode them as doubles and
lose digits; and masking removes or replaces what it masks.

## Safety

- **Read-only, enforced at the client.** Every request of the client the
  command opens passes a filter that lets through only the four read RPCs
  (`RunQuery`, `RunAggregationQuery`, `BatchGetDocuments`,
  `ListCollectionIds`); any other, a write, a transaction or a listen, is
  refused before it is sent, whatever code holds the client. Tests send writes
  through that client to a fake server and to the emulator and assert they are
  refused and never arrive; a test also parses the package source and fails on
  any call named like a Firestore write method. Use a read-only identity as
  well (for example `roles/datastore.viewer`).
- **Credentials only from the environment**: Google Application Default
  Credentials (`gcloud auth application-default login`, or
  `GOOGLE_APPLICATION_CREDENTIALS`), and the source URL from `--from` or the
  variable it names. Nothing is stored in the project.
- **Out of git, checked closed.** The output must be outside the mirror. It is
  allowed only when it is outside any git work tree, or when git ignores it by a
  (non-negated) rule of a `.gitignore` file that the repository tracks, so
  every clone ignores it too (`/data/` in the project's `.gitignore`). An
  output ignored only by this machine (the global ignore file,
  `core.excludesFile`, `.git/info/exclude`) or by an untracked `.gitignore`, an
  output git does not ignore, and any git failure all refuse the run; git runs
  without the `GIT_DIR`/`GIT_WORK_TREE` of the environment. Without git
  installed, an output below a `.git` folder is refused. The rejected records
  `--keep-rejected` writes are inside the output, under the same rule.
- **Bounded cost.** `--max-reads` stops a run before the budget is passed,
  `--dry-run` shows the size of a copy first, and runs resume.

The emulator enforces no index; if a production database lacks an index a query
needs, Firestore refuses it with `FAILED_PRECONDITION` and a link that creates
the index, and the run stops with exit code 1 (the records written so far stay,
and a re-run once the index is built resumes after a budget stop). Try
`--dry-run` first: it runs the same queries as counts. For tests, set
`FIRESTORE_EMULATOR_HOST` and use a `demo-` project; the run prints that it
reads the emulator.
