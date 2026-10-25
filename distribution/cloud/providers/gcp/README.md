# Google Cloud candidate

Checked 2026-10-25. Candidate: GKE Standard x86 Linux nodes, Persistent Disk
private state, the common Helm Runner plus Application metadata and a separate
short-lived deployer image. No partner/product/service identity, registry image,
deployer build or GKE installation is verified.

## Packaging and account gates

[Kubernetes packaging](https://docs.cloud.google.com/marketplace/docs/partners/kubernetes/create-app-package)
requires a public package repository with LICENSE and user guide, substitutable
namespace/name/images, an Application CR, deployer image and integration tests.
Commercial non-BYOL usage reporting is a separate requirement; no billing agent,
entitlement or commercial model is added or assumed by this candidate.

The official [Helm deployer guide](https://github.com/GoogleCloudPlatform/marketplace-k8s-app-tools/blob/master/docs/building-deployer-helm.md)
uses `chart/`, `/data/schema.yaml` and the provider Helm deployer base image.
Pin that base by a reviewed digest before building. A local archive of these
inputs is a deployer source context, not a built or verified deployer.

The [deployer schema](https://github.com/GoogleCloudPlatform/marketplace-k8s-app-tools/blob/master/docs/schema.md)
defines schema version `v2`, Application `v1beta1`, release version and remapped
image properties. Nested Helm inputs use dotted keys. Provider image relocation
does not establish that a source digest remains valid: verify the final copied
image digest before installation. Do not silently fall back to a mutable tag.

The deployer needs namespace-scoped resource-management permissions to install
and remove Application, Deployment, ConfigMap and NetworkPolicy; the runtime
Runner needs none of those permissions. Declare custom namespace Role rules
instead of the deployer's default namespace cluster-admin. Exclude Secret/PVC
creation and CRD/ClusterRole/cluster-admin grants by the restricted deployer. The
separate authorized bootstrapper creates its configuration Secret, deployer
ServiceAccount, Role/RoleBinding and Job. The Application CRD must already
exist as an independently managed cluster prerequisite. Operator-owned claims,
Secrets and ServiceAccount are not Application-owned components and survive
uninstall. The Application selector must match the common instance label. The
official Helm deployer uses `helm template`, so it does not create a Helm release
Secret; no `HELM_DRIVER` override is needed.

The unmodified SDK deletes deployer ServiceAccounts by label. This candidate
contains an explicit, hash-checked patch over SDK revision
`e6008fba2e5e5da014ce43782c64481b775475fd`: the authorized bootstrapper passes its
observed account name/UID into the Job and creates a separate Role allowing only
`delete` of that exact account name. The deployer uses named DELETE with a UID
precondition. It has no ServiceAccount list/create/patch or collection-delete
authority. A replaced UID or cleanup denial fails installation; there is no
successful no-op or broader fallback. Generated `sdk.patch` and
`patch-hashes.json` record the bootstrap and deployer changes.

Bootstrap validates bounded DNS names, namespace agreement and distinct runtime/
deployer accounts before any API command. The release name is limited to 51
characters so its disposable account suffix fits Kubernetes' 63-character limit.
Application/account manifests use JSON serialization. Disposable accounts are
created atomically; their UID comes from the create response. Existing accounts
are never adopted or reapplied in the final manifest stream. A collision stops
the attempt. Retrying requires explicit operator inspection/cleanup of any
previous bootstrap-owned objects; never delete an operator account automatically.

The pinned ownership stage recognizes the SDK's deployer-RBAC label. After the
real `process -> set_app_labels -> set_ownership --noapp` pipeline, every Role and
RoleBinding has exactly one observed ServiceAccount owner, with no Application
co-owner that would retain RBAC after cleanup. Both build contexts include the
hash-checked ownership/support files rather than relying on an unverified base
image's implementation.

The custom bootstrap context and deployer context must be reviewed and built
from approved immutable provider-tool digests together. The local patched
bootstrap does not prove that Marketplace's own UI/bootstrap supplies the same
UID/env/Role contract. Marketplace compatibility remains an explicit external
gate even after local restricted-identity tests pass. Do not present the package
as console-install-ready until the publisher/provider validates that seam.

Google's scalar UI schema cannot directly construct the common arrays. Two
bounded JSON string fields accept only existing source Secret references and
explicit IPv4 CIDR/port egress rules. The deployer adapter rejects unknown fields,
duplicate keys/environment names, malformed types and more than 50 items, then
passes typed arrays through the unchanged common Helm schema and runtime guards.
It never evaluates a shell or accepts secret values. Added ConfigMap and
NetworkPolicy instance labels group only generated components; exact runtime
parity checks normalize those two metadata additions and nothing else.
The runtime repository is generated by the SDK's image projection; it is not a
caller input or required UI property. Provider-only name/namespace inputs are
validated and removed before the unchanged common Helm schema is evaluated.
Offline tests exercise actual schema inputs through `make_app_params_yaml`,
`expand_config`, patched `output_yaml` and real Helm, including resource/DNS
overrides and rejected generated-property spoofing.

[Persistent Disk](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/persistent-volumes)
is the block candidate; verify `pd.csi.storage.gke.io`, Linux filesystem,
zone affinity, access mode and UID/GID permissions. Filestore is excluded for
private SQLite WAL state. One Recreate writer remains mandatory. Workload
Identity for source connectors is separately operator configured/tested; the
runtime receives no Google IAM binding or Kubernetes API token.

## Owner inputs and operations

Supply partner ID/name, solution ID and approved service name, public package
URL, staging registry, immutable final runtime and deployer digests, matching
release version/track, supported cluster/region matrix and existing namespace
prerequisites. Required local configuration is the same namespace/environment,
HTTPS Coordinator, existing scoped Secret, existing account and distinct claims
as the [shared contract](../../contract.md). Source Credentials remain only
existing Secret references. No raw token/DSN enters the deployer schema.

Once approved credentials, test account and spending authority exist, run the
official `mpdev verify`/install workflow on a reviewed pinned tool/deployer image
and execute the [shared acceptance protocol](../README.md). Supply test values
with pre-created isolated claims and token Secret, never production credentials.
Process-ready/Application-ready status cannot replace authenticated heartbeat
and a real Check outcome. Record install, upgrade, schema-compatible rollback,
restart and deletion evidence. Application deletion must remove only generated
runtime resources; verify private data/Secret/account retention and then make an
explicit cleanup decision. The dedicated deployer has lifecycle/permissions
distinct from the long-lived Runner and must be audited separately.

Cost assumptions: GKE management/service tier, worker capacity, Persistent Disk,
snapshots, Artifact Registry, NAT/egress and logging. No numerical bill, paid
listing, BYOL entitlement, support SLA or additional DataTug charge is approved.
