---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Terminal panel polish and predictable inspection

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-panel-polish?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-panel-polish?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-panel-polish?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-panel-polish?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Follow-up to the released terminal refinements: stable Explorer alignment, exact catalog inspection, focused panel controls, deliberate help and coherent chat chrome.

## Problem

Selection currently shifts Explorer labels, catalog groups show generic instructions instead of their objects, and result tabs remain above object inspection. Always-visible panel hints, ambiguous search, an unstyled help sheet and model information mixed into shortcuts weaken navigation. Chat tabs and composer need a single coherent frame and focus owner.

This follow-up preserves the historical `tui-refinements` and `unified-tui` artifacts. It changes terminal presentation and discovery only; browser parity is a future follow-up.

## Behavior

1. Open DataTug: a padded gold title, dim separators and F-key hints, distinct project value, and compact `F8 Server LIVE  F1 Help` communicate context. Model information belongs in the composer.
2. Focus Explorer and type or press Ctrl+F to search. Move selection without changing label coordinates. Selected rows stay mint/green with a neutral background and no additional selection marker.
3. Select Tables: Inspect immediately lists the exact group's admitted catalog tables, including when filtering or collapsing hides descendants. Select a table: replace the group with bold mint name and distinct column/type hierarchy. Select a result: restore its own Row/Column/Result inspection. These transitions execute no query and retain original results.
4. Resize the focused side panel with Ctrl+Shift+Left/Right; only that side's preferred width changes. Context and resize hints appear only in the focused panel footer.
5. Press F11 to focus chat tabs, move with Left/Right, activate with Enter and return with Esc. Ctrl+Tab/Ctrl+Shift+Tab remain available. Tabs align with chat content; a thin frame surrounds transcript and composer. The active tab matches the frame, inactive tabs are darker with a frame-colored bottom seam. One focus owner remains visible, with the composer marker including its cap.
6. Press F1: a borderless, lighter neutral card presents styled sections and dim keys for the current control and global commands, with no outline or active left marker. Its continuous background fills the content, one-row vertical padding and two-column left padding; the scrollbar ends at the outer right card cell with no trailing padding or app-background gutter. Give the dialog more outside space: at compact sizes keep three columns on each side and two rows above and below; wider layouts center a card at most 104 columns wide overall, including 102 supplied content columns and two left padding columns. When content overflows, a scrollbar at the outer right card edge has a quiet track and visible proportional thumb showing first, middle and last positions. Wheel, arrows/PageUp/PageDown/Home/End and track clicks scroll Help; clicking above/below the thumb pages, while dragging is unsupported. The scrollbar gap cannot activate Actions. Scrolling and clickable Actions use the card's actual content coordinates and retain modal ownership, composer draft/caret and focus. There is no border-style setting, Ctrl+B toggle, Unicode border-capability branch or environment guessing. Bottom toolbar omits duplicate F1. Other dialogs, the chat frame and side-panel dividers retain their appearance and ownership.

Ordinary typing, Shift navigation, exclusive composer/grid focus, session draft and delete fences, secure query derivation, immutable query/result retention and all existing actions remain intact. Shared chrome APIs are opt-in for compatibility with other consumers. Real terminal checks cover 140x42, 100x30 and 80x24.

## Dependencies

- tui-refinements

## Acceptance Criteria

### AC: stable-explorer

Given any catalog row, when selection moves or focus changes, then label coordinates remain fixed, the selected row stays green, and no selection marker is added. Ctrl+F focuses search and literal text remains searchable.

### AC: catalog-inspection

Given multiple sources with Tables groups, when a group or object is selected, then Inspect replaces prior tabs/content with only that exact admitted catalog selection. Group listings include collapsed/filtered descendants, exclude other sources, support scrolling to the last entry and execute no query. Returning to a result restores result inspection without losing retained state.

### AC: focused-panel-controls

Given both side panels, when Ctrl+Shift+Left/Right resizes one focused side, then the other preferred width remains unchanged. Footer hints are visible only for the focused panel and the final content row remains reachable at compact sizes.

### AC: chat-frame-and-tabs

Given an empty chat with no sessions or tab provider, or chat tabs with transcript and composer, when switching tab/focus or resizing, then transcript and composer retain a thin frame with consistent horizontal geometry. Active/inactive tab surfaces and the composer cap marker follow the requested hierarchy. F11 and existing session chords are discoverable and preserve drafts, retained results and close/delete fences.

### AC: chrome-and-help

Given normal or narrow terminals, when rendering header, composer, footer and F1 help, then function keys are dim and bracket-free, labels are distinct, model metadata is inside composer, and bottom F1 is absent. F1 is a borderless lighter neutral card with continuous background, one-row vertical/two-column left inner padding and no outline or active marker. Compact layouts have three outer columns on each side and two outer rows above and below, while wider cards center at a maximum overall width of 104 columns. Overflow alone shows a scrollbar flush with the outer right card edge, without trailing padding or an app-background gutter, with a muted track, distinct proportional thumb and clamped first/middle/last positions, including tiny viewports. Arrows/PageUp/PageDown/Home/End, wheel and track clicks scroll only Help and reach its first and last content; Home/End outside Help retain their normal owner; no thumb dragging is claimed. The scrollbar and its reserved gap are excluded from Actions hitboxes. Wrapping, scrolling and Actions mouse targets use actual card bounds and content origin without losing draft/caret/focus or mutating the underlying workspace. No border choice, Ctrl+B binding, persistence or Unicode capability branch is added. Help opened over another dialog preserves that dialog and its focus when closed; other dialogs, chat frames and side-panel dividers remain unchanged.

### AC: terminal-regression-evidence

Given focused behavioral tests and production terminal journeys at 140x42, 100x30 and 80x24, when the follow-up is reviewed, then evidence states expected versus observed behavior for selection, inspection, search, help, tab/session focus, width changes and composer/grid ownership. Existing security and retained-state tests continue passing. Root owns final Astra review, released-provider verification, WB landing and cleanup.

### AC: frame-content-and-header-actions

Given framed query-editor, center-panel and expanded-result content at 140x42, 100x30 and 80x24, when a visible sentinel occupies the last admitted content cell, then it survives frame composition and its mouse target remains reachable. Project, Server and Help header actions dispatch at their rendered cell coordinates even above the center column; frame borders and tab seams remain inert. The shared provider's geometry and mouse tests bind these behaviors to the production shell, not only a bounded screenshot.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
