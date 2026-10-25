# AWS candidate

Checked 2026-10-25. Candidate format: a Linux container product for an existing
EKS cluster with EC2 x86 workers and EBS-backed private state. Standard Helm
commands launch the common Runner. This is not an EKS add-on or an ECS/Fargate
state recipe. There is no listing, Marketplace ECR repository or approved
deployment account.

## Current provider requirements

[Container requirements](https://docs.aws.amazon.com/marketplace/latest/userguide/container-product-policies.html)
require Marketplace-owned ECR images, least privilege, disclosed dependencies
and production functioning. Prerelease listings are excluded. Commercial
community/developer/BYOL editions carry a paid-equivalent-within-90-days rule;
the owner's commercial decision and eligibility remain open. This package does
not implement metering or License Manager.

[Delivery options](https://docs.aws.amazon.com/marketplace/latest/userguide/container-add-version.html)
permit container-image delivery with usage instructions and a deployment-template
URL, or a separate Helm fulfillment option. Image delivery with the common chart
as the deployment template avoids maintaining another runtime chart. Provider
format approval remains pending. The stricter Helm fulfillment image/tag
validator must be resolved before claiming Helm-offer compliance.

[Quick Launch ended 2026-03-01](https://docs.aws.amazon.com/marketplace/latest/userguide/container-based-products.html);
use explicit standard Helm instructions, not a Quick Launch claim.
[Seller eligibility](https://docs.aws.amazon.com/marketplace/latest/userguide/seller-eligibility.html)
and a defined support organization are prerequisites, with additional paid/BYOL
tax/banking/jurisdiction requirements. Their state has not been queried.

[EBS CSI](https://docs.aws.amazon.com/eks/latest/userguide/ebs-csi.html) cannot mount
EBS onto Fargate pods. Use approved EC2-backed workers and verify filesystem,
zone affinity, CSI identity, permissions and encryption. EBS CSI permissions
belong to the existing storage infrastructure; the Runner gets no IAM role.
Source-specific IAM/IRSA support is a separate operator configuration/test.

## Seller and installation inputs

Supply seller/product identity, approved offer/licensing/support/security text,
Marketplace-owned ECR repository and approved regional digest, image scan and
redistribution notices, immutable public package URL, supported region/Kubernetes
matrix and authorized isolated test account. No account IDs or product IDs in
examples prove a provider resource exists. No registration, repository creation,
push, subscription or terms acceptance is performed by local packaging.

After approval, follow the [shared acceptance protocol](../README.md). Populate
the generated chart's values file with approved Marketplace `image.repository`
and `image.digest`, `trustedLocalOwner: true`, matching namespace/environment,
HTTPS Coordinator, two existing claims, `stateFilesystem: node-local-block`,
existing Runner Secret and ServiceAccount, and explicit source/network rules.
Regionalization requires independently verifying the copied image digest in
each selected region; never derive or guess a Marketplace ECR account ID.

Example operator commands (not executed against AWS):

```sh
helm template runner ./chart --namespace datatug --values operator-values.json
helm upgrade --install runner ./chart --namespace datatug --values operator-values.json
helm history runner --namespace datatug
helm rollback runner PREVIOUS_REVISION --namespace datatug
helm uninstall runner --namespace datatug
```

Only execute rollback after the shared stopped-state/schema-compatibility gate.
Values contain Secret names, never credentials. Observe authenticated scoped
Coordinator/Check evidence; no synthetic readiness URL exists. Uninstall retains
operator-owned disks/Secrets/identity and requires a residual-cost inventory.

Cost assumptions: EKS service tier/control plane, EC2 capacity, EBS type/capacity,
snapshots, ECR, NAT, egress and logging; no numeric quote or additional DataTug
charge is approved. Region/availability/customer-demand evidence remains absent.
