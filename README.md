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



## ♺ Continuous Integration — [![Build and Test](https://github.com/datatug/datatug-cli/actions/workflows/golangci.yml/badge.svg)](https://github.com/datatug/datatug-cli/actions/workflows/golangci.yml) [![Go Report Card](https://goreportcard.com/badge/github.com/datatug/datatug-cli?cache=1)](https://goreportcard.com/report/github.com/datatug/datatug-cli) [![GoDoc](https://godoc.org/github.com/datatug/datatug-cli?status.svg)](https://godoc.org/github.com/datatug/datatug-cli) [![Coverage Status](https://coveralls.io/repos/github/datatug/datatug-cli/badge.svg?branch=main)](https://coveralls.io/github/datatug/datatug-cli?branch=main) 

Help wanted to [get test coverage to 100%](https://github.com/datatug/datatug-cli/issues/64).

## Installation

### From source codes
```bash
go install github.com/datatug/datatug-cli@latest -o datatug
```

### macOS ([Homebrew](https://github.com/datatug/homebrew-tap))
```bash
brew tap datatug/tap
brew install datatug
```

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

## What it is and why?

This is an agent service for https://datatug.app that you can run on your local machine, or some server to allow DataTug
app to scan databases & execute SQL requests.

It can be run with your user account credentials (*e.g. trusted connection*) or under some service account.

## Would you steal my data?

No, we won't.

The project is **free and open source** codes available at https://github.com/datatug/datatug. You are welcome to
check - we do not look into your data.

You can easily get executable of the agent from source codes using next command:

```
go install github.com/datatug/datatug
```

Note: _[Go language](https://golang.org/) should be [pre-installed](https://golang.org/dl/)_

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

## How to get DataTug agent CLI?

Get from source codes by running:

```
> go install github.com/datatug/datatug
```

If it passes you are good to go:

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

Read [README-dev.md](docs/README-dev.md) for details on how to setup, debug, and contribute.

## Dependencies & Credits

- https://github.com/denisenkom/go-mssqldb - Go language driver to connect to MS SQL Server
- https://gihub.com/strongo/validation - helpers for requests & models validations

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

- [DALgo](https://github.com/dal-go/dalgo) - Database Abstraction Layer for Go
- [Bubble Tea](https://github.com/charmbracelet/bubbletea) - A Go framework for TUI apps

## Contributing

We welcome contributions to DataTug! Please read our [contributing guidelines](docs/CONTRIBUTING.md) for more
information on how to contribute to the project.

## Download

http://datatug.app/download

# [License](./LICENSE)

Apache License
