# Execution-plane deployment contract (candidate)

Contract: `datatug.execution-plane/v1`. This is locally validated preparation,
not a published image, marketplace offer or verified cloud installation.

One existing CLI image supplies the execution plane; full visuals stay at
https://datatug.app. The reusable Helm chart deploys only an outbound Runner:
no Service, ingress, public database listener, shell, source downloader, new
execution engine, billing agent or embedded web application. Use an existing
approved Coordinator over verified HTTPS. Use the same scope and endpoint in
the project manifest and deployment. Existing secured Check asset grants remain
unsupported; operator consent is separate from identity or project trust.

## Required operator inputs

- An approved immutable image repository/digest and explicit local-owner consent.
- Exactly one namespace/project/environment and a matching scoped Runner token
  in a pre-existing namespace Secret. No user/editor token goes to the Runner.
- The chart takes namespace and environment explicitly. It deliberately does
  not accept a second `scope.project` value: the Runner loads the project ID
  from the mounted project's manifest and rejects leased work whose project,
  namespace or environment differs. A duplicate chart-side project string could
  only be an unchecked label; it would not strengthen that runtime binding.
- A read-only project claim and a distinct private state claim, dedicated to
  this deployment, plus an existing least-privilege ServiceAccount. No IAM
  binding, storage, credential or seller account is created automatically.
- Node-local block-backed state with UID/GID 65532 write access and protected
  evidence directories. The caller must verify the CSI driver and filesystem:
  ReadWriteOnce alone does not prove block-storage semantics.

SQLite WAL is used by existing private evidence stores. Its shared-memory
requirements exclude network filesystems. Do not put that state on NFS, EFS,
Azure Files or Filestore. See [SQLite WAL requirements](https://www.sqlite.org/wal.html).
This is why EKS with an approved EBS-backed state claim is the current AWS
packaging candidate, sharing the chart with AKS/Azure Disk and GKE/Persistent
Disk. The earlier ECS/Fargate/EFS sketch is not a supported durable-state recipe;
it would require a separately validated persistence design. AWS remains first
in the candidate listing order; seller eligibility and customer opportunity
remain unverified.

## Runtime and operations

The chart pins one Linux amd64 pod, non-root UID/GID 65532, no Linux
capabilities, no privilege escalation, read-only root and project, private
writable state, bounded CPU/memory/tmp, and no automatically mounted Kubernetes
API token. Existing provider workload identity is operator-managed; the chart
does not grant cloud permissions. Supply only explicitly named source Secrets
(`dsnEnv` URL variables or PGPASSWORD) that the actual project uses.

Default network policy denies ingress and limits egress to DNS, the configured
Coordinator TCP port, and explicit database CIDR/port rules. Port access is not
a DNS-name allowlist; enforce stricter proxy/firewall policy if required. Verify
CNI enforcement, DNS selectors, NAT/egress, certificate trust and proxy settings
in an isolated account before release. Custom CA ConfigMaps contain only public
CA certificates; private TLS keys are not mounted into the Runner.

Pod Running is process status, not evidence of Coordinator authentication or
successful Checks. Observe scoped Coordinator registration/heartbeat, leased
completion and error/unknown outcomes with a separate authorised operator.
There is no invented Runner readiness endpoint. The local TLS Compose tests
exercise the same transport and scope; they are not cloud-account acceptance.

Use Recreate and one writer for private state. Rotate the scoped Secret, revoke
the old token at the Coordinator and perform a controlled Runner restart. Back
up private state with the Runner stopped and protect backup access. Upgrade by
approved image digest; preserve state and validate a real Check, then roll back
to a tested schema-compatible digest when needed. Uninstall removes chart
workloads/configuration, not operator-owned PVCs, Secrets or IAM bindings. Delete
retained data only after an explicit retention decision.

Offline projects/Checks remain available through the local CLI/image. An
outbound Runner needs its Coordinator; it does not silently upload query rows,
inputs, SQL or credentials. Existing result references remain private; the
hosted-home fallback is not the authorised-result release gate.

## Marketplace adapters and external gates

| Provider | Candidate delivery | Missing submission/account evidence |
| --- | --- | --- |
| AWS | EKS Helm + Marketplace-owned ECR image; operator-approved EBS state | Seller registration, repository transfer, exact image policy scan, tested launch instructions/account, support and listing approval |
| Azure | AKS Helm inside CNAB with manifest, ARM template and createUiDefinition | Partner/publisher entitlement, Azure image parameterisation, CPA verification, ACR copy permissions and actual AKS install |
| Google | GKE Helm + Application CR and deployer image/schema | Public deployment-package repository, service name, Producer Portal/partner eligibility, deployer verification and actual GKE install |

These adapters are requirements, not completed provider packages. Azure's CNAB
and Google's deployer are additional deployment artefacts even though runtime
execution uses one CLI image. Provider registry publication and commercial terms
require separate owner approval. No paid-plan requirement or additional DataTug
price is asserted; provider compute, disks, egress and logging are separate costs.
Size and price only after region, retention, workload and account inputs exist.

Primary requirements (checked 2026-10-25):
[AWS container policies](https://docs.aws.amazon.com/marketplace/latest/userguide/container-product-policies.html),
[Azure technical assets](https://learn.microsoft.com/en-us/partner-center/marketplace-offers/azure-container-technical-assets-kubernetes),
[Google packaging](https://docs.cloud.google.com/marketplace/docs/partners/kubernetes/create-app-package).

No cloud resource, seller submission, IAM change, recurring charge or registry
publication has been performed. Independent Astra cloud architecture/security
review and isolated real-account tests remain mandatory.
