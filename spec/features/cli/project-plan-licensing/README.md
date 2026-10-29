---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Single-CLI project-plan licensing

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/project-plan-licensing?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/project-plan-licensing?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/project-plan-licensing?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/project-plan-licensing?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

One CLI follows the published project-sharing and Business activity model; distinguish source scopes and implemented controls from remaining integration and legal gates.

## Problem

The old capability matrix imposed a blanket coordinator/enterprise boundary that conflicts with the owner-approved pricing page. The default CLI also includes proprietary components and a Commons Clause dependency despite its Apache-scoped source directories. Correct product guidance without inventing billing authority or claiming distribution clearance.

## Behavior

### REQ: single-cli-project-boundary

Keep one CLI and apply the published Free, Pro and Business project-sharing model in [LICENSING.md](../../../../LICENSING.md). The owner approved this product boundary on 2026-10-29; this Draft feature does not mark its remaining integration or legal grants complete.

### REQ: server-authoritative-plans

Reuse existing project-specific subscription and activity services. Do not derive payment eligibility from a server address, caller-supplied tier, local public flag or legacy coordinator key. Preserve authentication, project permissions and existing Apache source rights.

### User journey and observable results


1. A person runs the CLI, including against a remote database, or serves an
   owner-only private project. **Result:** no paid sharing subscription is
   required merely because a server, remote database or business user is involved.
2. People collaborate on a project backed by a verified public Git repository.
   **Result:** no plan cap on public-project users/editors; current project and
   provider write permissions still apply. Database rows and secrets stay private.
3. An owner invites another human to a private project. **Result:** the existing
   server-authoritative paid-project service resolves the payer and entitlement.
   Free has no shared-private allowance. Pro's five project/five user limits
   include the owner and passive participants; Business invitations have no cap.
4. A Business member views lists or schemas and does nothing else.
   **Result:** no query-activity chargeable event is created.
5. A member edits an actual query or executes it, including local execution in
   a shared cloud project. **Result:** the Business service handles authenticated
   activity context/reporting; retries do not create another human or period.
6. The Business month ends. **Result:** the published first-month exception,
   seven included active humans afterward and agreed capacity/usage terms apply
   per subscription billing month, not a rolling window or whole server count.
   Non-empty prepaid close remains unavailable in the inspected implementation.
7. A paid subscription ends or membership is revoked. **Result:** apply the
   existing permission and read-only/AI eligibility rules. No old signed context,
   local flag or legacy coordinator key restores access.

### Existing authority and implementation gaps


Inspected backend source at `bbc5e7c880a92ac5ca68a016b1ac42e1fea77562`:

- `facade4datatug/shared_project_plan_selector.go` and
  `shared_project_paid_access.go`: project-specific payer/plan and purchased
  limits; do not accept a caller-supplied tier as authority.
- `facade4datatug/github_project_access.go`: provider/project access is checked
  separately from subscription state. Publicity must be provider-authoritative.
- `facade4datatug/business_query_activity_service.go` and `query_activity.go`:
  reuse `BusinessQueryActivityService.IssueContext` / `Report`, authenticated
  context and accepted reports. Local reporting is not execution attestation.
- `facade4datatug/business_usage_period.go`: the inspected close rejects a
  non-empty prepaid-capacity basis. Do not document prepaid close as delivered.

The website's pricing policy source is
`datatug/websites/packages/site-kit/src/pricing/data.mjs`. Prices remain there
and in authoritative purchased terms, not a second CLI rate table. Test-mode
checkout and planned hosted AI allowances must not be described as live billing.

### Current implementation slice


- Consistent product/source scope documentation for one composite CLI.
- A coordinator notice that states the actual free/shared-private boundary.
- Regression coverage for that notice, alongside existing valid/invalid key,
  feature and environment-key tests.

No authentication or key-verification decision changes. The legacy
`commercial/licensing.CoordinatorEntitlementChecker` still runs at startup;
its no-key return is not project-plan enforcement. Feature constants, capacity
helpers and a signed legacy coordinator key are not a Pro/Business entitlement.
No payment state, issuer keys, upload enablement or production service is changed.



## Acceptance Criteria

### AC: guidance-matches-pricing

**Requirements:** #req:single-cli-project-boundary

Given a coordinator starting without a key, when it emits guidance, then it states owner-only private and shared public use is free, shared private use needs an appropriate plan, and startup does not establish plan entitlement. Existing valid/invalid-key and environment-key decisions remain unchanged. Covered by `TestCoordinatorEntitlementCheckerFreeUsageNotice` and existing checker tests.

### AC: source-and-distribution-scopes-are-explicit

**Requirements:** #req:single-cli-project-boundary

Given the documented one-CLI build, when a user reads the licensing map, then the proprietary import and Commons Clause dependency are disclosed, commercial grants remain draft, and no feature-name claim restricts Apache-scoped source.

### AC: full-journey-integration-remains-gated

**Requirements:** #req:server-authoritative-plans


These are not claimed implemented by a notice/documentation change:

- [ ] Resolve visibility and current membership through trusted server/provider
  state. Unknown/unavailable authority must never be interpreted as public or
  as a paid entitlement; preserve currently authorized free use independently.
- [ ] Exercise owner-only private and public collaboration without paid keys,
  plus eligible/ineligible shared-private Pro and Business flows end to end.
- [ ] Exercise Pro project/user limits including owner and passive invitees;
  preserve each purchased limits version instead of hardcoding current defaults.
- [ ] Verify metadata reads and editor focus create no Business activity, while
  actual query edits and query executions use the existing activity service.
- [ ] Verify human deduplication, retry idempotency, local-report semantics,
  revoked membership, stale contexts and exact billing-month boundaries.
- [ ] Preserve the Business first-paid-month exception; complete and review
  non-empty prepaid/partial-window closing before enabling that billing path.
- [ ] Verify subscription-end read-only behavior and AI denial independently of
  the legacy key's cryptographic grace calculation.
- [ ] Resolve Commons Clause permissions, proprietary distribution grants,
  issuer/trust configuration and final aggregate-artifact notice review before
  publishing the affected official binary.

The AG Grid/Charts hosted contractual boundary remains separate. No external
package receives those assets or rights through this pricing decision.


## Open Questions

- Which legally authorized final grant covers free operation, redistribution and building the proprietary components of the official composite binary? Product approval is recorded; legal finalization remains separate.
- Which explicit permission, upstream release or replacement resolves the selected Commons Clause dependency?
- What approved partial-window policy will permit non-empty prepaid-capacity closing? Preserve the inspected refusal until that policy is implemented and reviewed.
- How will a self-hosted server obtain trusted current project/entitlement evidence, including offline expiry, without accepting local assertions as authority? No new protocol is invented here.

---
*This document follows the https://specscore.md/feature-specification*
