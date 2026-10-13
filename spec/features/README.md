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
| [Chat FK JOIN exploration](chat-fk-join-exploration/README.md) | Draft | — | Discover and apply deterministic foreign-key joins from DataTug Chat RecordSets. |
| [Chat RecordSet charts and adaptive views](chat-recordset-adaptive-charts/README.md) | Draft | — | An immutable Chat RecordSet exposes its table, deterministic chart candidates, and a scrollable current-row inspector. The latter two views share space with the table when the terminal is wide enough and take the main pane when narrow. |
| [Unified terminal workspace and API observability](unified-tui/README.md) | Stable | — | A single terminal workspace supports Explore, chat, result grids, record inspection and live API observability. The workspace and the five follow-up UX additions shipped in DataTug CLI v0.73.0; dialog mouse-boundary repairs shipped in v0.73.1. This specification retains the accepted behavior and records the verified supported choices. The implementation inventory maps all 16 plan tasks to released source, tests, delivery and evidence limits. |
| [Polished terminal exploration workspace](tui-refinements/README.md) | Implementing | — | Refine the released terminal workspace through clearer selection, structured metadata, portable shortcuts, consistent tabs, session navigation and useful query columns. |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/features-index-specification*
