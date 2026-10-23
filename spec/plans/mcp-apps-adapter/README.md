---
format: https://specscore.md/plan-specification
status: Executing
---

# Plan: Mcp Apps Adapter

**Status:** Executing
**Source Feature:** mcp-apps-adapter
**Date:** 2026-10-23
**Owner:** alex
**Supersedes:** —

## Summary

Deliver the single adapter Feature in dependency order, keeping all ten acceptance contracts and independent review gates. User task-scoped autonomous authority supplies approval transitions after independent review; it is not a claim of human artifact inspection.

## Approach

Begin with the small upstream precision dependency, then immutable session protocol projection, authorized registry, embedded UI, real-host acceptance and parent-owned public delivery. No AC is deferred. All source edits wait for independent formal Feature/Plan review.

## Tasks



### Task 1: Review upstream precision dependency

**Verifies:** mcp-apps-adapter#ac:precision-reuse
**Id:** task-1
**Status:** complete
**Implemented-by:** datatug/ai-plugin@d4ff0c5beac346eb8ead527144630978c5052ff5

In ai-plugin lib/rows.js replace lossy noninteger Number comparison with exact decimal comparison; preserve stable/null semantics. Run precision/parity fixtures, independently review, and publish the approved upstream source through parent WB ownership before pinning its exact license/path/commit/hash in CLI vendor refresh.

### Task 2: Project protocol per session

**Verifies:** mcp-apps-adapter#ac:negotiation, mcp-apps-adapter#ac:privacy
**Id:** task-2
**Status:** complete
**Evidence:** /private/tmp/mcp-apps-review-task2-r1-20261023.md

In pkg/mcpserver add receiving middleware using actual GoSDK ServerSession InitializeParams and ID for immutable per-session tools/list projection and tools/call guards. Snapshot first real stdio initialization once, reject later per-request/sessionless capability mode flips in either direction and test bound scope remains unchanged. Use explicit private envelopes and static resource metadata, test two simultaneous capabilities and distinct UI-only secret sentinel absence (initial model-authored arguments are already visible); existing plain MCP tests remain unchanged.

### Task 3: Retain authorized bounded capabilities

**Verifies:** mcp-apps-adapter#ac:binding, mcp-apps-adapter#ac:bounds, mcp-apps-adapter#ac:execution
**Id:** task-3
**Status:** complete
**Evidence:** /private/tmp/mcp-apps-review-task3-r2-20261023.md

Layer memory-only registry over existing saved-query execution and contract validation, with fresh authority/definition checks and specified expiry/size/concurrency limits. Add controlled-clock revocation/replay/overflow tests plus daemon auth/grant/write/parameter negatives without new SQL endpoints.

### Task 4: Build embedded official App

**Verifies:** mcp-apps-adapter#ac:precision-reuse, mcp-apps-adapter#ac:views, mcp-apps-adapter#ac:ux, mcp-apps-adapter#ac:sdk-offline
**Id:** task-4
**Status:** complete
**Evidence:** /private/tmp/mcp-apps-review-task4-r2-20261023.md, /private/tmp/mcp-apps-ui-r2-integrated.log

Build browser renderer with locked published official SDK, exact upstream helpers, deterministic bundle and go:embed assets. Implement automatic five-view display, precise DTO, sorting/paging/record/history/copy/parameters, single toolbar and all accessible loading/error states; record vendor/build provenance.

### Task 5: Prove real official-host journey

**Verifies:** mcp-apps-adapter#ac:privacy, mcp-apps-adapter#ac:execution, mcp-apps-adapter#ac:precision-reuse, mcp-apps-adapter#ac:views, mcp-apps-adapter#ac:ux, mcp-apps-adapter#ac:sdk-offline
**Id:** task-5
**Status:** planning

Use official App+AppBridge/reference sandbox with explicit capability and privacy-safe logging, actual child CLI stdio/authenticated serve/committed Chinook; perform automatic render and parameter rerun. Exercise origin/source/network negatives, transcript sentinels, precision copy/sort and narrow/wide light/dark keyboard states; fixtures own and clean all processes/temp files.

### Task 6: Review and deliver public binary

**Verifies:** mcp-apps-adapter#ac:delivery, mcp-apps-adapter#ac:sdk-offline
**Id:** task-6
**Status:** planning

Parent drives independent implementation review, exact-head CI and WB landing using the repository datatug-release delegate/automatic release convention, never manual tags. Verify the exact downloaded public binary offline/no Node with the same child and official-host browser acceptance, update accurate tested-host/setup/privacy/SQLite website docs and clean task worktrees after receipts.

## Verification and Commit Contract

Every implementation commit includes a Verifies: trailer listing satisfied mcp-apps-adapter#ac:<id> references from its task, plus named test receipts. Verifies trailers establish traceability, not test success. Parent controls publication and landing; no config/hook bypass, unrelated scan changes or website demo CTA changes.

## Deferred AC Coverage

None; all ten ACs are covered above.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
