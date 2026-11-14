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
   under `ext` under `teams` (its id is `teams/ext/events`). **The definition
   decides how records are stored**, and the mirror writes what it declares (see
   [Storage forms](#storage-forms)); a definition in a form the mirror does not
   write is reported as `unsupported` and not copied.
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
     ext/customers: [email, name, contacts]
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
   and a list `[]`, a whole untyped (`any`) column `"***"` whatever it holds; a
   number, a bool, a timestamp or another tagged value is removed (a list item
   becomes `null`). The policy check refuses a mask path that does not name a
   declared field: its first name must be a column of the collection (`emial`
   is an error), and each next name must be something the column's type holds
   (an item, `*` or an index, of a `[]T`; any key of a `map[K]V`; nothing below
   a scalar). **A path below an untyped value is refused** (`settings.phone`
   when `settings` is `map[string]any`, `contacts.*.phone` when `contacts` is
   `[]any`): it cannot be checked, so a typo there would leave the data in
   clear; mask the untyped value as a whole (`settings`, `contacts`).
   Document ids and map keys are not values and cannot be masked.

   **A mask never makes the mirror lose a whole record.** The check also refuses
   a mask whose masked value the definition would refuse, which would get every
   record holding it rejected: a required number or bool (removed, so
   missing), a column with an `enum`, a length that `"***"` breaks, a `format`
   or a `foreign_key`, and the items of a list of numbers or bools (`null`).
   The error says to exclude the collection instead, or to leave the column
   unmasked. (The mirror writes no placeholder value: there is no neutral value
   of a number, a bool or an enum that would not read as data.)

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
| `--keep-rejected` | Also write each rejected document under `.datatug-mirror/rejected/`, with only the policy's masks applied: **its undeclared fields and the values that break its definition are written in clear**. The run prints a warning line when it is used. Off by default. |
| `--continue-on-long-ids` | Do not stop at a document whose id is too long for one file per record: copy the rest, count it in the report, exit 3. |
| `--prune` | Delete record files whose document no longer exists, in the collections this run read completely. |
| `--restart` | Start over instead of resuming a run that stopped at its read budget. |
| `--snapshot` | Read every document as it was when the run started (see below). |

Exit codes: `0` complete; `1` a read or write failed, or a document's id is
too long for its collection's one-file-per-record form; `2` usage or policy
error (also an output of another source);
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
read, the counts so far, and the record files and folders written (case-folded,
so a resumed run still finds a name that differs only in case). **The next run
with the same source, selection, definitions and policy continues from
there**, so a budgeted backup makes progress run by run instead of reading the
same first documents again. It compares digests of each (the definitions'
files by content, the policy's copy, exclude, mask and paths): when one
changed, it says which and starts over. `--restart` starts over too. A finished
run removes the file.

`--snapshot` reads every document as it was when the run started, so documents
changed during a long run do not mix two moments; Firestore keeps old versions
for an hour (7 days with point-in-time recovery). A resumed run reuses the
first run's time while it is under 55 minutes old, and otherwise says that its
parts were read at different times. Counts are not read at the snapshot time.

## What a run writes

- The records, in the form and format their definitions declare
  ([Storage forms](#storage-forms)), with sorted keys and a fixed format, so
  **a re-run over unchanged data changes no file** (only changed records are
  rewritten). A document whose parent document does not exist is still written
  at its path, and counted as `below a missing parent`.
- `.datatug-mirror/output.json`: which database the output is a mirror of
  ([One output, one database](#one-output-one-database)) and the key encoding
  of its file names.
- The definitions of the mirror (only the definition files; the mirror's own
  example records are not copied), so the output is a complete inGitDB database.
- `.datatug-mirror/report.json`: per collection, the action, reads, records,
  rejected documents, other parents and masked fields, plus the root
  collections the mirror does not define, documents read outside the selection,
  and records whose document no longer exists (`stale`: a file, or
  `<file>#<encoded id>` for a record kept in one file; reported, and deleted only
  with `--prune`, and only for a collection this run read from its start to its
  end, so a run stopped by its budget deletes nothing it did not see).
- **Rejected documents** (an undeclared field, a missing required field, a wrong
  type, a value a column does not allow, a date that does not parse, a name that
  differs only in case from another's) are not written into the database. The
  report keeps their path, the reasons and the names of the **declared** fields
  concerned, **never their values** (a reason that would quote a value, such as
  an enum check, names only the field). A field name the definition does not
  declare, and a map key, can be data (an email used as a key), so neither is
  shown: the report gives their count and a short fingerprint (an HMAC keyed by
  a random value of the run, so it cannot be looked up, and equal sets of names
  have equal fingerprints within one run); a value of a type the mirror cannot
  write is named by its column alone. Only `--keep-rejected` writes the record,
  with only the policy's masks applied, under
  `.datatug-mirror/rejected/<path>.json` (or notes that its path is too long).

The output is then checked by inGitDB's own validator (the checks of `ingitdb
validate`), and its findings are reported, without values.

### Storage forms

The schema decides the storage form, never the mirror. A collection's
definition declares it in `record_file`, and the mirror writes:

| `record_file.type` | Written as | Keys |
|---|---|---|
| `map[string]any`, `name` with `{key}` alone (`{key}.yaml`) | one file per record | the encoded id ([Key encoding](#key-encoding)) |
| `map[$record_id]map[$field_name]any`, a fixed `name` (`members.yaml`) | all records of the collection in one file; for a sub-collection one file per parent document | the real document ids, as map keys |
| `[]map[string]any` | not written (`unsupported`): a list keeps no Firestore id | |

The format is `yaml` (or `yml`) or `json`; another format is `unsupported`. A
parent of a sub-collection must keep one file per record, since its records are
the folders of the sub-collection. Defaults, in the owner's words: "By default
we export to YAML", and "default is all records in single yaml file": when the
mirror writes a definition, it is one YAML file of all records,

```yaml
record_file:
  name: members.yaml
  type: "map[$record_id]map[$field_name]any"
  format: yaml
```

inGitDB itself requires every definition to declare its `record_file`, and the
mirror reads only existing definitions, so **a declared form always wins**: a
mirror whose definitions declare one JSON file per record is written that way.

**One file per record holds an id only when its file name fits.** In the
owner's words: "if schema record per file and we hit long id we fail". A
document whose file name (the encoded id, with the name's prefix and suffix)
is over 255 bytes, or whose path is longer than the system writes (259
characters on Windows, 1023 elsewhere), **fails the run**: it stops at that
document with exit code 1 and an error naming the collection, the document and
the length, and the fix (declare the collection with all its records in one
file). Its report says `stopped:` and is incomplete; its resume state is kept,
so the next run starts again at that document's page. Only
`--continue-on-long-ids` copies the rest instead: each such document is counted
as `not_copied_id_too_long` (never silently skipped) and the run exits 3. A dry
run, and a run before its first read, fail on the ids they know up front: those
of `--space`/`--path` and those the policy's `paths` name. The ids of documents
a query returns are known only once read (finding them in a dry run would cost
a full read), so the run finds those.

**One YAML file of all records.** Its top-level keys are the real document ids
(any id, of any length: a key is not a file name), in byte order, each holding
its record; a record is written, masked and validated exactly as a file of its
own would be (inGitDB's validator gets the real id as its key). The file is
rewritten (atomically: a temporary file, then a rename) only when the
collection, or the collection-group query that reads it, has been read to the
end. A run that stops at its budget in the middle keeps what it read as chunks
under `.datatug-mirror/partial/` (which inGitDB does not read), one small file
per page and record file, each record written once; **the record file itself
is not written, so no half-read collection looks complete**. The run that
resumes reads the chunks back and writes the whole file. Names that differ only
in case are two keys of one map, which no filesystem folds, so the case check
covers only the folders of the parents. A record no longer in the source stays
in the file and is reported as stale (`<file>#<id>`); `--prune` removes its key,
and the file when no key is left.

Size and memory, honestly: the records of every file of the collection being
read stay in memory until that collection is read to the end, and each file is
then encoded and written whole. For 100k records of about 1 KB that is about
100 MB of YAML, a few hundred MB of memory while it is read, and one write of
the whole file; a re-run over unchanged data rewrites nothing. A collection far
larger than memory should keep one file per record.

### YAML

The mirror writes its own canonical YAML (keys in byte order, two-space
indent): a string is always read back as a string (one that would read as a
number, a bool, a date or null, such as `"123"`, `"true"`, `"2026-01-01"`,
`"null"`, `"~"`, is quoted), text with line breaks is a literal block (or a
double-quoted string when it has a carriage return, a control character, or
spaces or line breaks at its ends), a double always has a decimal point or an
exponent (`1.0`), int64 is exact, and `$time`, `$map` and the other tags are
plain keys. The tests read every such value back with inGitDB's own parser.

### Key encoding

A Firestore id can hold any byte but `/` and be up to 1500 bytes long; a file
name cannot. Every id written as a file or folder name (a document of a
one-file-per-record collection, and each parent document) is written by the
encoding `tilde-hex/v1`, recorded in `.datatug-mirror/output.json`. It is to be
declared in the inGitDB schema and implemented once in ingitdb-go; the mirror
implements it until then. Exactly:

- **Encode** an id (a non-empty byte string with no `/`) byte by byte. A byte
  is kept when it is `A-Z`, `a-z`, `0-9` or one of `-` `_` `.` `@` `+` `=` `,`,
  except: a `.` that is the first or the last byte, and the first byte of an id
  that is a Windows device name (`CON`, `PRN`, `AUX`, `NUL`, `COM0`-`COM9`,
  `LPT0`-`LPT9`, in any case, alone or before a `.`: `con.json`, `COM1.txt`).
  Every other byte, `~` itself, every byte of a non-ASCII character, and those
  exceptions become `~` and the byte as two upper-case hex digits.
- **Decode** a name: `~` and two upper-case hex digits are that byte; every
  other byte is itself. A name is a valid encoding only when it decodes to a
  non-empty id **and** encoding that id gives the name back (so `~41`, `a~3a`
  and `~` alone are no names; each id has exactly one name, and no two ids share
  one).
- Examples: `.hidden` is `~2Ehidden`, `x.` is `x~2E`, `a:b` is `a~3Ab`, `a~b` is
  `a~7Eb`, `Zürich` is `Z~C3~BCrich`, `CON` is `~43ON`, `con.json` is
  `~63on.json`, `..\x` is `~2E.~5Cx`.
- The name of a one-file-per-record file is the `record_file.name` with `{key}`
  replaced by the encoded id, and must be at most 255 bytes.

So no id can leave its folder, be hidden, or clash with a reserved name, and
the run reads its files back to the same ids. Firestore's generated ids and the
usual slugs do not change. Two ids that differ only in case (`c` and `C`) would
be one file on macOS and Windows: the second is rejected and named, also when a
resumed run reads it, and also for two parents whose folders differ only in
case. inGitDB takes a one-file-per-record key from its file name as it is, so
**such a record's inGitDB key is the encoded id** until inGitDB decodes keys by
the schema-declared encoding.

### One output, one database

The first run into an output records the database it reads in
`.datatug-mirror/output.json`: the resolved project id, the database id, and
the emulator host when `FIRESTORE_EMULATOR_HOST` is set (never a credential).
**Every later run into that output, its resume and its `--prune`, must read the
same database**, or it is refused before anything is read or written: another
project or database, the emulator instead of production (or another emulator,
or production instead of an emulator), or `--from env:NAME` whose variable now
names another project. So a budget-stopped production backup is never resumed
from an emulator, and `--prune` from an emulator never deletes production
records; an output created from an emulator is pruned only from that emulator.
An output folder that is not empty and has no `output.json` is refused too (also
with `--restart`): choose an empty folder.

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
  output git does not ignore, and any git failure all refuse the run. **The
  rule must be committed**: a `.gitignore` with changes not in `HEAD`, staged or
  not, does not count (other clones would not have them). Git runs
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
