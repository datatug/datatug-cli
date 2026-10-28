# DataTug Licensing Architecture

DataTug uses a transparent open-core model designed to empower individual developers and small teams while providing sustainable commercial licensing for enterprise-scale centralized infrastructure.

---

## Authoritative License Scopes

| Directory / Path | Applicable License | Terms & Governing File |
| :--- | :--- | :--- |
| `main.go` (CLI root) | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `apps/` | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `pkg/` | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `internal/` | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `testdata/` | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `scripts/` | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `docs/` | **Apache License 2.0** | Permissive open source ([LICENSE](LICENSE), [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt)) |
| `commercial/` | **DataTug Commercial Terms** | Proprietary commercial software ([LICENSES/COMMERCIAL-TERMS.md](LICENSES/COMMERCIAL-TERMS.md)) |

---

## Capability Matrix

The following matrix outlines feature availability across Community and Commercial tiers:

| Capability | Community Edition (Apache 2.0) | Commercial Edition (Pro / Enterprise) |
| :--- | :---: | :---: |
| **CLI & TUI** (`datatug`, `datatug tui`) | ✅ Included (Unlimited) | ✅ Included |
| **Local Server** (`datatug serve` on localhost/loopback) | ✅ Included (Unrestricted) | ✅ Included |
| **Local Query Execution & TugQL Engine** | ✅ Included | ✅ Included |
| **Database Schema Introspection & Modeling** | ✅ Included | ✅ Included |
| **Model Context Protocol (MCP) Server** | ✅ Included | ✅ Included |
| **Single-project Local Automation** | ✅ Included | ✅ Included |
| **Central Watch Coordinator Daemon** | Evaluation / Local Standalone | ✅ Licensed (`central_watch_coordinator`) |
| **Distributed / Team Watch Runners** | Evaluation / Local Standalone | ✅ Licensed (`team_runners`) |
| **Shared Multi-User Execution Store** | ❌ Commercial License | ✅ Licensed (`shared_execution_store`) |
| **Centralized Team Audit Trail** | ❌ Commercial License | ✅ Licensed (`team_audit_trail`) |
| **Carrier SMS Alerts (Twilio)** | ❌ Commercial License | ✅ Licensed (`sms_notifications`) |
| **High Availability & Cluster Failover** | ❌ Commercial License | ✅ Licensed (Enterprise tier) |

### Free Individual Developer Guarantee
Local developer workflows—running `datatug serve` on localhost, inspecting database schemas, executing queries, authoring TugQL scripts, and running agent workflows via MCP—will **never** require a commercial license key. The local server fails open and unrestricted.

---

## Architectural Separation of Concerns

1. **Source scopes and default executable (`pkg/`, `apps/`, `internal/`)**:
   These source directories use the Apache 2.0 scope above. The default CLI is a composite executable: `apps/datatugapp/commands/cmd_daemon_watch.go` imports `commercial/licensing` and installs its `CoordinatorEntitlementChecker` through `watchdaemon.EntitlementChecker`. Interface separation does not mean the default binary excludes commercial code. The checker and its existing behavior remain in place; the commercial terms remain **DRAFT — PENDING QUALIFIED LEGAL REVIEW**. This source map is not an aggregate binary redistribution grant.
2. **Commercial Subsystem (`commercial/`)**:
   Commercial features, including the offline Ed25519 entitlement engine and `CoordinatorEntitlementChecker`, reside strictly in `commercial/licensing/` and are governed by the [DataTug Commercial Terms](LICENSES/COMMERCIAL-TERMS.md).

The default CLI also embeds `pkg/mcpserver/app/index.html`, an MCP App resource. Its build manifest and third-party notices are separate source files retained by container packaging; they are not additional `go:embed` inputs. This resource is distinct from the full hosted DataTug UI and from AG Grid/Charts. Container packaging retains the five own license/map/terms texts, the MCP build manifest and dependency notices; final artifact and legal review remain publication gates.

---

## Third-Party Dependencies and Commons Clause Notice

DataTug utilizes select shared libraries from the Sneat ecosystem:
- **`github.com/sneat-co/sneat-go-core`**: Certain utility packages (`apicore`, `verify`, `twiliosms`) originate from `sneat-go-core`, which incorporates the Commons Clause v1.0 license condition on top of Apache 2.0.
- **Relicensing & Decoupling Roadmap**: Sneat.co and DataTug share common authorship. Upstream relicensing to pure Apache 2.0 or local cleanroom interface decoupling is an active pre-publication roadmap item (see `datatug-core/docs/ROADMAP.md`).
- For complete attributions and upstream license texts, refer to [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
