# DataTug commercial components

DataTug ships one CLI for free and paid use. This directory contains proprietary
licence-verification components included in the default composite executable;
it does not define a separate enterprise CLI.

The owner-approved product boundary follows [pricing](https://datatug.app/en/pricing/):
unlimited owner-only private projects and shared public projects are Free;
shared private projects require Pro, Business or an agreed Enterprise plan.
Pro's project/access limits and Business's monthly active-user rules are
explained in [LICENSING.md](../LICENSING.md). Serving multiple public-project
users, running CI or connecting to a remote database is not inherently paid.

## Status of this implementation

`licensing.CoordinatorEntitlementChecker` is a legacy coordinator startup check.
It verifies a supplied key's signature, validity and coordinator feature. The
absence of a key emits guidance; it is not a trusted project-plan decision.
The checker has no project visibility, membership, subscription or activity data.
Do not present feature constants, capacity helpers or this startup check as
complete Pro/Business enforcement. Reuse the existing hosted services described
in the [implementation contract](../docs/licensing-pricing.md).

Signature verification itself runs locally. That does not imply all plan or
activity decisions work offline. The legacy verifier's grace calculation does
not guarantee continuous checking during long-running server operation.

## Rights and release gates

The [commercial terms](../LICENSES/COMMERCIAL-TERMS.md) remain a draft requiring
qualified review of component scope, free-use and redistribution grants, and
paid operation. Product approval does not finalize that legal text. Existing
Apache source scopes and third-party licences remain unchanged; Commons Clause
and aggregate-binary distribution issues remain separate publication gates.
