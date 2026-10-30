# Security Policy

The DataTug team takes security vulnerabilities seriously. We appreciate your efforts to responsibly disclose any findings.

---

## Supported Versions

Security updates and patches are actively provided for the following versions:

| Version | Supported |
| :--- | :---: |
| Latest release (`main`) | :white_check_mark: |
| Prior minor release (`v0.x`) | :white_check_mark: |
| Older releases | :x: |

---

## Reporting a Vulnerability

**Please do not report security vulnerabilities via public GitHub issues.**

If you believe you have discovered a security vulnerability in DataTug (including the CLI, local server, MCP bridge, or commercial engine):

1. **Email Details**: Send a detailed report to **`security@datatug.app`**.
2. **Report Contents**:
   - A clear description of the issue and potential security impact.
   - Exact steps or minimal reproducible example (PoC) demonstrating the vulnerability.
   - Affected platforms, architectures, or configurations.
   - Any proposed remediation or patches.

### Response Timeline
- **Initial Acknowledgment**: Within **48 hours** of receiving your report.
- **Triage & Assessment**: Within **5 business days** confirming vulnerability status and severity.
- **Remediation & Advisory**: We coordinate release timing with the reporter, publish fixes in a release patch, and credit the researcher in release notes and CVE advisories (unless anonymity is requested).

---

## Security Architecture Highlights

- **Loopback Enforcement**: `datatug serve` binds strictly to localhost/loopback interfaces by default with Origin and CORS request guards to prevent cross-site request forgery from untrusted browser tabs.
- **Air-Gapped Licensing**: Commercial license validation runs entirely offline using local Ed25519 cryptographic signatures without network telemetry or phone-home requests.
- **Principals and Scopes**: Read-only exploration fail-closes unauthenticated mutation endpoints.
