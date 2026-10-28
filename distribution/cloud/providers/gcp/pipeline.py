"""Exercise final pinned SDK functions and CLI ownership stages without API calls."""
import ast
import base64
import copy
import functools
import json
import os
import pathlib
import subprocess
import sys
import types

import yaml

from . import assets, bootstrap_inputs, cleanup_bootstrap, provider_values, validate_schema


def functions(path, namespace):
    tree = ast.parse(path.read_text(encoding="utf-8"))
    nodes = [node for node in tree.body if isinstance(node, (ast.FunctionDef, ast.ClassDef))]
    for node in tree.body:
        if isinstance(node, ast.Assign) and all(isinstance(target, ast.Name) and target.id.startswith("_") for target in node.targets):
            nodes.append(node)
    exec(compile(ast.Module(body=nodes, type_ignores=[]), str(path), "exec"), namespace)
    return namespace


@functools.lru_cache(maxsize=8)
def official_helper(filename, source):
    official = types.ModuleType("datatug_pinned_config_helper")
    official.__file__ = filename
    exec(compile(source, filename, "exec"), official.__dict__)
    return official


def load(patched, config):
    path = patched / "config_helper.py"
    official = official_helper(str(path), path.read_text(encoding="utf-8"))
    schema = official.Schema(assets.schema(config))
    schema.validate()
    base = {"yaml": yaml, "config_helper": official, "json": json, "os": os, "base64": base64}
    provisioning = functions(patched / "provision.py", {**base, "datatug_cleanup_bootstrap": cleanup_bootstrap})
    expansion = functions(patched / "expand_config.py", base.copy())
    printing = functions(patched / "print_config.py", {**base, "datatug_provider_values": provider_values})
    return schema, provisioning, expansion, printing


def schema_inputs(config, namespace=None):
    """Construct actual declared provider inputs; generated image repo is omitted."""
    values = config["values"]
    flattened = {}

    def leaves(value, prefix=""):
        if isinstance(value, dict):
            for key, item in value.items():
                leaves(item, f"{prefix}.{key}" if prefix else key)
        elif not isinstance(value, list):
            flattened[prefix] = value

    leaves(values)
    properties = assets.schema(config)["properties"]
    result = {key: flattened[key] for key in properties if key in flattened}
    result.update(name=config["releaseName"], namespace=namespace or values["scope"]["namespace"])
    result["scope.namespace"] = result["namespace"]
    result["marketplaceInputs.sourceCredentialsJson"] = json.dumps(values["sourceCredentials"])
    result["marketplaceInputs.databaseEgressJson"] = json.dumps(values["networkPolicy"]["databaseEgress"])
    bootstrap_inputs.preflight(json.dumps(result))
    return result


def values(patched, config, inputs, deployer_image):
    schema, provisioning, expansion, printing = load(patched, config)
    # The exact SDK serializer injects __image_repo_prefix__; expand generates
    # image.repository, then patched output_yaml removes provider-only fields.
    serialized = provisioning["make_app_params_yaml"](copy.deepcopy(inputs), deployer_image)
    expanded = expansion["expand"](yaml.safe_load(serialized), schema)
    return yaml.safe_load(printing["output_yaml"](expanded))


def final_manifests(patched, config, inputs, account, account_uid, app_uid, deployer_image):
    schema, provisioning, _, _ = load(patched, config)
    bootstrap_inputs.preflight(json.dumps(inputs))
    prior = os.environ.get("DATATUG_DEPLOYER_SA_UID")
    try:
        os.environ["DATATUG_DEPLOYER_SA_UID"] = account_uid
        manifests = provisioning["process"](schema, copy.deepcopy(inputs), deployer_image,
                     None, None, None, account, None)
    finally:
        if prior is None:
            os.environ.pop("DATATUG_DEPLOYER_SA_UID", None)
        else:
            os.environ["DATATUG_DEPLOYER_SA_UID"] = prior

    def cli(filename, data, arguments):
        result = subprocess.run([sys.executable, str(patched / filename), *arguments],
            input=yaml.safe_dump_all(data), text=True, capture_output=True, check=False)
        if result.returncode:
            raise ValueError("Pinned SDK ownership pipeline failed")
        return list(yaml.safe_load_all(result.stdout))

    manifests = cli("set_app_labels.py", manifests,
                    ["--manifests=-", "--dest=-", "--name=" + inputs["name"], "--namespace=" + inputs["namespace"]])
    manifests = cli("set_ownership.py", manifests,
                    ["--manifests=-", "--dest=-", "--noapp", "--app_name=" + inputs["name"],
                     "--app_uid=" + app_uid, "--app_api_version=app.k8s.io/v1beta1",
                     "--deployer_name=" + account, "--deployer_uid=" + account_uid])
    expected_owner = [{"apiVersion": "v1", "kind": "ServiceAccount", "name": account,
                       "uid": account_uid, "blockOwnerDeletion": True}]
    rbac = [resource for resource in manifests if resource["kind"] in ("Role", "RoleBinding")]
    if len(rbac) != 4 or any(resource["metadata"].get("ownerReferences") != expected_owner for resource in rbac):
        raise ValueError("Final RBAC must have exactly one observed ServiceAccount owner")
    if any(resource["kind"] == "ServiceAccount" for resource in manifests):
        raise ValueError("A fresh account must never be applied/adopted in the final stream")
    return manifests
