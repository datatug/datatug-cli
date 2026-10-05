---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Show

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/show?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/show?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/show?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/show?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug show` lists what a [scan](../scan/README.md) wrote into a project: the project's ID, each environment, each source with its driver, each schema, and each table and view with its columns, their types and their place in the primary key. It is the second step of the quick start in the README: scan a database, look at what was scanned. The text is plain, one item to a line, and the same from one run to the next, so that two runs can be compared; `--format json` prints the same as one JSON document.

## Synopsis

```
datatug show --directory <path>
datatug show --project <id>
datatug show
datatug show --directory <path> --format json
```

## Problem

A project is a tree of JSON files on disk. After a scan, the first thing a person wants to know is what the scan found. Reading the tree is tedious, and an answer that does not list the tables of a scanned project (the first version of `show` listed environments and database IDs and nothing below them) sends the person to the files.

## Behavior

### Project resolution

#### REQ: requires-project-context

`show` MUST resolve its project as the shared CLI conventions say ([REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)): the folder of `--directory` (`--dir` is the same flag), or the registered project of `--project`, or else the current folder. `--project` and `--directory` together MUST exit `2`, and the message MUST name both. A `--project` that names no registered project (a person who has registered none has no settings file, and no project of that name) MUST exit `3`.

#### REQ: not-a-project-names-the-folder

A folder with no project file (`datatug-project.json` at the root of the folder) MUST exit `3` with one sentence on stderr that names the folder and the command that makes a project, and nothing on stdout. The folder need not exist, and a path that is a file is not a folder with a project in it: the same sentence and exit code. The project file is looked for at the root of the folder only: where the layout of [scan](../scan/README.md#project-layout-written-by-a-scan) puts it. A folder with no project file at its root and one in its folder `datatug` (where the create screen of the terminal UI wrote it until issue 263) gets one sentence that says the file is there, is to be moved up one folder, and needs its access (`"access": "private"`) and the time it was made (`created.at`) added before a scan can save into the project (that file holds an ID and a title only); the sentence says to make a new project instead as the other way out.

#### REQ: folders-only

`show` reads a folder of this machine. An address (`https://...`) given as the folder, or as the place of a registered project, MUST exit `2`.

### What it lists

#### REQ: lists-what-a-scan-wrote

For a project written by a scan, `show` MUST list, in this order, the project's ID (the `id` of the project file, and the name of the folder when the file holds none); each environment; for each environment each source (a database the environment records) with its driver; for each source each schema; for each schema each table, then each view, each with its columns in the order the scan stored them. A column is its name, its type (`-` when the scan stored none) and, when it is part of the primary key, `pk`, followed by its 1-based place in the key when the key has more than one column. A table or a view with no column is listed with none. A source whose catalog has no `dbModel` (registered and never scanned, as most of the catalogs of the public demo project are) is listed as a source with the line `not scanned` below it, and is no failure (in JSON, `notScanned` is `true` and `schemas` is empty). A source that was scanned and has no table and no view to list (a database with none, or a catalog whose model has no files, its `dbmodels` folder having been removed) is listed with the line `no tables or views` below it, and is no failure (in JSON, `empty` is `true` and `schemas` is empty); it is not told from a source that was never scanned, and a source with something to list has neither line. (`TestShowSaysSoForAScannedSourceWithNoTableOrView`.)

#### REQ: same-reader-as-chat

The sources of an environment MUST be listed as chat lists them: the catalogs the environment holds, as `ProjectStore.LoadEnvDbCatalogs` lists the folder of its catalogs (`pkg/api/resolver.go`, `catalogSources`, says why the list of catalogs in the environment file is not used: it may name a catalog that has no folder, and leave out one that has). A catalog the environment file lists and that has no folder is not a source, and a catalog folder the file does not list is. The tables, the views and the columns of a source MUST be read through `api.GetCatalogSchema` (`pkg/api/catalog_tables_api.go`), the reader that chat and `serve` use for the same files, so that what `show` lists and what they find cannot disagree. A source whose files cannot be read (the catalogs of an environment, the files of its model, its columns) MUST exit `1` with one sentence that names the source or the environment, and says nothing that the file system said (such a text quotes a path of the project).

#### REQ: postgres-source-by-variable

A PostgreSQL source MUST be shown with the name of the environment variable that holds its URL (read from the connection descriptor the project records for it), and with no host, no port, no user, no password and no URL, whatever the variable holds, and whether or not it is set: listing a project does not connect. A descriptor that cannot be read, that is outside the project folder or that names a variable a project may not name MUST exit `1`, naming the source.

#### REQ: no-catalog-says-so

A project with no source in any environment MUST print its ID line and one sentence that says no database has been scanned into it yet and names the command that scans one, and exit `0`. In JSON no environment has a source (`sources` is `[]` for each), and the list of environments is empty when the project has none. (`TestShowAProjectWithNoCatalog`, `TestShowListsNoSourceForAnEnvironmentWithoutCatalogs`.)

