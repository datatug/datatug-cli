#!/usr/bin/env python3
"""Validate bootstrap inputs before API access and serialize owned resources."""
import argparse
import json
import re
import sys
import uuid
import subprocess


def dns(value, maximum=63):
    if not isinstance(value, str) or len(value) > maximum or not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", value):
        raise ValueError("A bounded DNS label is required")
    return value


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate bootstrap input")
        result[key] = value
    return result


def preflight(parameters):
    values = json.loads(parameters, object_pairs_hook=unique)
    if not isinstance(values, dict):
        raise ValueError("Bootstrap parameters must be an object")
    name, namespace = dns(values["name"], 51), dns(values["namespace"])
    if values.get("scope.namespace") != namespace:
        raise ValueError("Provider and runtime namespace must match before bootstrap")
    runtime = dns(values["serviceAccount"])
    account = name + "-deployer-sa"
    if runtime == account:
        raise ValueError("Runtime and disposable deployer ServiceAccounts must differ")
    return name, namespace, account


def manifest(kind, name, namespace, app_name=None, app_uid=None, image_pull_secret=None):
    dns(name)
    dns(namespace)
    resource = {"apiVersion": "v1", "kind": kind,
                "metadata": {"name": name, "namespace": namespace}}
    if kind == "Application":
        resource["apiVersion"] = "app.k8s.io/v1beta1"
        resource["spec"] = {"selector": {"matchLabels": {"app.kubernetes.io/name": name}}, "assemblyPhase": "Pending"}
    elif kind == "ServiceAccount":
        dns(app_name, 51)
        if str(uuid.UUID(app_uid)) != app_uid:
            raise ValueError("An observed Application UID is required")
        resource["metadata"]["ownerReferences"] = [{"apiVersion": "app.k8s.io/v1beta1", "kind": "Application",
            "name": app_name, "uid": app_uid, "blockOwnerDeletion": False}]
        if image_pull_secret:
            dns(image_pull_secret)
            resource["imagePullSecrets"] = [{"name": image_pull_secret}]
    else:
        raise ValueError("Unexpected bootstrap resource kind")
    return resource


def create_account(name, namespace, app_name, app_uid, runtime_account, image_pull_secret=None, kubectl="kubectl", env=None):
    dns(app_name, 51)
    if name != app_name + "-deployer-sa" or dns(runtime_account) == name:
        raise ValueError("Disposable account must differ from the runtime identity")
    resource = manifest("ServiceAccount", name, namespace, app_name, app_uid, image_pull_secret)
    result = subprocess.run([kubectl, "create", "--namespace", namespace, "--filename=-", "-o", "json"],
                            input=json.dumps(resource), env=env, text=True, capture_output=True, check=False)
    if result.returncode:
        raise ValueError("Fresh creation failed; collision retries require explicit operator cleanup")
    created = json.loads(result.stdout)
    metadata = created.get("metadata", {})
    uid = metadata.get("uid", "")
    if metadata.get("name") != name or metadata.get("namespace") != namespace or str(uuid.UUID(uid)) != uid:
        raise ValueError("The create response did not identify the fresh account")
    return uid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("preflight", "application", "account", "create-account"))
    parser.add_argument("--name")
    parser.add_argument("--namespace")
    parser.add_argument("--app-name")
    parser.add_argument("--app-uid")
    parser.add_argument("--image-pull-secret")
    parser.add_argument("--runtime-account")
    parser.add_argument("--kubectl", default="kubectl")
    args = parser.parse_args()
    try:
        if args.operation == "preflight":
            preflight(sys.stdin.read())
        elif args.operation == "create-account":
            print(create_account(args.name, args.namespace, args.app_name, args.app_uid,
                                 args.runtime_account, args.image_pull_secret, args.kubectl))
        else:
            print(json.dumps(manifest("Application" if args.operation == "application" else "ServiceAccount",
                args.name, args.namespace, args.app_name, args.app_uid, args.image_pull_secret)))
    except (ValueError, KeyError, TypeError, OSError):
        print("Invalid bootstrap inputs; no resource may be adopted", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
