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

![datatug-cli-employees-2.png](docs/screenshots/datatug-cli-employees-2.png)

<!-- dev-approach:v1 -->
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

## What you can do with DataTug

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

A project can have hundreds of tables, and `datatug chat` normally shows the AI model all of their definitions on every
turn. Table narrowing decides, before each turn, which tables the model needs and shows it only those. Your own project
rules are always on; the decision model is off until you opt in.

**What it does.** Before the model is asked, the chat picks the tables for the question: first your project rules, then
(if you opted in) a decision model. Tables judged only possibly relevant stay in; so do the tables on the foreign-key
path between selected tables (when the source's foreign keys are known) and the tables the previous turn used, so
a follow-up such as "and by genre?" keeps what it follows. The model is told the names of the tables that were left
out and can read any one's definition with the read-only `describe_relation` tool. The chat prints one line naming the
tables the model was given, and the decision (engine, model, scores, tables before and after) is stored with the chat
session.

**It never makes the chat worse than before.** If the decision is disabled, slow (more than 1.5 s by default), failing,
refused (allowance spent, wrong endpoint), unsure, incomplete, or the schema is larger than 255 tables, the model gets
the full schema, exactly as it did without narrowing. After a failure the decision model is not asked again for five
minutes.

**Project rules (local, always on).** `<project>/ai/table-rules.yaml` (the format is provisional, until the decision
layer is specified):

```yaml
decision: disabled        # disabled (default) | auto | cloud, see below
rules:
  - phrase: Which countries buy the most music?   # exact question; case, spacing, trailing ?!. ignored
    tables: [Invoice, Customer]                   # exact table names
```

A matching rule decides on its own and nothing leaves your machine. A rule that names a table the schema does not have,
or has an unknown key, is reported as a warning when the chat starts and never fires. A malformed file, a symbolic link,
a directory in its place or a file over 64 KiB is ignored with a warning; the chat starts anyway.

**The decision model (off by default).** With `--model cloud` the chat can ask the DataTug AI cloud, which relays to
**TypeSafe AI's Jev** decision model (an external model, not an LLM: it scores each table's relevance). When you opt in,
this is sent to the DataTug cloud and from there to TypeSafe AI:

- your question, and up to the three earlier questions of the session (verbatim);
- every table name, with its column names (no column types, no rows, no values);
- the usual identifiers the cloud client already sends: an interaction id, your installation id and client version, and
  your sign-in token, which authenticates you to the DataTug cloud.

Opt in with `DATATUG_AI_DECISION_PROVIDER=auto` (use it when signed in with `--model cloud`) or `cloud` (the same, and
warn when it cannot be used), or with `decision: auto` in the project's `ai/table-rules.yaml`; the environment variable
wins. `disabled` turns it off again; any other value is reported and treated as `disabled`.
`DATATUG_AI_DECISION_TIMEOUT` (a Go duration, default `1500ms`) sets how long it may take per turn.
This is off by default pending a decision on how TypeSafe AI may handle this data.

Telemetry for a decision carries counts and the engine and model ids only (for example `narrowed:before=11:after=3`),
never table names or question text, and is sent only when a decision was actually made.

## What it is and why?

This is an agent service for https://datatug.app that you can run on your local machine, or some server to allow DataTug
app to scan databases & execute SQL requests.

It can be run with your user account credentials (*e.g. trusted connection*) or under some service account.

## Would you steal my data?

No, we won't.

The project is **free and open source** codes available at https://github.com/datatug/datatug. You are welcome to
check - we do not look into your data.

## Where are metadata stored?

When DataTug agent scans or compare your database it stores meta information in a datatug project as set of simple to
understand & easy to compare JSON files.

We recommend to check-in the project to some source versioning control system like GIT.

You can run commands for different projects by passing path to DataTugProject folder. E.g.:

```
> datatug show --project ~/my-datatug-projects/DemoProject
```

Paths to the DataTug project files, and their names are stored in `~/datatug.yaml` in the root of your user's home
directory.
This allows you to address a DataTug project in a console using a short alias. Like this:

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

Check the [CLI](https://github.com/datatug/datatug) section on how to run DataTug agent.

## Supported databases

At the moment we any DB supported by [DALgo](https://github.com/dal-go/dalgo). Like:

- [dalgo2firestore](https://github.com/dal-go/dalgo2firestore)
- [dalgo2sql](https://github.com/dal-go/dalgo2sql)

### Supported `sql` Databases:

Datatug can work with `sql` DBs if a relevant driver has been linked into `datatug`

- **SQLite** - via  [github.com/mattn/go-sqlite3](https://github.com/mattn/go-sqlite3 )
- **Microsoft SQL Server** - via [go-mssqldb](https://github.com/denisenkom/go-mssqldb)

We are open for pull requests to support other `sql` DBs.

## For developers

Read [README-dev.md](docs/README-dev.md) for details on how to setup, debug, and contribute; the terminal UI architecture (shell, widgets, grid, navtest) is summarised there and explained in [tui-screens.md](docs/tui-screens.md).

## Sample Databases

### By Database Platform

- SQLite
    - [Chinook Database](https://github.com/lerocha/chinook-database)
    - [Northwind](https://github.com/jpwhite3/northwind-SQLite3)
- MS SQL Server
    - [Northwind](https://github.com/Microsoft/sql-server-samples/tree/master/samples/databases/northwind-pubs)
- Oracle
    - [Northwind](https://github.com/dshifflet/NorthwindOracle_DDL)

### Northwind Database

- [SQLite](https://github.com/jpwhite3/northwind-SQLite3)
- [MS SQL Server](https://github.com/Microsoft/sql-server-samples/tree/master/samples/databases/northwind-pubs)
- [Oracle](https://github.com/dshifflet/NorthwindOracle_DDL)

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
