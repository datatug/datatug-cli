---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: tview to Bubble Tea migration (datatug, sneat CLIs, strongo-tui)

**Status:** Draft
**Source:** idea:tview-to-bubbletea-migration
**Date:** 2026-09-29
**Owner:** alex
**Supersedes:** —

## Summary

Move every terminal UI in the DataTug and Sneat CLIs onto Bubble Tea v2 (`charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`), using `datatug chat` (`pkg/chat`, built on `strongo/aichat/tui`) as the reference for look, structure and testing. When the work is done there is no `tview` or `tcell` reference in code, comments, docs, `go.mod` or `go.sum` of `datatug-cli`, `sneat-cli` or `strongo-tui`.

Founder rulings (2026-09-29) that shape the plan:

- Shared code goes in the shared library (`strongo/strongo-tui`), because the Sneat CLI adopts it too.
- Libraries are migrated in place. No backward compatibility, no deprecated shims, no old and new implementations side by side.
- `datatug filetug` is removed (it embeds filetug's tview-only Navigator, which has no Bubble Tea version).
- `datatug` must end at 100% statement coverage.

### Scope inventory (measured on `main`, `14307f0`)

| Area | Files | Lines |
|---|---|---|
| tview/tcell production code in `datatug-cli` | 51 | 6,271 |
| tview/tcell tests in `datatug-cli` | 18 | 5,868 |
| Already Bubble Tea (`pkg/chat`, `pkg/auth/gauth`) | — | ~44k (chat) |
| `strongo-tui` (tview widgets, colours, themes) | ~15 | ~1,040 |
| `sneat-cli` TUI (`internal/tui`, already Bubble Tea, no tview) | 8 | ~1,900 |

tview widgets in use in `datatug-cli`: Table (81 cell uses), TextView, TreeView, List, Flex/Grid, Box, Form/InputField/Button, Modal, Pages, plus the `sneatnav` shell (header breadcrumbs, menu, content, actions bar, alerts).

## Approach

**Shape of the end state**

```
strongo-tui (one module, Bubble Tea only)
├─ pkg/nav        root tea.Model: header+breadcrumbs, menu, content, actions bar, alert overlay, focus zones, Send/Quit/Run
├─ pkg/widgets    List, Table, TextView, Tree, Form/Input/Button, Frame, Modal, layout helpers
├─ pkg/highlight  chroma → ANSI helper (replaces filetug chroma2tcell)
├─ pkg/nav/navtest  TTY-free harness: press keys, read View text
└─ themes/colours on lipgloss (reuses the aichat theme palette)

datatug-cli  → screens only (apps/datatugapp/datatugui/**), no pkg/sneatview, no pkg/sneatv
sneat-cli    → internal/tui adopts pkg/nav for shared chrome
```

**Sequencing.** The shell and widgets are on every screen's critical path, so they land first. Screen ports then run in parallel by area, each in its own worktree lane with its own tests. The Bubble Tea and tview models cannot share a process, so `datatug-cli` switches in one integration PR; there is no long-lived mixed state on `main`.

**Development coupling.** While `strongo-tui` is unreleased, the `datatug-cli` worktree uses a local `replace` to the `strongo-tui` worktree. Both repos share one WB task (`tview-to-bubbletea`). Landing order is fixed: `strongo-tui` merged and tagged → `datatug-cli` `replace` removed and dependency bumped → `sneat-cli` bumped. `filetug` keeps its pinned old `strongo-tui` tag and is out of scope.

**Porting idiom.** Screens stay imperative and pointer-based so the port is mechanical:

| Old (tview / sneatnav) | New (strongo-tui) |
|---|---|
| `tview.NewList`, `NewTable`, `NewTextView`, `NewTreeView`, `NewForm` | `widgets.NewList`, `NewTable`, `NewTextView`, `NewTree`, `NewForm` |
| `SetInputCapture(func(*tcell.EventKey) *tcell.EventKey)` | `HandleKey(tea.KeyPressMsg) (handled bool, cmd tea.Cmd)` |
| `tui.App.SetFocus(p)` | `tui.SetFocus(zone)` / panel `SetFocused` |
| `tui.App.QueueUpdateDraw(f)` | `tui.Send(msg)` handled in `Update` |
| `tui.App.Stop()` | `tui.Quit()` |
| `sneatnav.NewPanel` / `sneatv.WithDefaultBorders` | `widgets.Frame` (border on by default, `WithoutBorders` option) |
| `chroma2tcell.ColorizeYAMLForTview` | `highlight.YAML` (ANSI) |
| `tview.Modal` / `Pages` alert | `tui.ShowAlert` overlay |

**Testing.** Tests drive `Update()` with messages and assert on rendered `View()` text through `navtest`; no test starts a real terminal (`runTeaProgram` seam, as in `pkg/chat`). Behaviours covered by the 18 existing tview test files (5,868 lines) are ported, not dropped. Coverage is a per-task exit gate, and the final task checks the whole module.

## Tasks

### Task 1: strongo-tui — Bubble Tea toolkit (in place)

**Id:** task-1
**Depends-On:** —
**Status:** planning

Rewrite `strongo-tui` on Bubble Tea v2 in place, with FileTug's upcoming migration as a design input: `pkg/nav` shell (focus semantics from `sneatnav`, including Up-at-top-of-menu to breadcrumbs, Right to content, Ctrl+Q quit, global key handlers suppressed while an input is focused), `pkg/widgets` (List, Table with lazy content provider, TextView, Tree, Form, Frame, Modal, layout helpers), `pkg/highlight`, and the `navtest` harness. Fold the existing `charm/` sub-module into the root module, delete all tview/tcell code, examples and docs, tidy `go.mod`. Exit gate: 100.0% coverage on every package, `grep -ri 'tview\|tcell'` empty, README with the porting table above.

### Task 2: strongo-tui — release

**Id:** task-2
**Depends-On:** 1
**Status:** planning

Land the `strongo-tui` PR with `wb pr land`, wait for CI, tag the release (check whether the repo auto-tags before hand-tagging), and record the tag for the consumers. Any consumer still on the old tview API keeps its pinned tag.

### Task 3: datatug-cli — remove `datatug filetug` and non-UI filetug coupling

**Id:** task-3
**Depends-On:** —
**Status:** planning

Delete `apps/datatugapp/commands/cmd_filetug.go` and its registration. Remove the remaining `filetug` imports that are not UI (`fsutils` in `dtproject`, `dtviewers`, `dtlog`, `dtgithub`; `chroma2tcell` in settings) by using standard library or a small local helper for the few `fsutils` functions actually used, so `datatug-cli` no longer depends on the `filetug` module (which drags tview into `go.mod`). The two helpers actually used (`ExpandHome`, `DirExists`) go to a shared non-UI `strongo` module that FileTug will also use, not into a private copy. This task is independent of task 1 and can run first.

### Task 4: datatug-cli — shell, entrypoints and cleanup

**Id:** task-4
**Depends-On:** 1, 3
**Status:** planning

Point `datatug-cli` at the new `strongo-tui` (local `replace` during development). Replace `apps/datatugapp/tui.go` (`NewDatatugTUI`), `apps/global/app.go`, `apps/datatugapp/commands/cmd_ui.go` (`runUI`, `openFile`, module registration), the main-menu registry (`datatug_main_menu.go`), and the panic-recovery path in `main.go` (terminal restore) with the Bubble Tea shell. Delete `pkg/sneatview/**` and `pkg/sneatv/**` (including `databrowser`) once their users are ported; no copies are kept.

### Task 5: Port project screens (`dtproject`)

**Id:** task-5
**Depends-On:** 4
**Status:** planning

Port `dtproject/*` (project list, project menu and screen, create-project wizard 682 lines, add-to-GitHub flow 549 lines, demo project, dashboards, queries) and their tests. Forms use the new `Form`; long-running work (GitHub calls, scans) reports back with `tui.Send`. Exit gate: 100% coverage of `dtproject`.

### Task 6: Port database viewer (`dbviewer`)

**Id:** task-6
**Depends-On:** 4
**Status:** planning

Port `dtviewers/dbviewer/*` (SQLite and inGitDB browsers, tables box and list, table columns / FKs / referrers / content recordset, recordset table) and tests. This is the heaviest Table user (lazy content provider, wide cell counts, FK navigation). Exit gate: 100% coverage.

### Task 7: Port cloud viewers (`clouds`)

**Id:** task-7
**Depends-On:** 4
**Status:** planning

Port `dtviewers/clouds/*` (shared clouds UI, gcloud: credentials, projects, project, Firestore db/collections/collection/indexes, main menu; aws; azure) and tests. Exit gate: 100% coverage.

### Task 8: Port remaining screens

**Id:** task-8
**Depends-On:** 4
**Status:** planning

Port `dtsettings` (settings screen with highlighted YAML), `dtapiservice` (API monitor), `dtviewers` (viewers main menu and screen), `webui_handoff.go`, `apps/datatugapp/commands/grid.go`, and the tview leftovers in tests of `pkg/dtstate`. Review `pkg/auth/gauth` (already Bubble Tea) for use of the old shell and align it if needed. Exit gate: 100% coverage.

### Task 9: datatug-cli — integration, coverage and dependency clean-up

**Id:** task-9
**Depends-On:** 2, 5, 6, 7, 8
**Status:** planning

Remove the local `replace`, bump `strongo-tui` to the released tag, `go mod tidy` (no `tview`, `tcell` or `filetug` left in `go.mod` / `go.sum` beyond what is unavoidable and recorded), refresh the `TEST-COVERAGE*.md` docs and remove stale `cover*.out` files if they are tracked. Verification: `go build ./... && go vet ./... && go test ./...`, whole-module statement coverage 100.0%, `scripts/check-hermetic-tests.sh`, `grep -ri 'tview\|tcell'` empty, and a real-terminal smoke test (`datatug ui`, `datatug ui -f <sqlite>`) walking menu, settings, viewers, project and DB screens, resize, mouse, Ctrl+Q and a forced panic (terminal must be restored).

### Task 10: sneat-cli — adopt the shared shell

**Id:** task-10
**Depends-On:** 2
**Status:** planning

`sneat-cli` is already Bubble Tea and has no tview code, so this is convergence, not migration. Move `internal/tui` (spaces, space, contacts, contact card, confirm screens) onto `strongo-tui` `pkg/nav` and `widgets` so both CLIs share chrome, key bindings and testing harness; drop the private push/pop screen stack in favour of the shared shell where it fits. The two already-imported chat surfaces (`internal/chat`, `internal/chatapp`) are untouched. Exit gate: 100% coverage of `internal/tui`, `grep -ri 'tview\|tcell'` empty (currently one test file mentions them).

### Task 11: Docs, specs and landing

**Id:** task-11
**Depends-On:** 9, 10
**Status:** planning

Update `datatug-cli` README and `docs/` for the removed `filetug` command, add a short architecture note (shell, widgets, `navtest`), transition this plan and the idea through `specscore change-status`, and land each repo with `wb worktree land` (one call per repository) in the order `strongo-tui` → `datatug-cli` → `sneat-cli`, cleaning all branches and worktrees.

## Risks

- **Big-bang switch in `datatug-cli`.** Mitigated by the parallel lanes each proving their own coverage, then one integration gate (task 9). Not mitigated by a mixed tview/Bubble Tea mode; that would be the duplicate implementation the founder ruled out.
- **Table performance.** Recordsets can be large; the `Table` must virtualise rows (lazy content provider) rather than render every row. Covered by a benchmark-style test in task 1.
- **Terminal restore on panic.** Bubble Tea restores the terminal on its own recover path, but `main.go` currently calls `global.App.Stop()` explicitly; task 4 keeps an equivalent guarantee and task 9 tests it.
- **100% coverage across ~6k ported lines.** Each port task carries its own coverage gate so gaps cannot pile up at the end. Production seams are added where code touches the network or a TTY, not tests contorted around real processes.
- **Cross-repo release lag.** `strongo-tui` must be tagged before `datatug-cli` can land; task 2 is a hard gate and the `replace` directive must never reach `main`.

## Decisions

Resolved with the founder on 2026-09-29:

1. **`filetug` coupling.** Only two `fsutils` helpers are used (`ExpandHome`, `DirExists`), so task 3 moves them to a shared non-UI module (`strongo`) rather than copying them into each CLI. FileTug is also planned to migrate to Bubble Tea, so `strongo-tui` is designed for FileTug as a consumer too, and FileTug will import the same shared helpers.
2. **`strongo-tui/charm` sub-module.** No repository in the workspace imports it (only its own `go.mod` names it), so folding it into the root module is safe and needs no migration note.
3. **Sneat CLI.** Task 10 goes ahead now: `sneat-cli` and `datatug` are close enough that they share one shell.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
