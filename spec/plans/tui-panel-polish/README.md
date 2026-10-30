---
format: https://specscore.md/plan-specification
status: Implemented
---

# Plan: Polish terminal panels and chat controls

**Status:** Implemented
**Source Feature:** tui-panel-polish
**Date:** 2026-10-13
**Owner:** CLI-lane
**Supersedes:** —

## Summary

Deliver the terminal-only selection-to-inspection and chat navigation journey in `tui-panel-polish`, building on historical shipped specs without rewriting them.

## Approach

The user opens a clear header, searches Explorer, selects Tables and then a table, inspects a retained result, resizes each focused side, switches chats with restored drafts and opens readable F1 help. Each step immediately shows the requested state with no query rerun or extra confirmation. The shared lane supplies opt-in frame/chrome/composer and independent-width APIs; the CLI lane supplies catalog selection, inspection, help and wiring. Root reviews phase contracts before implementation, owns real PTY evidence and final review/publication. Web parity is deferred without editing web source.

## Tasks

### Task 1: Agree shared geometry and keyboard contracts

**Id:** task-1
**Verifies:** tui-panel-polish#ac:focused-panel-controls, tui-panel-polish#ac:chat-frame-and-tabs, tui-panel-polish#ac:chrome-and-help, tui-panel-polish#ac:frame-content-and-header-actions
**Status:** complete
**Implemented-by:** 714a6c161745841075b8c009d61eac88d00167c4
**Evidence:** https://github.com/strongo/aichat/pull/29, https://github.com/tuigoff/tuigoff/pull/12, https://github.com/datatug/datatug-cli/pull/377
**Owner:** shared lane; root releases phase

Audit event owners and implement opt-in frame/chrome/composer providers plus independent side preferences. The frame persists for an empty chat with no sessions or tab provider; cover this with geometry tests. Ctrl+F searches Explorer; Ctrl+Shift+Left/Right resizes focused side; F11 focuses session tabs. Preserve Shift arrows, Ctrl+Shift+Up/Down sort, existing session chords and text editing ownership. Confirm one-row focused footer geometry and marker reuse before CLI wiring.

### Task 2: Fix stable Explorer and exact catalog inspection

**Id:** task-2
**Verifies:** tui-panel-polish#ac:stable-explorer, tui-panel-polish#ac:catalog-inspection, tui-panel-polish#ac:focused-panel-controls
**Status:** complete
**Implemented-by:** 714a6c161745841075b8c009d61eac88d00167c4
**Evidence:** https://github.com/datatug/datatug-cli/pull/377, https://github.com/datatug/datatug-cli/releases/tag/v0.77.0
**Depends-On:** 1
**Owner:** CLI lane

Remove selected-only row prefix; retain green styling. Store exact group identity and admitted object membership independently of collapse/filter presentation. Fully replace result tabs/body on object selection; reset inspection offset each selection, list exact source-group tables and style table columns. Hide unfocused side hints and wire explicit Ctrl+F search. Reproduce alignment, multi-source/collapsed/filtered group, stale tabs and zero-query behaviors with focused model tests.

### Task 3: Wire coherent chrome and readable help

**Id:** task-3
**Verifies:** tui-panel-polish#ac:chat-frame-and-tabs, tui-panel-polish#ac:chrome-and-help
**Status:** complete
**Implemented-by:** 714a6c161745841075b8c009d61eac88d00167c4
**Evidence:** https://github.com/datatug/datatug-cli/pull/377, https://github.com/datatug/datatug-cli/releases/tag/v0.77.0
**Depends-On:** 1
**Owner:** CLI lane

