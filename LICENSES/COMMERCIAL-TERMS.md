# DataTug Commercial Terms — working draft

> **DRAFT — PENDING QUALIFIED LEGAL REVIEW.** The product boundary below is
> owner-approved; proposed legal grants and restrictions are not a finalized
> licence, checkout agreement, redistribution clearance or release approval.

## 1. Covered material and one CLI

DataTug provides one CLI executable. Its default build contains both Apache-2.0
source components and proprietary `commercial/` components. This draft concerns
only the proprietary components and separately contracted services. It does not
relicense Apache components or third-party material, and does not make a feature
proprietary merely because it has a plan flag or is used by an organisation.
See [LICENSING.md](../LICENSING.md) for the source scopes and actual build boundary.

A final agreement must identify the licensor with authority to grant the rights,
the covered versions/components, the customer and the purchased entitlement.
A licence key alone is not a contract or evidence of ownership of third-party rights.

## 2. Approved product boundary

The commercial product rules must match [DataTug pricing](https://datatug.app/en/pricing/).
As approved on 2026-10-29:

- **Free:** unlimited owner-only private projects and unlimited public projects.
  Public projects have no plan cap on users or editors. Personal and business
  CLI, TUI, queries, Checks, MCP and CI use follow the same project rules.
- **Pro:** adds five shared private projects, each permitting five authenticated
  human users including the owner. Every permitted user counts regardless of
  activity or role.
- **Business:** unlimited projects and invitations. The first paid month has no
  extra-active-user charges. After that, seven monthly active humans are
  included; additional usage and prepaid capacity follow the purchased terms.
- **Enterprise:** only the custom scope and commitments expressly agreed.

Merely running a server, serving many public-project users, connecting to a
remote database or running CI does not create a paid-plan requirement. Sharing
private projects is the relevant paid product boundary. Project permissions
remain separate from both payment and activity measurement.

## 3. Business activity and entitlement scope

An active human edits a data query or executes a data query in an applicable
shared cloud project, including local execution. Loading table or dashboard data
counts when it executes a query. Metadata viewing and opening/focusing an editor
alone do not count. Each human counts once per Business subscription billing
month across that subscription's applicable shared projects. Public and owner-only
private projects do not consume shared-private active-user capacity.

Invitations and passive membership are not Business activity. Runners, CI jobs,
service credentials and retries are not additional human seats and must not
conceal attribution of qualifying human activity. Accepted activity reporting
requires authenticated, project-scoped server authority; a local report is not
proof of actual execution.

Prepaid units add monthly capacity under the published product rule; unused
monthly capacity does not roll over. Non-empty prepaid closing is not yet
supported by the inspected backend close path. This draft does not activate
prepaid billing or choose a partial-window rule.

Amounts, discounts, tax, refunds, billing periods and purchased limits belong to
the applicable checkout/order and subscription terms. Preserve their version;
later website edits must not silently reduce purchased limits. Free allowances
may change only with the notice described by the pricing policy.

## 4. Free-use and distribution clauses to finalize

The final proprietary-component agreement must expressly authorize free operation
of the official composite CLI for the Free cases above, including personal
private projects and collaboration on public projects. It must identify permitted
download, internal copying, installation and redistribution of the unmodified
official binary and its required notices. It must separately define any permitted
building or modification of proprietary source. These rights must not be inferred
from the Apache licence of other directories.

Paid operation of the covered proprietary components for shared private projects
must be tied to the applicable Pro, Business or Enterprise entitlement. Do not
impose those restrictions on separately Apache-licensed source or derivative
works exercising their existing rights. Third-party grants and trademark rights
must be checked independently. These clauses are review requirements, not an
assertion that all proposed distribution rights have already been granted.

## 5. Enforcement, expiry and operation

The current offline Ed25519 verifier and legacy coordinator startup check do not
implement the product sharing and Business usage model by themselves. A final
agreement must distinguish legal obligations from implemented controls, specify
entitlement/issuer trust and operation when entitlement data is unavailable, and
state any support or service commitments expressly.

The existing verifier calculates a fourteen-day expiry grace period at the time
it verifies a key. This does not establish automatic renewal or continuous expiry
checks. The published subscription-end policy makes affected shared projects
read-only and disables their AI; personal and public Free projects retain their
Free limits. Future enforcement must preserve permissions and avoid treating a
missing key as proof of unrestricted shared-private entitlement.

## 6. Finalization checklist

Before this draft can become an effective commercial licence or release clearance:
identify the legal licensor and authority; finalize the free-use, copying,
redistribution and proprietary-source rights; resolve the selected Commons Clause
dependency; reconcile signed entitlement versions with purchased plan limits;
review expiry, termination, warranty, liability and applicable-law provisions;
and verify actual distribution notices and enforcement claims.

No prices, payment activation, AI allowances, public releases or additional
permissions for AG Grid/Charts are introduced by this draft.
