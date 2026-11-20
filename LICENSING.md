# DataTug licensing and project plans

DataTug uses **one CLI executable** for free and paid use. The product boundary
follows [DataTug pricing](https://datatug.app/en/pricing/), not a separate
enterprise CLI, the server's listen address, database location, runner count,
or whether the user works for a company.

This document records the owner-approved product boundary as of 2026-10-29.
It describes source scopes and the intended plan rules separately from current
runtime enforcement. Commercial terms remain a draft; this update does not
supply missing dependency rights or approve publication of a composite binary.

## Product boundary

| Plan / project | Project and user rule |
| --- | --- |
| Free personal private | Unlimited owner-only private projects. |
| Free public | Unlimited public projects, with no plan cap on public-project users or editors. GitHub-hosted public projects use their public Git repository as the project source. |
| Pro shared private | Five shared private projects, each with up to five authenticated human users, including the owner. Every permitted user counts regardless of role or activity. |
| Business shared private | Unlimited projects and invitations. The first paid month has no extra-active-user charges; afterward seven monthly active humans are included. Additional usage follows the purchased subscription terms. |
| Enterprise | Custom terms agreed explicitly; no additional capabilities or guarantees are implied by this document. |

CLI, TUI, local queries, Checks, MCP and CI workflows may be used personally
or for business within those project/plan rules. Public sharing and owner-only
private use are free. Sharing a private project requires the appropriate paid
plan; there is no invented free shared-private team allowance.

Public projects still require applicable read/write authorization. A public Git
repository is not permission to publish credentials, private database rows or
query results. Project publicity must come from trusted project/provider state,
not a local boolean, arbitrary repository URL, or a guessed network address.

## Access is different from Business activity

Business activity means a distinct authenticated human actually editing a query
or executing a data query, including local execution in a shared cloud project.
Loading a table or dashboard counts when it executes a query. Viewing metadata
(table/query lists, schemas or dashboard definitions), opening an editor, or
merely having access does not count as activity.

Count a human once per Business subscription billing month, across that
subscription's applicable shared projects. This is not a rolling 30-day window,
a lifetime seat, or a count across every tenant on one server. Runners, CI jobs,
service tokens and retries are not additional human seats; automation must not
hide the human to whom qualifying activity is attributed. Free public projects
and owner-only private projects do not consume shared-private active-user capacity.

The published Business rule adds prepaid capacity to monthly included capacity;
unused monthly capacity does not roll over. This is a product rule, **not a claim
that every prepaid billing path is implemented**. The inspected backend usage
close path rejects non-empty prepaid capacity pending its partial-window policy.
Prices, discounts, refunds and purchased limits belong to the subscription terms;
this CLI document does not create another billing schedule or price list.

When a shared-project subscription ends, the published policy makes its shared
projects read-only and disables their AI. Free personal and public projects
retain their Free limits. Access still follows current project permissions.
Purchased limits and their applicable version must be preserved; the live pricing
page must not silently reduce an existing customer's purchased entitlement.

## Source licence scopes

| Source | Licence |
| --- | --- |
| Root `main.go`, `apps/`, `pkg/`, `internal/`, `testdata/`, `scripts/`, `docs/` | Apache License 2.0, subject to separately identified third-party material. See [LICENSE](LICENSE) and [Apache text](LICENSES/Apache-2.0.txt). |
| `commercial/` | Proprietary components; [commercial terms](LICENSES/COMMERCIAL-TERMS.md) are **DRAFT — PENDING QUALIFIED LEGAL REVIEW**. |
| Third-party dependencies and embedded resources | Their own applicable licences and attribution requirements; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). |

Product plan limits do not add restrictions to code already licensed under
Apache-2.0. This matrix does not relicense third-party code. Commercial grants
must identify the covered components explicitly, rather than claiming that a
feature name or runtime flag converts Apache-scoped implementation into
proprietary software.

## One composite executable, not an Apache-only binary

The default build imports `commercial/licensing` from
`apps/datatugapp/commands/cmd_daemon_watch.go`. An interface boundary does not
exclude that implementation from the executable. The source scope table is
therefore not an aggregate Apache-only redistribution grant for the binary.

The selected `github.com/sneat-co/sneat-go-core` dependency carries the Commons
Clause condition. Shared authorship and a relicensing roadmap do not resolve
that restriction. An explicit applicable permission, a properly released
upstream licence change, or replacement of the restricted implementation is
required before describing the affected distribution as unrestricted Apache.
Core dependencies also have per-directory scopes; inspect the actual selected
versions and compiled packages instead of relying only on their root licence.

The basic embedded MCP App is separate from the full hosted visual experience.
AG Grid and AG Charts must not be redistributed in the CLI or other external
packages. Hosted contractual rights remain a separate review requirement.

## Current enforcement and release boundary

The legacy coordinator checker verifies a supplied Ed25519 key and its
`central_watch_coordinator` feature. With no key it emits guidance and returns.
It does **not** resolve public versus private projects, owner-only versus shared
access, Pro's project/user limits, or Business billing-month activity. Its lack
of a key is not proof of a free entitlement, and a valid legacy key is not proof
of a current Pro/Business subscription.

Use the existing hosted project-access and Business activity services for those
server-authoritative decisions; do not introduce another CLI billing counter.
The [implementation contract](docs/licensing-pricing.md) names the source paths,
journey, acceptance criteria and gaps. Local execution reports do not prove that
execution happened. Authentication and project authorization remain mandatory.

The existing verifier's grace-period calculation is not a promise of continuous
expiry enforcement or billing renewal. Capacity helper methods and feature
constants are not evidence that every advertised capability is metered or gated.
This alignment changes guidance only; it does not enable charges, hosted uploads,
payment activation, new licence issuance, or a release. The pricing page currently
labels checkout as test mode; planned hosted AI allowances are not live entitlements.
