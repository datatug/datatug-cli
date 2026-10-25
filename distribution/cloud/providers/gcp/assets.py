"""Google schema preparation, independent of the open deployer-cleanup gate."""
import json
import shutil

from . import chart, patch_sdk


def schema(config):
    metadata = config["providerMetadata"]
    properties = {
        "name": {"type": "string", "maxLength": 51, "pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", "x-google-marketplace": {"type": "NAME"}},
        "namespace": {"type": "string", "maxLength": 63, "pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", "x-google-marketplace": {"type": "NAMESPACE"}},
        "image.digest": {"type": "string", "title": "Verified final runtime digest",
                         "description": "Verify the provider-copied runtime image digest; a source digest is not sufficient.",
                         "pattern": "^sha256:[a-f0-9]{64}$"},
        "trustedLocalOwner": {"type": "boolean", "default": False,
                              "title": "Explicitly permit trusted local-owner execution"},
        "scope.namespace": {"type": "string", "title": "Matching DataTug namespace"},
        "scope.environment": {"type": "string", "title": "DataTug environment"},
        "coordinator.endpoint": {"type": "string", "title": "Approved HTTPS Coordinator URL"},
        "coordinator.port": {"type": "integer", "minimum": 1, "maximum": 65535, "default": 443},
        "coordinator.caConfigMap": {"type": "string", "default": "", "title": "Existing public CA ConfigMap name"},
        "projectClaim": {"type": "string", "title": "Existing read-only project PVC name"},
        "stateClaim": {"type": "string", "title": "Existing private block state PVC name"},
        "stateFilesystem": {"type": "string", "enum": ["node-local-block"],
                            "title": "Attest operator-verified block filesystem"},
        "runnerSecret.name": {"type": "string", "title": "Existing scoped Runner Secret name"},
        "runnerSecret.key": {"type": "string", "default": "token", "title": "Existing Secret key name"},
        "serviceAccount": {"type": "string", "maxLength": 63, "pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", "title": "Existing runtime ServiceAccount name"},
        "resources.requests.cpu": {"type": "string", "title": "Runner requested CPU"},
        "resources.requests.memory": {"type": "string", "title": "Runner requested memory"},
        "resources.limits.cpu": {"type": "string", "title": "Runner CPU limit"},
        "resources.limits.memory": {"type": "string", "title": "Runner memory limit"},
        "networkPolicy.enabled": {"type": "boolean", "enum": [True], "title": "Required network policy"},
        "networkPolicy.dnsNamespace": {"type": "string", "title": "Approved DNS resolver namespace"},
        "marketplaceInputs.sourceCredentialsJson": {
            "type": "string", "default": "[]", "title": "Existing source Secret references as JSON",
            "description": "Only envName, secretName and key; no credential values. Maximum 50 references."},
        "marketplaceInputs.databaseEgressJson": {
            "type": "string", "default": "[]", "title": "Explicit database egress rules as JSON",
            "description": "Only IPv4 CIDR and port; maximum 50 rules. Example: [{\"cidr\":\"10.0.0.0/16\",\"port\":5432}]"},
    }
    verbs = ["get", "list", "watch", "create", "update", "patch", "delete"]
    return {
        "x-google-marketplace": {
            "schemaVersion": "v2", "applicationApiVersion": "v1beta1",
            "partnerId": metadata["partnerId"], "solutionId": metadata["solutionId"],
            "publishedVersion": config["packageVersion"],
            "publishedVersionMetadata": {"releaseNote": "Candidate outbound Check Runner package; not published"},
            "images": {"": {"properties": {"image.repository": {"type": "REPO_WITH_REGISTRY"}}}},
            "deployerServiceAccount": {
                "description": "Manage this namespace's generated Application and Runner resources. The separately authorized bootstrapper adds exact-name self-cleanup authority, guarded by observed UID.",
                "roles": [{"type": "Role", "rulesType": "CUSTOM", "rules": [
                    {"apiGroups": ["app.k8s.io"], "resources": ["applications"], "verbs": verbs},
                    {"apiGroups": ["apps"], "resources": ["deployments"], "verbs": verbs},
                    {"apiGroups": [""], "resources": ["configmaps"], "verbs": verbs},
                    {"apiGroups": ["networking.k8s.io"], "resources": ["networkpolicies"], "verbs": verbs},
                    {"apiGroups": [""], "resources": ["pods"], "verbs": ["get", "list", "watch"]},
                    {"apiGroups": ["apps"], "resources": ["replicasets"], "verbs": ["get", "list", "watch"]},
                ]}],
            },
        },
        "properties": properties,
        "required": [key for key in properties if key != "coordinator.caConfigMap"],
    }


def prepare_schema(path, config):
    # JSON is valid YAML; serializer preserves identities as data, never YAML code.
    path.write_text(json.dumps(schema(config), indent=2) + "\n", encoding="utf-8")


def prepare(bundle, config, source):
    chart.prepare(bundle / "chart", config)
    prepare_schema(bundle / "schema.yaml", config)
    patch_sdk.prepare(source, bundle / "sdk-patch")
    deployer = bundle / "deployer"
    deployer.mkdir()
    (deployer / "chart").mkdir()
    shutil.copytree(bundle / "chart", deployer / "chart" / "datatug-execution")
    shutil.copyfile(bundle / "schema.yaml", deployer / "schema.yaml")
    shutil.copytree(bundle / "sdk-patch", deployer / "sdk-patch")
    (deployer / "Dockerfile").write_text(
        "FROM " + config["providerMetadata"]["deployerBase"] + "\n"
        "COPY sdk-patch/print_config.py /bin/print_config.py\n"
        "COPY sdk-patch/datatug_provider_values.py /bin/datatug_provider_values.py\n"
        "COPY sdk-patch/datatug_bootstrap_inputs.py /bin/datatug_bootstrap_inputs.py\n"
        "COPY sdk-patch/expand_config.py sdk-patch/config_helper.py sdk-patch/schema_values_common.py /bin/\n"
        "COPY sdk-patch/set_ownership.py sdk-patch/resources.py sdk-patch/yaml_util.py sdk-patch/log_util.py /bin/\n"
        "COPY sdk-patch/datatug_cleanup.py /bin/datatug_cleanup.py\n"
        "COPY sdk-patch/clean_iam_resources.sh /bin/clean_iam_resources.sh\n", encoding="utf-8")
    bootstrap = bundle / "bootstrap"
    bootstrap.mkdir()
    shutil.copytree(bundle / "sdk-patch", bootstrap / "sdk-patch")
    (bootstrap / "Dockerfile").write_text(
        "FROM " + config["providerMetadata"]["bootstrapBase"] + "\n"
        "COPY sdk-patch/provision.py /bin/provision.py\n"
        "COPY sdk-patch/datatug_cleanup_bootstrap.py /bin/datatug_cleanup_bootstrap.py\n"
        "COPY sdk-patch/datatug_bootstrap_inputs.py /bin/datatug_bootstrap_inputs.py\n"
        "COPY sdk-patch/config_helper.py sdk-patch/schema_values_common.py /bin/\n"
        "COPY sdk-patch/set_ownership.py sdk-patch/set_app_labels.py sdk-patch/resources.py sdk-patch/yaml_util.py sdk-patch/log_util.py /bin/\n"
        "COPY sdk-patch/install /scripts/install\n", encoding="utf-8")
