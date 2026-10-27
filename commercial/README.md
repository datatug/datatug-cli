# DataTug Commercial Engine

This directory contains commercial extensions and enterprise entitlement mechanisms for DataTug.

## Governance & Boundary

The code in this directory (`commercial/`) is proprietary and governed by the **DataTug Commercial Terms** (see [`LICENSES/COMMERCIAL-TERMS.md`](../LICENSES/COMMERCIAL-TERMS.md)).

Unlike the permissive open-source Community components (`apps/`, `pkg/`, `internal/`, root CLI `main.go`) licensed under Apache License 2.0, the commercial engine provides centralized coordination, multi-user sharing, enterprise audit logging, and team runners for organizational self-hosted environments.

## Features Gated by Commercial Licensing

- `central_watch_coordinator`: Shared centralized watch coordinator scheduling jobs across organizational runners.
- `shared_execution_store`: Centralized multi-user execution store and historical query persistence.
- `team_audit_trail`: Immutable organizational activity logging and compliance auditing.
- `sms_notifications`: Cellular SMS alerting via cloud telephony providers (e.g., Twilio).

## Architectural Principles

1. **Air-Gapped & Offline**: License verification performs local cryptographic verification of Ed25519 digital signatures. No network phone-home, external validation APIs, or cloud beacons are required.
2. **Operational Grace Period**: A 14-day grace period ensures that accidental expiration of a license does not instantly disrupt active workflows. Operators receive actionable warnings on `stderr`.
3. **Fails-Open for Developers**: Single-user local CLI, TUI, and local `datatug serve` on localhost remain part of the Community edition and never require a commercial license.
