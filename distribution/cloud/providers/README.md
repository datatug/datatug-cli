# Cloud provider package candidates

These are offline preparation materials for the existing
[`datatug.execution-plane/v1` contract](../contract.md). No provider installation,
CNAB publication, registry upload, Marketplace submission or commercial approval
has occurred. Public order remains Action and Docker, then VS Code, then AWS,
Azure and Google Cloud. Account gates do not prevent independent preparation.

The common [Helm chart](../helm) remains the only runtime source. Provider
packaging must preserve its one outbound Runner, explicit owner consent,
HTTPS Coordinator, scoped existing Secret, read-only project, distinct private
block-backed state, single writer and no inbound Service. Image-location and
Marketplace metadata adapters are packaging concerns; they cannot add an engine,
browser, full application, credential collector or billing agent.

| Candidate | Runtime / storage | Provider-specific preparation | External gates |
| --- | --- | --- | --- |
| [AWS](aws/README.md) | EKS EC2 workers / EBS filesystem | Container image and Helm launch instructions | Seller/product eligibility, Marketplace ECR, regional image verification, security scan, isolated install |
| [Azure](azure/README.md) | AKS / Azure Disk filesystem | CNAB inputs: Helm, manifest, ARM and portal UI | Partner Center, ACR and ingestion permissions, CPA verification/build, portal/install acceptance |
| [Google Cloud](gcp/README.md) | GKE Standard / Persistent Disk filesystem | Helm Application CR, deployer image and schema | Partner/service identifiers, public package repository, final registry mapping, mpdev verification/install |

Container reuse is the smallest candidate: SaaS would add a hosted service and
billing/data obligations; a VM adds OS/image maintenance. Neither alternative
has been ruled ineligible. Seller entitlement, customer demand and commercial
terms remain unverified for all three providers. The provisional AWS-first
order has no new customer-evidence justification.

## Inputs and confidentiality

Only reference existing namespace Secrets; never enter token, password, DSN or
private key values into package files, values, portal fields or command arguments.
Source credentials stay in the explicitly configured Secret references. An
existing project claim is mounted read-only. The private evidence claim must use
an operator-verified block filesystem with UID/GID 65532 access. RWO is an access
mode, not proof of a compatible filesystem. SQLite WAL excludes NFS, EFS, Azure
Files and Filestore. The runtime chart provisions no disk, identity or credential;
provider installer objects have the separate authority described below.

The Runner uses an existing ServiceAccount and disables automatic Kubernetes API
token mounting. It needs no Kubernetes API or cloud IAM grant for the basic
runtime. Installation has separate privileges: ordinary Helm records release
state in Secrets; Azure's cluster-extension infrastructure manages its installer;
Google's bootstrap creates a deployment configuration Secret, deployer
ServiceAccount, Role/RoleBinding and Job before the restricted deployer runs.
These installer actions are not runtime permissions. They require an authorized
operator and must be included in account-level review and teardown receipts.
Source connectors that need cloud identity require separate
operator-configured workload identity and independently tested SDK/connector
support. Do not add broad IAM/RBAC or a token mount to make a test pass.

NetworkPolicy enforcement is a cluster prerequisite. Permit DNS to the approved
resolver, TCP to the Coordinator port and explicit database CIDR/port rules.
The common Coordinator port rule does not restrict destination hostname/IP;
use an external egress firewall/proxy for destination-level controls. Configure
NAT/private routing, approved proxy and public CA trust independently; a custom
CA ConfigMap contains public certificates only. No public inbound database path
is required. An offline project can run locally; an outbound Runner needs its
Coordinator and does not become ready merely because its pod is Running.

## Installation and acceptance protocol

These steps define work still to execute in an explicitly approved isolated
account; they are not an executed operational runbook.

1. Record region/zone, cluster/Kubernetes/CNI/CSI versions, image digest,
   package/source hashes, namespace, project manifest ID and environment. Verify
   x86 Linux workers and adequate cluster capacity. Confirm the two existing
   claims, existing scoped token Secret and existing ServiceAccount. Keep their
   contents out of receipts.
2. Render and inspect the candidate with operator-specific values. Verify the
   image is the approved provider-registry digest, storage is block-backed, and
   one replica uses Recreate. Verify ingress denial and required outbound routes.
3. Install only after account/spend approval. Observe authenticated scoped
   Coordinator heartbeat and a real named Check completion. Compare safe outcome
   status with the existing local CLI; business false and unknown are distinct
   from infrastructure/authentication failure.
4. Exercise denied token, wrong namespace/project/environment, expired token,
   Coordinator unavailable, blocked database route, wrong CA and revoked source
   credentials. No cross-scope work may run; no raw secrets, SQL or rows enter
   platform logs/listing metrics. Record failures without copying private output.
5. Terminate/restart the Runner and verify private evidence retention. Stop the
   Runner before an access-controlled state backup. Rotate the existing scoped
   token, revoke its predecessor and restart under operator control.
6. Upgrade to a reviewed image digest with a retained stopped-state backup; run a
   real Check. Roll back only to a tested schema-compatible prior digest/package.
   Failed evidence/schema compatibility requires restoration, not an assumed
   `helm rollback` success. Do not allow two writers during upgrade or rollback.
7. Remove the provider deployment/extension and verify Runner termination and
   scoped-token revocation. Operator-owned claims, Secrets, ServiceAccount,
   identity bindings and backups remain. Record an explicit retention/deletion
   decision for each; removing a Helm release must not delete private state.

