---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Terminal Chat UX Acceptance Matrix

**Status:** Draft
**Date:** 2026-09-23
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we catch terminal-specific chat and grid failures before users encounter them?

## Context

DataTug chat has multiple focus regions, inline tables, mouse and keyboard interactions, and responsive layouts. Recent manual use found issues that deterministic view tests missed. A cell-detail dialog is being developed separately.

## Recommended Direction

Define a repeatable hands-on acceptance matrix across terminal sizes, result sizes, input methods, and platforms. Record expected focus, scrolling, selection, copy, and recovery behavior with reproducible steps and evidence.

## Alternatives Considered

- Rely on unit tests alone: useful for rendering logic, but they do not reveal terminal-emulator key and mouse behavior.
- Require full automation across terminal emulators immediately: costly and fragile before the acceptance journeys are stable.

## MVP Scope

Cover narrow and wide windows, empty and large RecordSets, macOS Option keys, mouse wheel versus text selection, dialogs, session restoration, and one real Chinook query journey.

## Not Doing (and Why)

- Full end-to-end automation of every terminal emulator — choose the smallest reliable manual and automated mix later

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A short matrix reproduces the high-impact focus and scrolling failures seen in manual use | Run it against the normal CLI in at least one macOS terminal and record deviations |
| Should-be-true | Stable terminal interactions can be covered by deterministic UI tests | Add focused tests for each repeatable regression found |
| Might-be-true | A second terminal emulator exposes meaningful differences | Compare Option keys and mouse behavior in another emulator |


## SpecScore Integration

- **New Features this would create:** a terminal chat acceptance checklist and focused regression coverage
- **Existing Features affected:** DataTug CLI chat UI
- **Dependencies:** a runnable CLI and Chinook demo source

## Open Questions

- Which terminal emulators should be mandatory in the first acceptance pass?
- Which journeys can be automated without losing the ability to inspect real terminal behavior?
