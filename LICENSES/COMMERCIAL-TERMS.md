# DataTug Commercial Terms of Use

> **NOTICE: DRAFT VERSION — PENDING QUALIFIED LEGAL REVIEW**
>
> This document sets forth draft commercial licensing terms for enterprise and organizational self-hosted deployments of DataTug. These terms apply solely to software modules located within the `commercial/` directory and proprietary enterprise features as defined herein.

---

## 1. Preamble & Scope

DataTug is distributed under an open-core licensing model:
1. **Community Edition (Apache License 2.0)**: The core DataTug toolchain—including the command-line interface (`datatug`), terminal UI (`datatug tui`), local daemon server (`datatug serve` running locally on loopback), local query execution, TugQL parser, schema explorer, Model Context Protocol (MCP) server, and standalone utilities—is licensed under the permissive Apache License 2.0.
2. **Commercial Edition (DataTug Commercial Terms)**: Advanced centralized coordination, team synchronization, shared execution storage, team audit logging, SMS notification dispatch, and multi-tenant organizational deployments located in `commercial/` or gated by commercial feature flags require a valid, signed DataTug Commercial License.

## 2. Definitions

- **"Community Software"**: The open-source portions of DataTug contained within `cmd/`, `apps/`, `pkg/`, `internal/`, `testdata/`, `scripts/`, and `docs/`, licensed under Apache 2.0.
- **"Commercial Features"**: Software located in `commercial/` and enterprise capabilities such as `central_watch_coordinator`, `shared_execution_store`, `team_audit_trail`, and `sms_notifications`.
- **"Organizational Self-Hosted Deployment"**: Any multi-user, team-shared, or centralized service instance deployed within an organization's network, cloud VPC, or managed infrastructure.
- **"License Key"**: A cryptographically signed Ed25519 token issued by DataTug or an authorized reseller authorizing specific tiers, capacities, and feature flags.

## 3. Grant of Commercial License

Upon payment of applicable subscription or perpetual license fees and receipt of a valid License Key, DataTug grants Licensee a non-exclusive, non-transferable, revocable license to install, operate, and utilize Commercial Features strictly within the licensed capacity limits (e.g., maximum coordinators, maximum runners, maximum active team users).

### Commercial Tiers:
- **Pro**: Designed for departmental teams requiring centralized watch coordination and shared runner pools with standard capacity limits.
- **Enterprise Self-Hosted**: Designed for large organizations requiring high-availability coordinator clusters, custom runner scaling, team audit trail logging, and enterprise SLAs.

## 4. Verification and Air-Gapped Operation

1. **Offline & Air-Gapped Guarantee**: DataTug Commercial License verification operates 100% offline via local Ed25519 digital signature validation. The software does not "phone home" or require outbound network connectivity to any external licensing server.
2. **14-Day Grace Period**: If a time-limited subscription license expires, DataTug enters a fourteen (14) calendar-day operational grace period. During this period:
   - All commercial features remain fully functional to avoid service disruption.
   - Actionable warning diagnostics are logged to standard error (`stderr`) alerting operators of the expiration and instructions to renew.
   - Upon expiration of the grace period without renewal, commercial features refuse startup or fail closed until a valid key is provided.

## 5. Free Developer Guarantee

Nothing in these Commercial Terms shall restrict, encumber, or require payment for:
- Individual local development workflows using `datatug` CLI or TUI.
- Running `datatug serve` on localhost for personal exploration or agent-assisted development.
- Interacting with local databases, repositories, or local file storage.
- Using TugQL queries, MCP protocol bridges, and standard exporters.

## 6. Restrictions

Licensee shall not:
- Tamper with, alter, or circumvent cryptographic license signature verification or capacity metering.
- Sub-license, lease, or offer DataTug Commercial Features as a public managed service (SaaS) to third parties without an explicit OEM agreement.
- Remove, alter, or obscure any proprietary notices or copyright labels on commercial source code or binary distributions.

## 7. Disclaimer of Warranty and Limitation of Liability

EXCEPT AS EXPRESSLY AGREED IN A SEPARATE WRITTEN COMMERCIAL CONTRACT:
THE COMMERCIAL SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, AND NON-INFRINGEMENT. IN NO EVENT SHALL DATATUG AUTHORS, CONTRIBUTORS, OR AFFILIATES BE LIABLE FOR ANY CLAIM, DAMAGES, OR OTHER LIABILITY ARISING FROM THE USE OR INABILITY TO USE THE COMMERCIAL SOFTWARE.

---

*For commercial licensing inquiries, enterprise support contracts, or custom volume agreements, contact: sales@datatug.app*