Wire typed header and composer metadata, frame and shared footer. Remove duplicate bottom F1; advertise F11 and existing chat navigation. Style help into readable sections with separately dimmed keys and cell-width-correct mouse actions after wrapping and scroll. F1 alone opts into a borderless lighter neutral card with continuous background, one-row vertical padding and two-column left padding; right padding is zero so the scrollbar occupies the outer right card cell. Keep three outer columns on either side and two outer rows above/below in compact terminals, and center wider cards at a maximum overall width of 104 columns (102 supplied content columns plus left padding). Shared rendering supplies actual content bounds and translated mouse origin; CLI reserves the rightmost two content cells for an outer-right-edge scrollbar and its inner gap when width permits. Verify its actual global position and matching neutral background, with no trailing padding or app-background gutter; when content fits, no foreground edge is drawn. Overflow alone shows a quiet track and distinct proportional thumb, clamped at first/middle/last positions. Add modal-owned wheel scrolling, track-click paging and standard Help-local Home/End alongside existing keys. Exclude the gutter from Actions targets, clear stale targets on every scroll and prove first/last content reachability through actual key events; preserve Home/End outside Help. Thumb dragging is unsupported because the shell currently translates clicks, not motion, into local coordinates. Remove the abandoned border toggle, state and Unicode capability branches, preserving ordinary Ctrl+B outside Help. Verify no outline/marker, increased outer margin, key contrast, scroll/draft/caret/focus, Actions mouse hits and parent-modal return at all three required sizes. Other dialogs, chat frame and side-panel dividers remain unchanged. Keep all retained operations discoverable.

### Task 4: Verify production journeys and publish dependency first

**Id:** task-4
**Verifies:** tui-panel-polish#ac:terminal-regression-evidence, tui-panel-polish#ac:catalog-inspection, tui-panel-polish#ac:chat-frame-and-tabs, tui-panel-polish#ac:focused-panel-controls, tui-panel-polish#ac:chrome-and-help, tui-panel-polish#ac:frame-content-and-header-actions
**Status:** complete
**Implemented-by:** 714a6c161745841075b8c009d61eac88d00167c4
**Evidence:** https://github.com/datatug/datatug-cli/actions/runs/38456229628, https://github.com/datatug/datatug-cli/actions/runs/38456230052, https://github.com/datatug/datatug-cli/releases/tag/v0.77.0
**Depends-On:** 2, 3
**Owner:** CLI lane focused tests; root PTY, final review and landing

Run focused regressions and package checks through WB. Root builds production CLI with released providers, walks the complete selection/search/group/table/result/resize/help/session journey in real terminals at 140x42, 100x30 and 80x24, records expected versus observed screenshots and records substantive source/UX review plus focused review of subsequent deltas. Retain the completed Astra review as history; use Sol for light UI reviews and reserve Astra for substantial architecture/code review. Fix or explicitly decline findings, recheck affected journeys and land through WB with exact remote and cleanup receipts. Implementation lanes do not start independent services or perform their own landing/release operations. Root owns bounded diagnostic child processes, any embedded test server and their teardown.

## Delivery record

Tasks are implemented by [CLI PR #377](https://github.com/datatug/datatug-cli/pull/377), landed at `714a6c161745841075b8c009d61eac88d00167c4` and publicly released as [DataTug CLI v0.77.0](https://github.com/datatug/datatug-cli/releases/tag/v0.77.0). Shared contracts shipped in aichat v0.15.0 (PR #29) and tuigoff v0.8.0 (PR #12). The feature's delivery evidence binds exact Go CI/release runs, downloaded checksum, native executable/buildinfo/signing, installed CLI read-only update discovery and published-binary PTY journeys to that release. No self-update installation was performed.

Expected first/middle/last Help reachability, accurate Actions targets and three Customer rows/metadata; the public-binary PTY observed Help card/background/right-edge scrollbar geometry at all three required sizes, navigation/track paging/Actions clicks at 80x24, and the grid/metadata at 140x42, followed by a clean exit. Earlier diagnostic journey receipts remain separate. Original Astra r8 review, released-pin acknowledgment and Sol integration/lint reviews remain historical receipts. Native Warp rendering remains unverified; thumb dragging and browser parity are outside this delivery. Root's public-delivery/check receipts and `qa-public-v0.77.0/` captures are under `/private/tmp/datatug-tui-panel-polish/`. Final bookkeeping landing and remaining owned-task cleanup are pending root execution; task completion records implementation/delivery, not completed cleanup.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
