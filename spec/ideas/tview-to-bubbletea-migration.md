---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Tview To Bubbletea Migration

**Status:** Draft
**Date:** 2026-09-29
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we run every DataTug and Sneat terminal UI on one Bubble Tea toolkit, with no tview or tcell left anywhere?

## Context

datatug-cli's TUI is built on tview/tcell (sneatnav shell, ~50 screen files) atop tuigoff and filetug; datatug chat and sneat-cli already run on Bubble Tea.

## Recommended Direction

<!-- 2–3 paragraphs: what and why, over the alternatives. -->

## Alternatives Considered

<!-- 2–3 directions that lost, and why each lost. -->

## MVP Scope

tuigoff migrated in place with widgets and nav shell; all datatug-cli screens ported at 100% coverage; datatug filetug removed; sneat-cli adopts the shared shell.

## Not Doing (and Why)

- Port filetug itself — separate tview app pinning an old tuigoff tag
- Backward-compatible tview shims — founder ruled no compatibility or duplicate implementations

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | placeholder dealbreaker assumption | describe how to validate |
| Should-be-true | … | … |
| Might-be-true | … | … |


## SpecScore Integration

- **New Features this would create:** TBD at design time
- **Existing Features affected:** none
- **Dependencies:** none

## Open Questions

None at this time.
