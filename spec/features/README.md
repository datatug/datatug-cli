---
format: https://specscore.md/features-index-specification
---

# datatug-cli Features

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures) — graph, discussions, approvals

Features of the `datatug` CLI.

| Feature | Status | Kind | Description |
|---------|--------|------|-------------|
| [CLI](cli/README.md) | Implementing | Command | The `datatug` CLI is the local agent for the DataTug data exploration platform. It scaffolds DataTug projects, scans database schemas into versionable project files, serves an HTTP API for the DataTug Web UI, opens an interactive terminal UI (TUI) over the project, executes queries against connected databases, and manages a per-user registry of DataTug projects. |
| [Git Integration for Mutating Commands](mutation-git-integration/README.md) | Stable | Capability | A single, shared `--git=<none\|stage\|commit>` flag plus a small go-git helper that any DataTug command which writes project files can opt into — staging (or, later, committing) **exactly the files that command changed**, so each mutator doesn't reinvent git handling. |
| [AI Query Builder](ai-query-builder/README.md) | Approved | — | The **CLI Implementation** of the AI Query Builder Capability (`specscore:feature/ai-query-builder@github.com/datatug/datatug`): an interactive `datatug` terminal mode that seeds and progressively refines a read-only query from natural language, printing the current query and rendering results in the existing CLI table UI. This Feature specifies only the CLI-specific surface and the deltas/limitations from the Capability; all platform-agnostic behavior is inherited from the Capability it `**Implements:**`. |
| [Serve-Brokered AI Query Builder](serve-brokered-query-builder/README.md) | Approved | — | The **CLI (daemon) Implementation** of the Serve-Brokered AI Query Builder Capability (`specscore:feature/serve-brokered-query-builder@github.com/datatug/datatug`). It extends the existing `datatug serve` daemon ([cli/serve](../cli/serve/README.md)) with two additions on the **same** process, host, port, and lifecycle: an **MCP endpoint** (streamable HTTP) that a terminal AI-agent drives, and a **query-builder session broker** that holds one current query per named-query *tab* and synchronizes it with the hosted Web UI over HTTP + WebSocket. Each tab is in one of two modes: **DTQL mode** (default) holds a canonical dalgo AST whose textual form is a 1:1 DTQL-YAML serialization, rendered to the connection's native query language on run; **native mode** holds the connection's own native query text plus parameters verbatim and executes it as-is. The AST spans relational and nested/document shapes; a tab may convert DTQL→native one-way. This Feature specifies only the daemon-side surface and its deltas; all platform-agnostic behavior is inherited from the Capability it `**Implements:**`. |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/features-index-specification*
