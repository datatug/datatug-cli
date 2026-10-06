# DataTug CLI & agent for web UI

<table border=0>
    <tr>
        <td><img src="https://github.com/datatug/.github/raw/main/datatug-logo-3.2.png"></td>
        <td>
            <p>
                DataTug is an open-source, CLI-first data exploration platform with a Web UI, designed to help you explore, query, and
                connect data across multiple sources without losing context. It automatically surfaces related data — even across
                different systems — so you can move naturally between datasets, queries, and results.
            </p>
            <p>
                Free for personal use, DataTug keeps your workflows transparent, versioned, and portable, whether you work locally, in
                GitHub, or in the cloud.                
            </p>
        </td>
    </tr>

</table>

## Quick start

Install the CLI (macOS and Linux with Homebrew; the other ways are in [Installation](#installation)):

```bash
brew install --cask datatug/tap/datatug
```

Then scan a database, look at what was scanned, and run a query. Every command and every output block below is run
by a test of this repository ([quickstart_doc_test.go](apps/datatugapp/commands/quickstart_doc_test.go)), so what you
read here is what the CLI prints. The test runs the commands in the process of the test, and runs step 4 against a
stand-in for the PostgreSQL server (see there).

**1. Scan a database.** The sample is a SQLite file made with the `sqlite3` program (any SQLite file works). `sqlite3`
comes with macOS; on Linux install the package `sqlite3` (`apt install sqlite3`, `dnf install sqlite`), or use a SQLite
file you have. The scan writes a project, a folder of plain files, and prints its progress on stderr and nothing on
stdout. The first time the CLI runs in a terminal it also prints a short notice about anonymous usage telemetry on stderr,
once; [Telemetry](#telemetry) says what is sent and how to turn it off.

```console
$ sqlite3 shop.db "
CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, FirstName TEXT NOT NULL, LastName TEXT);
CREATE TABLE Invoice (InvoiceId INTEGER PRIMARY KEY, CustomerId INTEGER, Total NUMERIC);
CREATE VIEW CustomerNames AS SELECT CustomerId, FirstName || ' ' || LastName AS FullName FROM Customer;
INSERT INTO Customer VALUES (1, 'Ada', 'Lovelace'), (2, 'Alan', 'Turing');
INSERT INTO Invoice VALUES (1, 1, 12.5), (2, 2, 40);
"
$ datatug scan -d shop-project -D sqlite3 --path shop.db --db shop --env local
```

**2. Look at what was scanned.** `datatug show` lists the project: each environment, each source with its driver, each
schema, each table and view with its columns, their types and their place in the primary key (`pk`; `pk 2` is the
second column of a key of several). `--format json` prints the same as one JSON document.

```console
$ datatug show -d shop-project
Project shop-project
Environment local
  Source shop (sqlite3)
    Schema main
      Table Customer
        CustomerId INTEGER pk
        FirstName TEXT
        LastName TEXT
      Table Invoice
        InvoiceId INTEGER pk
        CustomerId INTEGER
        Total NUMERIC
      View CustomerNames
        CustomerId INTEGER
        FullName -
```

**3. Run a query.** The query reads the database file; `--no-policies` runs it without [access policies](spec/features/cli/query/README.md)
(the line `access: running without access policies` goes to stderr).

```console
$ datatug query run --db sqlite://./shop.db --from Customer --no-policies
$key               CustomerId  FirstName  LastName
__dalgo_record_id  1           Ada        Lovelace
__dalgo_record_id  2           Alan       Turing
```

**4. Scan PostgreSQL the same way.** Put the connection URL in an environment variable whose name starts with
`DATATUG_`, and pass the name of the variable with `--driver postgres --dsn-env`. The project stores the name of the
variable and never the URL: the host, the port, the user and the password stay in your environment. A PostgreSQL scan
reads every schema the role can use (the system schemas apart), and `show` lists each schema, with its views as views.
The output below is for a database with the two tables Customer and Invoice in the schema `public`; the output for your
database lists its own schemas and tables. The test of this README runs this step against a stand-in for the
server, and the scan of a real server is tested by the CI job "Journey (PostgreSQL <major>)", on PostgreSQL 17 and 18.

```console
$ export DATATUG_SHOP_URL='postgres://USER:PASSWORD@localhost:5432/shop'
$ datatug scan -d shop-pg-project --driver postgres --dsn-env DATATUG_SHOP_URL --db shop --env prod
$ datatug show -d shop-pg-project
Project shop-pg-project
Environment prod
  Source shop (postgres, URL in $DATATUG_SHOP_URL)
    Schema public
      Table Customer
        CustomerId int pk
        FirstName string
        LastName string
      Table Invoice
        InvoiceId int pk
        Total decimal
```

What else works, and what does not yet, is in [Supported databases](#supported-databases).

![datatug-cli-employees-2.png](docs/screenshots/datatug-cli-employees-2.png)

<!-- dev-approach:v1 -->
## Export a database to inGitDB

`datatug db export` writes each supported DALgo source collection into a native inGitDB project. SQLite supplies a streaming row reader; other providers may materialize a collection in their query reader. For a local SQLite database:

```bash
datatug db export --from "sqlite://$PWD/shop.db" --to "ingitdb://$PWD/shop-ingitdb"

# Choose one records format for every exported table (default: json).
datatug db export --from "sqlite://$PWD/shop.db" --to "ingitdb://$PWD/shop-csv" --records-format csv
ingitdb validate --path ./shop-ingitdb
```

The destination directory must not exist. DataTug builds a complete project beside it and publishes the directory only after all collections succeed; a failed export leaves no completed destination. A source without a primary key receives export ordinal record IDs, marked as transport IDs rather than source keys. Those IDs are reproducible for a fixed source snapshot with deterministic provider row order; SQLite's source reader uses rowid order for keyless tables. Decimal values use string transport and binary values use base64; source schema, indexes, foreign-key declarations, and views are retained as descriptive metadata when the provider exposes them.

`--records-format` accepts `json`, `jsonl`, `ingr`, `csv`, and `yaml` (also `yml`), case-insensitively. JSON, INGR, and YAML use record-ID-keyed maps; JSONL and CSV carry a separate `$ID` transport key. CSV uses typed JSON-literal cells, preserving null versus empty text, exact integers, and quoted values.

The export uses DataTug's DALgo source URL dispatcher, including local `sqlite://`, native `ingitdb://`, OpenVaultDB HTTP URLs, and `env:NAME` for a URL stored in an environment variable. PostgreSQL URL support remains a preview capability and may reject a source before export. The current native root export requires collections without namespaces, databases, or parent scopes; it reports a clear error for a source it cannot map. Provider capabilities differ; an unreadable collection or value that cannot be represented fails the export explicitly. Source constraint checking, when available, is a preflight check. Native inGitDB does not execute SQL foreign-key actions or other SQL constraints on later writes, and a mutable source can change after the preflight check. For a consistent snapshot, hold a source-side snapshot or read lock during export. Re-exporting an inGitDB project through a provider without physical storage-class metadata retains logical values and source schema but marks constraints unverified; it cannot claim the original SQLite INTEGER-versus-REAL cell classes.

## Our approach to development

We build with our own tooling:

- **[SpecScore](https://specscore.md)** — specify requirements as `SpecScore.md` artifacts
- **[SpecStudio](https://specscore.studio)** — author & manage specs across their lifecycle
- **[inGitDB](https://ingitdb.com)** — store structured data in Git where applicable
- **[DALgo](https://dalgo.io)** — data access layer for Go
- **[cover100.dev](https://cover100.dev)** — drive toward 100% test coverage
- **[DataTug](https://datatug.io)** — query & explore data
<!-- /dev-approach -->

## ♺ Continuous Integration — [![Build and Test](https://github.com/datatug/datatug-cli/actions/workflows/golangci.yml/badge.svg)](https://github.com/datatug/datatug-cli/actions/workflows/golangci.yml) [![Go Report Card](https://goreportcard.com/badge/github.com/datatug/datatug-cli?cache=1)](https://goreportcard.com/report/github.com/datatug/datatug-cli) [![GoDoc](https://godoc.org/github.com/datatug/datatug-cli?status.svg)](https://godoc.org/github.com/datatug/datatug-cli) [![Coverage Status](https://coveralls.io/repos/github/datatug/datatug-cli/badge.svg?branch=main&cache-1)](https://coveralls.io/github/datatug/datatug-cli?branch=main)

Help wanted to [get test coverage to 100%](https://github.com/datatug/datatug-cli/issues/64).

## Installation

### macOS / Linux — curl

```bash
(p=$(mktemp) && trap 'rm -f "$p"' EXIT && curl -fsSL https://datatug.io/install/get-cli -o "$p" && sh "$p")
```

Environment overrides: `DATATUG_VERSION` (default: latest release), `DATATUG_INSTALL_DIR` (default: `~/.local/bin`).

### Windows — PowerShell

```powershell
$p=Join-Path $env:TEMP ("datatug-"+[guid]::NewGuid()+".ps1"); try { irm https://datatug.io/install/get-cli.ps1 -OutFile $p -EA Stop; & $p } finally { Remove-Item $p -EA SilentlyContinue }
```

Environment overrides: `DATATUG_VERSION`, `DATATUG_INSTALL_DIR` (default: `%LOCALAPPDATA%\DataTug\bin`). Current Windows releases support amd64.

### macOS / Linux — Homebrew ([tap](https://github.com/datatug/homebrew-tap))

```bash
brew install --cask datatug/tap/datatug
```

Homebrew gets a release only when the maintainers promote it, so it can be behind the direct installers (curl and PowerShell, which always take the latest release). `brew list --cask --versions datatug` shows the version installed.

The direct installers and Homebrew package do not require Go. See the full
[installation guide](https://datatug.io/install/) or the official
[AI-agent instructions](https://datatug.io/agent-instructions/install/).

### Updating

```bash
datatug self-update
datatug self-update --check  # report whether a newer release exists, without applying it
```

Homebrew installs run `brew update && brew upgrade --yes --cask -- datatug`; direct installs from curl or PowerShell download and checksum-verify the latest release and swap the binary in place. See [spec/features/cli/self-update](spec/features/cli/self-update/README.md).

### Installing and upgrading related CLIs

```bash
datatug install                # list ingitdb, ovdb and specscore, with install status
datatug install ovdb           # show details and install it the same way datatug itself was installed
datatug install ovdb --dry-run # report the planned action without downloading or writing anything
datatug upgrade                # report current/latest/verdict for every installed fleet CLI plus datatug itself
datatug upgrade --all          # upgrade every installed fleet CLI plus datatug itself, after one confirmation
```

`ingitdb` validates and edits the inGitDB databases DataTug reads; `ovdb`
runs a user-owned OpenVaultDB server DataTug can query as a catalog;
`specscore` lints DataTug's own specifications. `datatug upgrade` is the
fleet-wide counterpart to `self-update`: `datatug self-update` is exactly
`datatug upgrade datatug`, built from the same catalog configuration, so
the two never disagree. See
[spec/features/cli/install](spec/features/cli/install/README.md).

## Where DataTug is going

This is the direction of the product, not a list of what the released CLI does: what works today is in the
[Quick start](#quick-start) and in [Supported databases](#supported-databases).

- Explore data everywhere — SQL databases, cloud data sources, logs, and APIs (HTTP / REST)
- CLI-first workflows with a Web UI — dashboards, charts, and shared views
- Create parametrised queries and query sets for repeatable troubleshooting and investigation scenarios
- Automatically navigate related data across tables, views, APIs, and different data sources
- Build data pipelines to transform, combine, and enrich data
- Document schemas and metadata with a built-in wiki
- Version everything with Git — queries, dashboards, pipelines, and settings stored as readable project files
- Choose where your project lives:
    - Local directory (fully offline)
    - GitHub repository
    - DataTug Cloud

DataTug turns scattered data into a connected, navigable workspace — combining the speed of the CLI with the clarity of
a Web UI for exploration, troubleshooting, and collaboration.

## `datatug chat`: table narrowing

`datatug chat` normally shows the AI model the definitions of all of a project's tables on every turn. Table narrowing
decides, before each turn, which tables the model needs and shows it only those. Your own project rules are always on;
the decision model is off until **you** turn it on.

**What it does.** Before the model is asked, the chat picks the tables for the question: first your project rules, then
(if you turned it on) a decision model. Tables judged only possibly relevant stay in; so do the tables on the
foreign-key path between selected tables (when the source's foreign keys are known) and, for a follow-up such as "and by
genre?", the tables the previous turn used. The model is told the names of the tables that were left out and can read
up to two of them per turn with the read-only `describe_relation` tool. The chat prints one line naming the tables the
model was given, and the decision (engine, model, scores, tables before and after) is stored with the chat session.

**What it costs and saves, honestly.** Narrowing removes schema bytes from every request, but the model's request also
holds fixed instructions and tool definitions, and a table the model was not shown can cost a `describe_relation`
round trip, which re-sends the whole request. Measured on the 11-table Chinook sample, summed over every model call of
a turn:

| | model calls | input bytes | vs no narrowing |
|---|---|---|---|
| no narrowing | 2 | 22,059 | |
| narrowed to 3 of 11 tables | 2 | 21,369 | -3.1% |
| narrowed, and the model reads 1 omitted table | 3 | 32,785 | +48.6% |

The schema context alone goes from 1,829 to 1,111 bytes (-39%, the note naming the omitted tables included). The saving
grows with the schema (hundreds of tables, wide tables) and is small on a schema as small as Chinook's. Narrowing is
applied only when the narrowed context is at least 25% smaller than the full one; otherwise the full schema is kept.
It is not a guarantee of a better answer: a wrong narrowing is possible, which is why the omitted tables are named and
readable.

**When it cannot decide, the chat sends the full schema,** exactly as it did without narrowing: the decision model is
off, slow (more than 1.5 s by default), failing, refused (allowance spent, wrong endpoint), unsure, incomplete, the
schema is larger than 255 tables, or the narrowing would save less than 25%. After a failure the decision model is not
asked again for five minutes.

**Project rules (local, always on).** `<project>/ai/table-rules.yaml` (the format is provisional, until the decision
layer is specified):

```yaml
decision: auto            # a REQUEST for the decision model; see below. disabled (default) | auto | cloud
rules:
  - phrase: Which countries buy the most music?   # exact question; case, spacing, trailing ?!. ignored
    tables: [Invoice, Customer]                   # exact table names
```

A matching rule decides on its own and nothing leaves your machine. A rule that names a table the schema does not have,
or has an unknown key, is reported as a warning when the chat starts and never fires. A malformed file, a symbolic link,
a directory in its place or a file over 64 KiB is ignored with a warning; the chat starts anyway.

**The decision model (off until you turn it on).** With `--model cloud` the chat can ask the DataTug AI cloud, which
relays to **TypeSafe AI's Jev** decision model (an external model, not an LLM: it scores each table's relevance). When
it is on, this is sent to the DataTug cloud and from there to TypeSafe AI:

- your question, and up to the three earlier questions of the session that were asked while it was on (verbatim);
- every table name, with its column names (no column types, no rows, no values);
- the usual identifiers the cloud client already sends: an interaction id, your installation id and client version, and
  your sign-in token, which authenticates you to the DataTug cloud.

Only you can turn it on, so that opening a cloned repository never starts forwarding your questions:

- `DATATUG_AI_DECISION_PROVIDER=auto` (or `cloud`, the same plus a warning when the chat is not using `--model cloud`)
  turns it on for that session. `DATATUG_AI_DECISION_PROVIDER=disabled` always wins over everything below.
- `datatug chat --cloud-decision allow` records your consent for this project (identified by its directory, so a copy
  elsewhere does not inherit it, and a project you move or rename needs consent again) in `datatug/decision-consent.json` in your user config directory, outside the project.
  `--cloud-decision refuse` records a refusal; `--cloud-decision forget` removes the record.
- A project's own `decision: auto|cloud` only **requests** it. Without your consent it is ignored, and the chat prints one
  line at start saying so, what would be sent and to whom, and the commands above.

Whenever it is on, the chat shows one line at start, as a system line at the top of the chat (the terminal UI hides
anything printed before it starts) and on stderr for non-interactive runs, naming where the setting came from, what is sent
to whom, and how to turn it off. Rule and configuration warnings appear the same way. These lines are not messages: they
are not stored in the session and never sent to the model. `DATATUG_AI_DECISION_TIMEOUT` (a Go duration, default `1500ms`) sets how long it may take per turn. It is off
by default pending a decision on how TypeSafe AI may handle this data.

Telemetry for a decision carries counts and the engine and model ids only (for example `narrowed:before=11:after=3`),
never table names or question text, and is sent only when a decision was actually made.

## What it is and why?

This is an agent service for https://datatug.app that you can run on your local machine, or some server to allow DataTug
app to scan databases & execute SQL requests.

It runs on your machine under your user account. The connection URL of a PostgreSQL source stays in an environment
variable of yours, and a project never holds it.

## Would you steal my data?

No, we won't.

The project is **free and open source**, and the code of this CLI is at https://github.com/datatug/datatug-cli. You are
welcome to check - we do not look into your data.

The CLI does send a small amount of anonymous usage telemetry, which you can switch off: see [Telemetry](#telemetry).

## Telemetry

**What is sent.** The CLI sends anonymous usage events and crash reports to [PostHog](https://posthog.com)
(`eu.i.posthog.com`), through [`pkg/dtlog`](pkg/dtlog), the only code in this repository that can send them. We use them
to learn how often the CLI is run, which screens are opened and where it crashes, so we can fix what people hit. With telemetry on
the CLI also fetches its PostHog project key from `raw.githubusercontent.com`, once a day when that succeeds (it is retried on each run until it does): that request carries no
data of yours (GitHub sees the IP address it comes from, as any web server does), and it is not made when telemetry is off.

| Event | When | Fields it carries |
|---|---|---|
| `DataTug CLI started` | a command starts | the common fields below |
| `DataTug CLI exited` | a command ends | the common fields below |
| `Screen opened` | a screen of the terminal UI opens | the common fields, `$app_name` (`DataTug`), `$app_version`, `$screen_id` and `$screen_name` (fixed labels of DataTug's own screens, such as `viewers/sqlite` and `SQLite Viewer`) |
| `$exception` (crash report) | the CLI crashes with a panic | `distinct_id`, the SDK and system fields below, and one exception: its `type` (`panic`), its `value` (the Go type of the panic value, such as `string` or `*errors.errorString`; for a crash of the Go runtime itself, such as an index out of range, its message, which names numbers and types only) and its stack: function names, source file *names* (no directory), line numbers and code addresses; and `$debug_images`: the type, build id, load address, link address, size and architecture of the executable (not its path) |

The common fields: `uuid` (a random id of the event), `distinct_id` (a random id of this install, kept in
`~/datatug/.posthog.yaml`), `timestamp`, the
session (`$session_id`, `$session_start_time`, `$session_duration`), and what the PostHog Go SDK adds: `$lib`,
`$lib_version`, `$os`, `$os_version`, `$os_distro`, `$go_version`, `$geoip_disable` and `$is_server`. `$geoip_disable` asks
PostHog not to look up your location; as for any web request, PostHog's server still receives the IP address the request
comes from.

**What is not sent.** No database content, no query text, no project or database name, no path, no host, no user name, no
command-line argument and no credential. The text of a panic is not sent either, because it can hold a path or a host: it
goes to stderr and to the local log only. A test builds each event as `pkg/dtlog` hands it to the PostHog client and fails
when its fields change, so this list cannot drift from the code, and another test fails when any other package imports the
PostHog client. The notice is checked by tests for the claims it makes about the fields (such as which event carries the
DataTug version), and by a reader against this list when either changes.

**The first run.** The first time the CLI runs with telemetry on in a terminal (stderr is a terminal) it prints a notice
on stderr (at most six lines) saying the above in brief, and nothing is sent on that run. Events start with the next run.
The notice is printed once per user: the CLI records that it was shown in the file `~/datatug/.telemetry-notice-shown`
(delete it to see the notice again). If that file cannot be written the notice is printed again next time, and the run
does not fail. A run whose stderr is not a terminal (a script, a service, shell completion with `2>/dev/null`) tells
nobody, so it prints nothing, writes no marker and sends nothing: the notice waits for the first run in a terminal. The
same holds when the home folder cannot be resolved (a service without a home): nothing is created and nothing is sent.

**Turn it off.** Telemetry is on only when `DATATUG_TELEMETRY` is empty or one of `1`, `true`, `on`, `yes`; any other value
turns it off, so a typo never leaves you measured. Any one of these turns telemetry off completely: no notice, no client,
no request, no file:

```
DATATUG_TELEMETRY=0     # 0, false, off, no, or any value other than 1, true, on, yes
DO_NOT_TRACK=1          # any value but empty or 0 (https://consoledonottrack.com)
CI=true                 # any value but empty or false: set by most CI systems
```

A variable set in one terminal is gone in the next. To turn telemetry off for good, add `export DATATUG_TELEMETRY=0` to
your shell profile (`~/.zshrc`, `~/.bashrc`), or on Windows run `setx DATATUG_TELEMETRY 0` once. `datatug --help` names the
variable too. `datatug version --json` never sends telemetry, whatever you set.

**Chat with the DataTug cloud AI.** `datatug chat --model cloud` is your choice to use the DataTug cloud AI service: your
questions go to it to be answered, whatever the switch says, because that is what the command does. It also sends a
metadata report of each turn (the length of your message in characters and words, its status and outcome, the names of the
actions that ran, the conversation id, a random id of the turn, the client type and feature labels, and your install id, OS, CPU architecture and DataTug version; not the text of your question and not
your rows). That report is usage telemetry and follows the same switch: with `DATATUG_TELEMETRY`, `DO_NOT_TRACK` or `CI`
turning telemetry off, or on the first run, it is not sent. Its install id is a different random id from the one above,
kept in `datatug/installation_id` in your user configuration folder (`os.UserConfigDir`). Chat with your own AI profile
reports nowhere.

## Where are metadata stored?

When DataTug agent scans or compare your database it stores meta information in a datatug project as set of simple to
understand & easy to compare JSON files.

We recommend to check-in the project to some source versioning control system like GIT.

You can run commands for different projects by passing the path to the project folder. E.g.:

```
> datatug show -d ~/my-datatug-projects/DemoProject
```

A project can be registered under a short name with `datatug projects add`: the paths to the registered projects, and
their names, are stored in `~/.datatug.yaml` in your user's home directory. Then a project is addressed by its name:

```
> datatug show -p DemoProject
```

If the current directory is a DataTug project folder you don't need to specify project name or path.

```
> datatug show
```

## How to get the DataTug CLI?

Use one of the supported methods in [Installation](#installation). None requires Go.

Then verify the installed CLI:

```
> datatug --help
```

## How to run?

Start with the [Quick start](#quick-start). `datatug --help` lists every command, and `datatug serve` runs the agent that
the web app talks to (see [spec/features/cli/serve](spec/features/cli/serve/README.md)).

## Supported databases

| Database | Scan (`datatug scan`) | Query (`datatug query run`) |
|---|---|---|
| SQLite | supported | supported |
| PostgreSQL | supported: every schema the role can use, tables and views, columns with their defaults, primary keys (no foreign keys or indexes yet); the connection URL stays in an environment variable | preview, read-only: set `DATATUG_PREVIEW_POSTGRES=1` |
| inGitDB | not supported yet | supported: `--db ingitdb://./path-to-the-database` |
| OpenVaultDB | not supported yet | not tested in this release |
| SQL Server | accepted by `scan -D sqlserver`, not tested in this release | not supported yet |
| Firestore | not supported yet | not supported yet |

A database that is not in this table is not supported by this release. We are open for pull requests.

**PostgreSQL**, via [dalgo2postgres](https://github.com/dal-go/dalgo2postgres). `datatug scan -D postgres --dsn-env
DATATUG_SHOP_PG_URL --db shop --env local` reads the connection URL from the environment variable (it is never written to
the project) and saves every schema the role can use, the system schemas apart: tables, views and materialized views
(as views), columns with their defaults (the text of the SQL expression) and primary keys, a folder for each schema.
The scan does not save foreign keys or indexes yet.

## For developers

Read [README-dev.md](docs/README-dev.md) for details on how to setup, debug, and contribute; the terminal UI architecture (shell, widgets, grid, navtest) is summarised there and explained in [tui-screens.md](docs/tui-screens.md).

## Sample Databases

SQLite samples to scan and query:

- [Chinook Database](https://github.com/lerocha/chinook-database)
- [Northwind](https://github.com/jpwhite3/northwind-SQLite3)

## Open Source Libraries we use

- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - Terminal UI framework (The Elm Architecture) with Lip Gloss styling; shared widgets live in `tuigoff`
- [DALgo](https://github.com/dal-go/dalgo) - Database Abstraction Layer for Go
- https://gihub.com/strongo/validation - helpers for requests & models validations

## Contributing

We welcome contributions to DataTug! Please read our [contributing guidelines](docs/CONTRIBUTING.md) for more
information on how to contribute to the project.

## Download

http://datatug.app/download

# [License](./LICENSE)

Apache License
