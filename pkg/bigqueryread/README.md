# DataTug BigQuery CLI composition

This package connects the released `github.com/dal-go/dalgo2bigquery v0.3.0`
protocol to the actual `datatug query bigquery` Cobra commands. Fixture tests
exercise protected preview, approval, one submission and two pages through the
real command tree. Live Google authentication and paid execution have **not been
executed** for this change. Source rights/admission and both runtime live gates
remain open.

`--file` accepts typed JSON containing `profile` and scalar DTQL `query`.
Alternatively, `--source-profile` accepts the reviewed profile JSON and `--file`
accepts scalar DTQL JSON separately. Each source/query input is bounded to
256 KiB, with unknown fields, duplicate keys, invalid Unicode, unsafe query
shapes and excessive depth rejected before policy substitution or authentication.
Only explicit fields, bounded scalar conditions/order and a positive limit are
supported. Whole-AST guards precede generic policy/substitution paths, and the
released compiler validates the policy-effective result. The copied driver AST
normalizes DALgo policy `In` arrays to constant slices; empty lists are supported,
1000 elements are the limit, and nested, null, mixed-type or expression elements
refuse before authentication. The generic policy AST is never mutated.

The commands are:

- `connect --auth google --ledger <absolute-private-directory>` deliberately
  opens Google consent using the existing credential storage owner. Its callback
  binds IPv4 loopback before browser launch, uses random state and PKCE, admits
  one matching GET callback, launches an owned cancellable OS opener, and closes
  its listener and accepted HTTP connections under a bounded context. Read-only plus
  `openid` is the baseline; `--enable-cancellation` separately requests BigQuery
  scope. It separately attests the fresh consent token and stored credential and
  refuses success if their account/grant bindings differ. Other operations never
  launch consent or a listener.
- `preview` requires explicit `--auth adc|google`, `--execution-project` and
  `--maximum-bytes-billed`. `--session-budget-bytes` defaults to that exact cap;
  `--page-size` is bound by the preview. Stdout includes effective SQL/typed
  parameters, observed source, independently verified Google principal, estimate,
  cap, bounds and approval digest. `--preview-out` optionally writes a private
  `preview-*.json` artifact within the ledger directory.
- `run` accepts `--preview` and exact `--approve-digest`, with the same protected
  source/query/context. It reauthorizes and rechecks before one submission and
  emits the first typed page. `--receipt-out` writes only receipt/cursor to a
  private `receipt-*.json` artifact within the ledger directory, without rows. Artifacts are committed before stdout, and operation, persistence
  and output failures remain distinct.
- `page --receipt <receipt-or-page-json>` reads the next page of that job.
  `--cursor` can supply the original opaque cursor separately. `--reconnect` is
  deliberate same-subject reauthorization, preserving original approval,
  counters, deadline and job; it cannot replay a query. `status` and separately
  enabled `cancel` use the same receipt and truthful authoritative control
  outcome. Control and the driver's local authoritative `Snapshot` share one
  bounded context. The latest receipt/counters/billing and original issued cursor
  are exported with meaningful control outcomes, including partial failures. A
  failed snapshot preserves the previous artifact and both errors. Control
  outputs do not read or replay rows, even after execution expiry.

The opt-in **operator-only WDI pilot** adds `--operator-pilot` to `preflight`,
`preview`, `run`, `status` and `cancel`. `preflight` checks the current user's
granted BigQuery and `openid` scopes plus same-token UserInfo identity without
calling BigQuery. Pilot preview accepts only
`bigquery-public-data.world_bank_wdi.country_summary` in `US`, the two native
fields `country_code` and `short_name` ordered by both, limit 2, `demodb-dev`,
page size 1, and both cap and session budget set to `10485760` bytes. It runs a
dry-run estimate. The pilot does not allow result paging. Pilot run emits only a
receipt, without delivering the initial response's rows or making a result-page
request. Control operations likewise emit only the updated receipt.

After reviewing the preview, an operator supplies `--operator-pilot-policy` on
run and control. This JSON file must be in an operator-owned private directory
(mode 0700), itself mode 0600 or stricter, and contain exactly:

```json
{
  "format": "datatug-bigquery-operator-pilot/1",
  "approvalDigest": "<exact reviewed preview digest>",
  "rightsReviewRef": "<accepted exact source-rights review reference>",
  "executionProject": "demodb-dev",
  "maximumBytesBilled": "10485760",
  "sessionBudgetBytes": "10485760",
  "allowancePath": "<absolute path in same private directory>/submission.claim"
}
```

The reviewed source profile must carry that same rights reference and a
separately reviewed publisher reference. The CLI checks their consistency; it
cannot establish legal clearance from a reference string. Before `run` can
submit, it creates and syncs the claim file with exclusive creation. The claim
stays consumed across process restarts, failed or ambiguous submissions, and
zero-billed jobs. Changing or deleting the policy fails closed for controls;
never delete the claim to retry an uncertain submission. Public users must not
receive `bigquery.jobs.create` on the Sneat-funded execution project. This
operator pilot has no built-in source-row cache or snapshot output; BigQuery may
still materialize temporary provider-side results under its own retention rules.

Every operation reuses the same private `--ledger` directory. A new directory
is a new authorization and budget session, not continuation of an existing job.
No cap/row/page/wall bound can be renewed through a continuation flag. Ctrl-C,
local close and expiry keep known job receipts or `submission_unknown`; cancellation
is never inferred from stopping local waiting. Error output preserves existing
receipt authority when available and never includes raw service errors or tokens.
Policy stderr contains policy/rule/field metadata, never predicate literals,
bindings or explanations; explicit preview JSON retains the requested parameters.
Output is `--format json` only. Preview recovery is bounded to 2 MiB and page
recovery to 5 MiB; compact receipt artifacts retain the 256 KiB input bound.

`--as/--role/--group/--var/--policy/--policies-dir/--no-policies` retain DataTug's
policy conventions. Those labels do not attest Google execution identity.
`--execution-project` remains distinct from saved-query `--project`.

Google-user ADC is an explicit opt-in and does not choose an execution project.
Workload/service-account ADC is refused here; its authoritative identity belongs
to an operator-injected provider in the driver's server composition. `--auth
google` accurately names the existing user-controlled stored OAuth grant. Scope
options and stored requested scopes are not capability evidence. Only current
Google token-response `scope`, explicit expiry, and same-token UserInfo `sub`
attest the current grant. Missing `scope`, `openid`, BigQuery grant or stable
subject refuses with setup guidance; `cloud-platform` never substitutes for the
BigQuery grant. No token enters this package's ledger, artifacts or diagnostics.

For this Go CLI, generation binds a persistent nonsecret private authorization
session nonce, verified Google-user subject and exact current granted-scope set.
Refreshing an access token retains that generation only after identity/grants are
verified again. Subject/grant changes invalidate pending approvals; deliberate
same-subject job rebind is required for grant changes. The returned transport uses
an `oauth2.StaticTokenSource` snapshot of the exact verified token over the driver
request guard. This narrow Go session interpretation does not change browser GIS
rules, and remains subject to independent review.
