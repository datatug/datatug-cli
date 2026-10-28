#!/usr/bin/env python3
"""Offline render/security/parity checks. No provider or Kubernetes API calls."""
import argparse
import hashlib
import json
import os
import pathlib
import subprocess
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parent
COMMON = ROOT.parent / "helm"


def run(command):
    result = subprocess.run(command, text=True, capture_output=True, check=False)
    if result.returncode:
        # Diagnostics can contain invalid operator input; never echo them.
        raise ValueError(f"{pathlib.Path(command[0]).name} {command[1]} failed (exit {result.returncode})")
    return result.stdout


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"))


def docs(rendered):
    return [item for item in yaml.safe_load_all(rendered) if item]


def runtime(rendered, google_metadata=False):
    resources = docs(rendered)
    applications = sum(r["kind"] == "Application" for r in resources)
    if applications != (1 if google_metadata else 0):
        raise ValueError("Exactly one Google Application required" if google_metadata
                         else "Application metadata is only permitted for Google")
    if google_metadata:
        instance = next(r for r in resources if r["kind"] == "Deployment")["metadata"]["labels"]["app.kubernetes.io/instance"]
        for resource in resources:
            if resource["kind"] in ("ConfigMap", "NetworkPolicy"):
                labels = resource["metadata"].get("labels", {})
                if labels.get("app.kubernetes.io/instance") != instance:
                    raise ValueError("Google metadata label differs from the release instance")
                del labels["app.kubernetes.io/instance"]
                if not labels:
                    resource["metadata"].pop("labels", None)
    return sorted((r for r in resources if r["kind"] != "Application"),
                  key=lambda r: (r["kind"], r["metadata"]["name"]))


def check_runtime(rendered, namespace):
    resources = docs(rendered)
    allowed = {"Deployment", "ConfigMap", "NetworkPolicy", "Application"}
    if any(r["kind"] not in allowed or r["metadata"]["namespace"] != namespace for r in resources):
        raise ValueError("Unexpected resource kind or namespace")
    deployments = [r for r in resources if r["kind"] == "Deployment"]
    if len(deployments) != 1:
        raise ValueError("Exactly one Runner Deployment required")
    deployment = deployments[0]
    if deployment["spec"]["replicas"] != 1 or deployment["spec"]["strategy"]["type"] != "Recreate":
        raise ValueError("Private state requires one Recreate writer")
    pod = deployment["spec"]["template"]["spec"]
    sec = pod["securityContext"]
    if pod.get("automountServiceAccountToken") is not False or sec.get("runAsUser") != 65532 or sec.get("runAsNonRoot") is not True:
        raise ValueError("Runtime identity contract changed")
    if pod.get("initContainers") or len(pod["containers"]) != 1:
        raise ValueError("Only one existing Runner container allowed")
    container = pod["containers"][0]
    sec = container["securityContext"]
    if sec.get("readOnlyRootFilesystem") is not True or sec.get("allowPrivilegeEscalation") is not False or sec.get("capabilities", {}).get("drop") != ["ALL"]:
        raise ValueError("Container restrictions changed")
    if "@sha256:" not in container["image"]:
        raise ValueError("Immutable runtime digest required")
    mounts = {m["name"]: m for m in container["volumeMounts"]}
    if mounts["project"].get("readOnly") is not True or mounts["state"].get("readOnly") is not False:
        raise ValueError("Read-only project and private writable state required")
    policy = [r for r in resources if r["kind"] == "NetworkPolicy"]
    if len(policy) != 1 or policy[0]["spec"]["ingress"] != []:
        raise ValueError("Explicit ingress denial required")


def render(chart, values, release, namespace, helm):
    return run([helm, "template", release, str(chart), "--namespace", namespace, "--values", str(values)])


def validate(bundle, helm):
    marker = read_json(bundle / "CANDIDATE-NOT-FOR-SUBMISSION.json")
    common = read_json(bundle / "common-values.json")
    release, namespace = marker["releaseName"], common["scope"]["namespace"]
    actual = render(bundle / "chart", bundle / "operator-values.json", release, namespace, helm)
    expected = render(COMMON, bundle / "common-values.json", release, namespace, helm)
    if runtime(expected) != runtime(actual, google_metadata=marker["provider"] == "gcp"):
        raise ValueError("Provider changed the common runtime")
    check_runtime(actual, namespace)
    if marker["provider"] == "azure":
        arm, ui = read_json(bundle / "mainTemplate.json"), read_json(bundle / "createUIDefinition.json")
        if set(arm["parameters"]) != set(ui["parameters"]["outputs"]):
            raise ValueError("Azure UI/ARM parameters differ")
        if len(arm["resources"]) != 1 or arm["resources"][0]["type"] != "Microsoft.KubernetesConfiguration/extensions":
            raise ValueError("Azure may create only an extension on an existing cluster")
        properties = arm["resources"][0]["properties"]
        if properties["autoUpgradeMinorVersion"] is not False or properties["configurationProtectedSettings"] != {}:
            raise ValueError("Unexpected Azure upgrade or secret-value behavior")
    if marker["provider"] == "gcp":
        resources = docs(actual)
        application = next(r for r in resources if r["kind"] == "Application")
        instance = {"app.kubernetes.io/instance": release}
        if application["spec"]["selector"]["matchLabels"] != instance:
            raise ValueError("Google Application selector differs from the release")
        if any(r["metadata"].get("labels", {}).get("app.kubernetes.io/instance") != release for r in resources):
            raise ValueError("Google components are not grouped by instance")
        if {c["kind"] for c in application["spec"]["componentKinds"]} != {"Deployment", "ConfigMap", "NetworkPolicy"}:
            raise ValueError("Google Application must not own existing private resources")
    run([helm, "lint", str(bundle / "chart"), "--namespace", namespace, "--values", str(bundle / "operator-values.json")])
    (bundle / "rendered.yaml").write_text(actual, encoding="utf-8")
    return {"provider": marker["provider"], "validation": "offline-only", "runtimeParity": True,
            "helmLint": True, "securityInvariants": True,
            "renderSha256": hashlib.sha256(actual.encode()).hexdigest(),
            "helmVersion": run([helm, "version", "--short"]).strip(),
            "providerInstall": "not-run", "sellerSubmission": "not-run"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=pathlib.Path)
    parser.add_argument("--helm", default=os.environ.get("HELM", "helm"))
    args = parser.parse_args()
    try:
        print(json.dumps(validate(args.bundle.resolve(), args.helm), indent=2))
    except (ValueError, KeyError, OSError, yaml.YAMLError) as error:
        print(f"Offline validation refused: {type(error).__name__}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
