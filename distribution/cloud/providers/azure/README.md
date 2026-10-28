# Azure candidate

Checked 2026-10-25. Candidate: AKS Kubernetes Application, existing managed
cluster, namespace-scoped extension and Azure Disk private state. No cluster,
ACR, publisher entitlement, extension type, offer or CNAB exists from this work.
The shared Helm runtime is reused inside a provider package.

## Packaging and account gates

[Technical assets](https://learn.microsoft.com/en-us/partner-center/marketplace-offers/azure-container-technical-assets-kubernetes)
require Helm, manifest, ARM template and createUiDefinition inside CNAB. Images
must be parameterized under `global.azure.images`; tags or digests are supported.
Microsoft copies package images into its public ACR, so neither private-plan
visibility nor a private source ACR protects secrets/IP in the payload. No secret
value belongs in any image, UI field, configuration setting or manifest.

The generated image adapter uses
`global.azure.images.runner.registry`, `.image` and `.digest`. Registry path
prefixes are supported; schemes, authentication, whitespace and tag delimiters
are rejected. Digests require lowercase SHA-256; there is no tag fallback.
Real CPA digest ingestion/copying remains unverified. Resource quantities remain
typed chart defaults; the portal does not pass whole-number CPU strings through
Helm's scalar `--set` parser. The generated portal reference/egress fields have
the list topology chosen by the input configuration; direct extension overrides
still require common schema and operational review.

Use `container-package-app` verification only after selecting a reviewed pinned
tool digest and supplying the entitled publisher/ACR. `cpa buildbundle` also
uploads; it is not a local-only validation command and must remain gated by
publication authorization. No local source archive is called a CNAB.

Registry-copy authorization, tenant linkage and PartnerCenterIngestion
registration are seller prerequisites; this package performs no Entra/IAM write.
The [official sample](https://github.com/Azure-Samples/kubernetes-offer-samples/tree/main/samples/k8s-offer-azure-vote)
establishes package manifest/ARM/UI shape. This candidate targets existing AKS
only, uses namespace scope and excludes cluster creation, billing labels and
automatic minor upgrades. Approved plan/offer/publisher/extension identifiers
must come from Partner Center. Provider insertion variables remain intact.

The [extension API](https://learn.microsoft.com/en-us/azure/templates/microsoft.kubernetesconfiguration/2024-11-01/extensions)
supports pinned extension version with `autoUpgradeMinorVersion: false` and a
namespace target. Before submission, CPA and the portal sandbox must validate
the exact generated ARM/UI, string-to-Helm boolean conversion and namespace
flow; local shape checks do not evaluate Azure expressions.

[Azure Disk CSI](https://learn.microsoft.com/en-us/azure/aks/csi-storage-drivers)
provides single-node block disks. Verify the existing claim actually uses
`disk.csi.azure.com` with a supported Linux filesystem and UID/GID permissions.
Azure Files is excluded for private SQLite WAL state. The Runner ServiceAccount
gets no Azure permissions. Any connector-specific Workload Identity requires
separate operator configuration and compatibility evidence.

## Owner inputs and operations

Required inputs: entitled publisher and application name, approved package
version, ACR registry, actual image-copy/digest receipt, offer/plan/extension type,
existing AKS cluster in selected resource group, namespace/environment,
Coordinator URL, existing project/state claims, scoped token Secret and existing
ServiceAccount. Support/legal/privacy terms, commercial model, regional support,
security review and isolated-account spending approval remain unresolved.

The generated ARM deploys only a cluster extension to an existing AKS cluster;
it must not create a cluster, PVC, Secret, role assignment or public endpoint.
Follow the [shared acceptance protocol](../README.md), including negative scope,
CA/network/auth cases and retained private-state restart/backup. Portal
installation/upgrade/rollback must use approved extension package versions;
schema compatibility and a stopped-state backup precede rollback. Do not manage
the same installation concurrently with independent Helm and extension writers.

To tear down an approved live test, remove its namespace-scoped extension,
confirm Runner termination, revoke the scoped token and audit retained disks,
backups, Secrets and seller registry artifacts. Do not delete the existing AKS
cluster or namespace/shared resources as part of application uninstall.

Cost assumptions: AKS service tier, worker VMs, Azure Disk capacity/type,
snapshots, ACR, NAT/egress and logging. Region, topology, traffic and retention
are unknown; no numeric estimate or additional DataTug charge is approved.