### Output

#### REQ: plain-text

The text MUST be plain: one item to a line, two spaces of indent for each level, no tab, no emoji, no line longer than 80 columns for names that are themselves short enough to fit. The labels are `Project`, `Environment`, `Source`, `Schema`, `Table` and `View`. A name that comes from the project (the project, an environment, a source, a schema, a table, a view, a column, a type) is printed as it is when every character of it is printable and it holds no space and no double quote, and otherwise as a quoted string (`"a\nb"`, `"Order Details"`, `"say \"hi\""`), so that a name with a line break forges no line, a name with a terminal escape sequence acts on no terminal, and a name with a space in it (a column `id INTEGER pk`) is told from the type and the key that follow it on its line. JSON is not quoted this way: it holds each name exactly. The driver of a source is printed only when it is a plain name, and as `unknown driver` otherwise, in both formats: a catalog file may hold a URL with a password there.

#### REQ: json-format

`--format json` MUST print the same information as one JSON document: `project`; `environments[]` of `id` and `sources[]`; a source has `id`, `driver`, `dsnEnv` (PostgreSQL only), `notScanned` (`true` only for a source that was never scanned), `empty` (`true` only for a source that was scanned and has nothing to list) and `schemas[]` of `name`, `tables[]` and `views[]`; each of those has `name` and `columns[]` of `name`, `type` and `primaryKeyPosition` (the last two left out when there is none). Any other `--format` than `text` and `json` MUST exit `2`.

#### REQ: stable-order

Within one project the order MUST be deterministic and the same in both formats: environments by ID, sources by ID, schemas by name, tables and views by name (byte order), columns as the scan stored them. Two back-to-back runs on the same project MUST produce byte-identical stdout.

#### REQ: writes-to-stdout

