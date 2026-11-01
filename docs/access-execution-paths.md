# CLI execution boundary inventory

This inventory describes source paths, not completed MVP acceptance. A native
read-only handle limits mutations; it does not establish current membership,
connection protection, an Agent subject, or retained-result expiration.

| Surface | Current disposition | Remaining boundary work |
|---|---|---|
| `db get`, `db preview` | Shared Core authority and guarded result transport; explicit `--agent` selects Agent routes without human-grant fallback | Broader query shapes and exact-object journey acceptance remain separate |
| `query run` structured/TugQL, saved structured queries | Existing local policy evaluator and read-only source handles | Unified Core connection/source authority and retained-result lifetime |
| `query run --sql`, saved SQL, MCP saved SQL | Explicit read-only raw compatibility for connections without a control plane (OQ-13); loaded policy and native capability gates still apply | Space-recorded raw reads require their governed grant route |
| `query redis` | Explicit allow-listed raw read compatibility (OQ-13) | Protected connection authority; this is not a policy-rewritten structured query |
| BigQuery and Typesense commands | Dedicated provider read safeguards | Unified connection authority and provider-specific acceptance |
| `db copy --from`, `db export`, `compare database` | Source-read openers; SQLite source opens native read-only | Protected whole-source access must reject restrictive object rules, through Core |
| `db copy --to` | Explicit writer; remote compatibility notice, no new confirmation gate yet | Complete the published warning period before remote confirmation/Space refusal; OQ-15 remains an explicit exception |
| `updateUrlConfig` | Legacy execution, warning-only compatibility period | Retire after at least two weeks of published notice; it is outside unified grants |
| SQLite TUI | Native read-only data and schema handles | Unified protected-source authority; read-only is not permission to read every table |
| Schema/scan/table metadata | Existing source-read paths and local capabilities | Apply the same connection and exact-object authority before live metadata I/O |
| MCP TugQL | Not implemented by this checkpoint | Separate Agent identity/token, generic Core query/result contract, governed tool and native acceptance |
| Checks and Watches | Existing local-owner/capability gates; no new standing authority is granted here | Standing Service subject/policy, protected TugQL-only execution and lifetime proof |
| Local result/session/artifact stores | DataTug-owned local persistence, not source-write permission | Grant-bound retained-data ownership, purge and replay proof |
| Owned demos and pinned public fixtures | Explicit fixture paths, not production protection evidence | Keep fixture and real-source acceptance separate |

`--no-policies` is preserved. Its read profile does not create a data-write grant.
Local project ownership and the database read profile remain distinct. A flag
occurrence alone is not a bypass; each route must be traced to its authority,
source mode and the OQ-13/OQ-15 compatibility exceptions.

## Native-opener ratchet

[`native-openers.json`](../internal/accessboundary/native-openers.json) inventories
native-provider constructor/open references and source-opener methods, including
function-value aliases. `TestNativeOpenerInventory` parses every non-test Go file,
including platform-tagged files, and fails when the inventory changes. Each file
has a disposition, including explicitly pending Core cutovers; recording a call
does not approve its use on a protected connection. New or removed references
require review rather than automatic snapshot regeneration.

The test is static and bounded. It recognizes `database/sql`, current DALgo
provider imports, pgx/MongoDB/Redis constructors, and named source-opener methods.
It does not prove arbitrary dynamic dispatch, transitive wrappers, a provider's
internal behavior, native enforcement, or that every release path is governed.
Additional providers and newly named opener families require extending the
ratchet. Existing command-tree tests independently track CLI registration.

The required generic Core query integration must preserve supported TugQL
filters, projections, joins, aggregates, ordering and limits. A table-preview
tool or blanket denial is not a replacement for the required governed read tool.