Record commands, exit codes, provider resource IDs, Coordinator-safe outcome
receipts and residual-resource/cost inventory. A rendered manifest, successful
packaging command or process status cannot replace this acceptance evidence.

## Listing, support and licensing

Candidate title: **DataTug outbound Check Runner**. Candidate summary: Run existing
DataTug Checks inside an operator-controlled Kubernetes environment with an
approved HTTPS Coordinator. This is an execution component; it has limited
utility without a compatible Coordinator and a trusted project. The full visual
experience remains at DataTug.app; no UI assets are included. Do not place
cross-provider marketing or upsell copy into the AWS listing.

Package/runtime source license: Apache-2.0, with the repository LICENSE and
transitive notices retained in the actual runtime payload. The pinned Core MIT
notice and all binary/module licenses remain required. No AG Grid/Charts or Apps
assets may enter these packages. Provider-tool/deployer images add their own
license/security inventory; validate the final deployer separately from the
runtime. Hosted AG contractual interpretation remains a distinct owner/legal
gate; technical absence from a package does not resolve it.

Before submission the owner must supply the legal seller name, support and
security contacts/URLs, privacy policy, terms/EULA, approved pricing/licensing
model, release lifecycle and vulnerability-remediation policy. No support SLA,
free entitlement, paid offer, geographic eligibility or accepted terms is
asserted. Suggested support evidence: installation logs with redacted safe
identifiers, exact image/package versions and safe status envelopes; never ask
customers to send tokens, DSNs, SQL or result rows. Measure adoption only from
approved aggregate installations/versions or opt-in safe outcomes; no new
telemetry collector is added.

## Sizing and costs (assumptions dated 2026-10-25)

The chart requests 100m CPU and 256Mi memory, limits one CPU and 768Mi, and uses
a 64Mi memory-backed tmp volume. These are initial test bounds, not a throughput
promise or production capacity assessment. Start with one isolated project and
one Check, measure peak memory/CPU and private-evidence growth, then size from
retention and concurrency. Disk capacity, region and node count are required
operator inputs; the package does not select them.

Existing-cluster cost still includes incremental worker capacity, private-state
and project disks, snapshots/backups, registry storage/pulls, NAT/egress, logging,
control-plane/service tiers and any existing Coordinator service. New clusters
can add baseline control-plane/node costs. There is no calculated monthly bill:
region, availability tier, purchase model, retention and traffic are unknown.
Use current provider calculators with recorded assumptions before approval:
[AWS](https://calculator.aws/), [Azure](https://azure.microsoft.com/pricing/calculator/),
[Google Cloud](https://cloud.google.com/products/calculator).

Offline files are candidates. Passing local validation must be reported
separately from a built CNAB/deployer, actual provider installation, seller
acceptance and public listing.

## Offline entrypoints

Python 3 and PyYAML are required for parsing; Helm is supplied explicitly or via
`HELM`. No tool is installed, daemon started or network connection opened by the
packager. Run from this directory; output must be a new directory outside the
repository:

```sh
python3 package.py aws --config fixture.json --fixture --output /tmp/datatug-aws-candidate --helm /path/to/helm
python3 package.py azure --config fixture.json --fixture --output /tmp/datatug-azure-candidate --helm /path/to/helm
python3 package.py gcp --config fixture.json --fixture --output /tmp/datatug-gcp-candidate --helm /path/to/helm --google-tools-source /path/to/pinned-provider-sdk
python3 validate.py /tmp/datatug-aws-candidate --helm /path/to/helm
HELM=/path/to/helm python3 -m unittest discover -s . -p 'test_provider_assets.py' -v
```

`fixture.json` contains deliberately nonexistent images/identities. Explicit
`--fixture` is mandatory and the output keeps a NOT-FOR-SUBMISSION marker. For
operator preparation, supply real approved inputs in a private configuration
file and omit `--fixture`; this still does not prove registry ownership, image
pull/scan, seller entitlement or installation. Unknown chart fields and inline
credentials are rejected by the actual Helm schema before packaging. Generated
archives retain LICENSE, schema, source chart hashes and offline receipts.

For Google's offline schema validator, obtain the pinned public source revision
recorded in `sources.json`, inspect it and verify the wrapper's expected hashes:

```sh
python3 gcp/validate_schema.py /tmp/datatug-gcp-candidate/schema.yaml --source /path/to/marketplace-k8s-app-tools
```

This executes the provider's actual schema validator, not a substitute schema.
It does not execute `mpdev`, pull/build a deployer or validate an account. Azure
local checks validate artifact references, serialization and parameter flow;
CPA and portal expression evaluation remain separate external gates.

The Google source archive includes separate `bootstrap/` and `deployer/` build
contexts plus exact SDK patch hashes. Both base digests in the fixture are fake;
building/pulling requires independently reviewed real tool images. Do not push
either image as part of preparation. Exercise SDK/scalar mapping locally:

```sh
python3 gcp/verify_sdk.py --source /path/to/pinned-provider-sdk --helm /path/to/helm --output /tmp/gcp-sdk-receipt.json
```

The opt-in `gcp/test_cleanup_cluster.py` harness requires an already authorized
local cluster/private loopback kubeconfig and a fresh dedicated namespace. It
uses real temporary ServiceAccount tokens, never a permissive impersonation
shortcut, verifies exact-name/UID deletion and runtime-object retention, and
removes its namespace/token files. It does not create a cluster. Its results do
not prove Marketplace UI bootstrap, GKE integration, deployer image build or
Coordinator/Check acceptance.