All output MUST go to stdout; a failure goes to stderr and exits non-zero (see [REQ: error-on-stderr](../README.md#req-error-on-stderr)). Nothing is printed to stdout before a failure that is known up front.

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--project` | `-p` | string | Registered project ID. |
| `--directory` | `-d`, `--dir` | string | Project directory path. |
| `--format` | none (`show` has no `-f`) | string | `text` (default) or `json`. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Project listed, or said to hold no scanned database |
| `2` | `--project` and `--directory` together; an unsupported `--format`; an address as the folder |
| `3` | The folder is not a project (or does not exist); `--project` names no registered project |
| `1` | A file of the project cannot be read (load failure, a source's files, a PostgreSQL descriptor), or a write failed |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [init](../init/README.md) | A freshly made project is listed by `show`, with the sentence that says nothing has been scanned into it. |
| [scan](../scan/README.md) | `show` lists what the latest scan wrote, from the files of its [layout](../scan/README.md#project-layout-written-by-a-scan). The README's quick start runs `scan` and `show` as a test. |
| [terminal UI](../ui/README.md) | The project the UI's create screen makes has its project file at the root of the folder, so that `show`, `scan`, `serve` and chat read it (issue 263). The screen refuses a folder that already holds a project file, and an ID that is registered, and writes nothing then: a scan's project file is never replaced. |
| [validate](../validate/README.md) | `show` does NOT validate: a project that cannot be validated is still listed. |

## Acceptance Criteria

### AC: lists-what-a-scan-wrote

**Requirements:** show#req:lists-what-a-scan-wrote, show#req:same-reader-as-chat, show#req:plain-text, show#req:stable-order

Given a SQLite file with a table with a one-column key, a table with a composite key whose order is not the order of its columns, and a view, `datatug scan` into a folder and then `datatug show -d <folder>` exits `0` and writes the text of the journey database: the project, the environment, the source with its driver, the schema, the tables and the view with their columns, types and key positions, every line within 80 columns and no tab. (The quick start of the README shows the same kind of text for its own database, and is run by [AC: quick-start-is-true](#ac-quick-start-is-true).) Two runs are byte-identical. (`TestShowListsWhatAScanWrote`, `TestShowListsEnvironmentsAndSourcesInOrder`.)

### AC: sources-are-listed-as-chat-lists-them

**Requirements:** show#req:same-reader-as-chat, show#req:lists-what-a-scan-wrote

On a project of the shape of the public demo project (an environment file that lists a catalog that has no folder; catalogs with no `dbModel`, one of them an inGitDB catalog; a catalog folder that its environment file does not list; one scanned source), `show` exits `0` and lists every catalog folder of each environment, the unscanned ones with `not scanned`, and the scanned ones with their tables; the environment whose file lists a catalog with no folder is listed with no source. A catalog file that cannot be read exits `1` with a sentence that names the environment and no path. (`TestShowListsTheSourcesChatListsOnAProjectOfTheShapeOfTheDemo`, `TestShowListsNoSourceForAnEnvironmentWithoutCatalogs`, `TestShowFailsWhenAStoredFileCannotBeRead`.)

### AC: names-are-printed-safely

**Requirements:** show#req:plain-text

A SQLite file whose columns are named with a line break and an escape character, or with a space or a double quote, scanned and shown, gives one line for each column (the names quoted), no line that is not an item of the listing and no control character; a driver in a catalog file that is a URL, or has a line break, or is empty, is printed as `unknown driver`, and nothing of it is. A project file with no ID is named by its folder. (`TestShowQuotesNamesThatAreNotPrintable`, `TestShowQuotesNamesWithASpaceOrAQuote`, `TestShowText`, `TestShowDoesNotPrintADriverThatIsNotAPlainName`, `TestShowNamesAProjectWithNoIDByItsFolder`.)

### AC: json-is-the-same-document

**Requirements:** show#req:json-format

`--format json` on that project prints one JSON document with the same project, environments, sources, schemas, tables, views and columns; `--format yaml` exits `2`. (`TestShowFormatJSON`.)

### AC: postgres-is-named-by-its-variable

**Requirements:** show#req:postgres-source-by-variable

After `datatug scan --driver postgres --dsn-env NAME` over the fake reader of the scan journey, `show` writes `URL in $NAME` for the source and no part of the connection URL (the secret, the user, the host, the port, the scheme), in either format, with the variable unset. A descriptor that names a variable a project may not name, or that is missing, or that points out of the folder, exits `1` and names the source. (`TestShowNamesTheVariableOfAPostgresSource`, `TestShowRefusesAPostgresSourceWithAnUnusableDescriptor`.)

### AC: no-catalog-says-so

**Requirements:** show#req:no-catalog-says-so

A folder whose project has no database scanned into it exits `0` with the ID line and the one sentence; in JSON, `environments` is empty. (`TestShowAProjectWithNoCatalog`.)

### AC: not-a-project-is-not-found

**Requirements:** show#req:requires-project-context, show#req:not-a-project-names-the-folder, show#req:folders-only

`datatug show -d <folder>` for a folder that is not a project, or does not exist, exits `3` with one sentence that names the folder and the command that makes a project, and writes nothing to stdout; the same with no flag in a folder that is not a project; `--project` with `--directory` exits `2`, naming both; `--project` with a name that is not registered exits `3`; an address as the folder, or as the place of a registered project, exits `2`; a path that is a file exits `3` with the same sentence; a folder whose project file is one folder deeper, in `datatug`, exits `3` with a sentence that says where to move it and what to add to it so that a scan can save into it (its access and the time it was made), or to make a new project instead. (`TestShowRemedyForTheOldWizardPathWorksWhenFollowed`, `TestShowAFolderThatIsNotAProject`, `TestShowAFileIsNotAProject`, `TestShowDoesNotLookOneFolderDeeper`, `TestShowWithoutAFolderReadsTheCurrentOne`, `TestShowByRegisteredName`, `TestShowByRegisteredNameAtAnAddress`, `TestShowFailsWhenAStoredFileCannotBeRead`.)

### AC: wizard-project-is-read

**Requirements:** show#req:requires-project-context

A project made in the terminal UI's create screen (`dtproject.CreateLocalProject`) is listed by `datatug show -d <folder>` and by `datatug show -p <id>`, and a scan into it is listed too; the screen writes no folder `datatug` inside the project. Creating a project in a folder that already holds a project file, or with an ID that is registered, is refused and leaves the file byte-identical; an ID or a title the form refuses is refused by `CreateLocalProject` too, before anything is written; a registration that fails leaves no project file behind, so the next try is not refused. (`TestProjectCreatedInTheWizardIsReadByTheRestOfTheCLI`, `TestCreateLocalProjectEndToEnd`, `TestCreateLocalProjectFailures`.)

### AC: quick-start-is-true

**Requirements:** show#req:lists-what-a-scan-wrote, show#req:postgres-source-by-variable

Every command and every output block of the quick start of the README is run by a test, in order, against a SQLite file the quick start makes and, for PostgreSQL, the fake reader of the scan journey; each command's stdout equals the block under it, the scan reports on stderr and the query says on stderr that it runs without access policies, and no file of the folder holds a part of the URL. The commands run are exactly the five of the quick start, and a fenced block that is neither a console block nor the one installation command fails the test (a block whose kind is changed is never skipped without a word). The test also runs in the build with cgo off, as a release is built. A change of a command or of an output block without the other fails the test. (`TestQuickStartOfTheREADMEIsTrue`, `TestQuickStartTestCatchesAChangeOfOnlyOneSide`.)

## Open Questions

- Foreign keys, indexes and referenced-by are not listed: a scan does not store them. A later task adds foreign keys.
- Should `show` gain `--depth` (environments only, environments and sources, everything) for projects with many tables?

---
*This document follows the https://specscore.md/feature-specification*
