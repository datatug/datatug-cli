# Feature: Console

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli%2Fconsole) — graph, discussions, approvals

**Status:** Planned

## Summary

`datatug console` starts an interactive console with autocomplete for DataTug commands. Today the command is a placeholder — it sets `GO_FLAGS_COMPLETION=1` and prints "To be implemented" — and this spec pins the *intended* contract so the future implementation has a target.

## Synopsis

```
datatug console
```

## Problem

A user running many `datatug` commands in a row pays the shell-startup cost and the project-context-resolution cost on every invocation. An interactive shell that keeps the project loaded, knows the registered commands, and tab-completes flags would be substantially faster for exploration.

Without a spec, "interactive console" can mean many things — a REPL, an autocomplete-only mode, or a full TUI. This document pins the target.

## Behavior

### Surface

#### REQ: stdin-driven-repl

The console MUST be a line-oriented REPL on stdin. It MUST NOT depend on the terminal being a TTY beyond the readline capabilities (e.g., it MUST still function when piped a list of commands via `cat commands.txt | datatug console`).

#### REQ: command-grammar-matches-cli

The grammar inside the console MUST be the same as the top-level CLI: `<resource> <verb> [flags...]`. A user typing `projects` inside the console MUST get the same behavior as `datatug projects` outside it.

#### REQ: exit-and-quit

The literal commands `exit` and `quit` MUST cleanly close the console and exit `0`. EOF on stdin (Ctrl-D) MUST behave identically.

### Autocomplete

#### REQ: tab-completes-commands

Pressing TAB at the start of an input line MUST list available commands. Pressing TAB after a partial command MUST complete it if unambiguous.

#### REQ: tab-completes-flags

Pressing TAB after a partial flag MUST complete it.

#### REQ: tab-completes-values

Where a flag's value set is enumerable (e.g., `--format` accepting `yaml|json|grid`), TAB MUST cycle through valid values.

### State

#### REQ: shared-project-context

Once the user enters a project context (via `cd-style` command or `use <project>`), subsequent commands in the same session MUST inherit it. The exact UX for context selection is a sub-feature and is out of scope for this MVP spec.

## Parameters

None.

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Console exited cleanly |
| `1` | Terminal initialization failure |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | The console is a different entry point to the same commands; behavior MUST match. |
| [ui](../ui/README.md) | Independent. The TUI is mouse-and-keyboard-driven over widgets; the console is line-oriented. |

## Acceptance Criteria

### AC: starts-and-exits

**Requirements:** console#req:stdin-driven-repl, console#req:exit-and-quit

`datatug console` starts a REPL that exits `0` on `exit`, `quit`, or EOF. (Not yet met — the current implementation prints `"To be implemented"` and returns.)

### AC: ran-as-commands-equiv-to-cli

**Requirements:** console#req:command-grammar-matches-cli

Running `datatug projects` outside the console produces the same stdout as typing `projects` inside the console. (Not yet met.)

## Outstanding Questions

- Should the console use `liner`, `readline`, `prompt-toolkit`-style, or a custom line editor? The current implementation only toggles `GO_FLAGS_COMPLETION`, which is no longer relevant under urfave/cli v3.
- Is the console scoped to a single project context, or does it support multi-project switching mid-session?
- Should the console persist command history across sessions in `~/.datatug_history`?
- Should this feature be deferred until the [CLI parent's command-naming migration](../README.md#outstanding-questions) lands, so the console only ever sees the canonical naming?

---
*This document follows the https://specscore.md/feature-specification*
