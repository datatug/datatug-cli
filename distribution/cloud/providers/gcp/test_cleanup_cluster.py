#!/usr/bin/env python3
"""Explicitly authorized local-cluster RBAC/UID tests; never creates a cluster."""
import argparse
import copy
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import urllib.parse

PROVIDERS = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROVIDERS))
import validate
from gcp import bootstrap_inputs, patch_sdk, pipeline



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True, type=pathlib.Path)
    parser.add_argument("--kubectl", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--google-tools-source", required=True, type=pathlib.Path)
    args = parser.parse_args()
    if not args.namespace.startswith("datatug-provider-"):
        raise ValueError("A dedicated datatug-provider-* test namespace is required")
    admin_env = {**os.environ, "KUBECONFIG": str(args.kubeconfig.resolve())}

    def command(argv, *, env=admin_env, data=None, check=True):
        result = subprocess.run([args.kubectl, *argv], input=data, env=env,
                                text=True, capture_output=True, check=False)
        if check and result.returncode:
            raise ValueError("Local-cluster test command failed; private diagnostics withheld")
        return result

    config = json.loads(command(["config", "view", "--minify", "--raw", "-o", "json"]).stdout)
    cluster = config["clusters"][0]
    if urllib.parse.urlparse(cluster["cluster"]["server"]).hostname not in ("127.0.0.1", "localhost", "::1"):
        raise ValueError("This harness only accepts a private loopback local-cluster kubeconfig")
    if any("exec" in user["user"] or "auth-provider" in user["user"] for user in config.get("users", [])):
        raise ValueError("Local test kubeconfig must not invoke an external authentication provider")
    if command(["get", "namespace", args.namespace], check=False).returncode == 0:
        raise ValueError("Refusing a pre-existing namespace")
    command(["create", "namespace", args.namespace])
    created = True
    ns = ["--namespace", args.namespace]
    checks = []

    def apply(resource):
        command(["apply", *ns, "-f", "-"], data=json.dumps(resource))

    def account(name):
        apply({"apiVersion": "v1", "kind": "ServiceAccount", "metadata": {"name": name, "namespace": args.namespace}})
        return uid("serviceaccount", name)

    def uid(kind, name):
        return command(["get", kind, name, *ns, "-o", "jsonpath={.metadata.uid}"]).stdout

    def role(actor, target):
        name = actor + "-cleanup"
        metadata = {"name": name, "namespace": args.namespace,
                    "ownerReferences": [{"apiVersion": "v1", "kind": "ServiceAccount", "name": actor,
                                         "uid": uid("serviceaccount", actor), "blockOwnerDeletion": False}]}
        apply({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
               "metadata": metadata,
               "rules": [{"apiGroups": [""], "resources": ["serviceaccounts"],
                          "resourceNames": [target], "verbs": ["delete"]}]})
        apply({"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
               "metadata": metadata,
               "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name},
               "subjects": [{"kind": "ServiceAccount", "name": actor, "namespace": args.namespace}]})

    try:
        sentinel_sa = account("runner-existing")
        apply({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "runner-credentials", "namespace": args.namespace},
               "type": "Opaque"})
        apply({"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "private-state", "namespace": args.namespace},
               "spec": {"storageClassName": "", "accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "1Mi"}}}})
        sentinels = [("serviceaccount", "runner-existing", sentinel_sa),
                     ("secret", "runner-credentials", uid("secret", "runner-credentials")),
                     ("pvc", "private-state", uid("pvc", "private-state"))]
        with tempfile.TemporaryDirectory(prefix="datatug-provider-tokens-") as temporary:
            patched = pathlib.Path(temporary) / "patched-sdk"
            patch_sdk.prepare(args.google_tools_source, patched)
            fixture = validate.read_json(PROVIDERS / "fixture.json")
            fixture["releaseName"] = "cleanup-self"
            inputs = pipeline.schema_inputs(fixture, namespace=args.namespace)
            inputs["serviceAccount"] = "runner-existing"
            deployer_image = fixture["values"]["image"]["repository"] + "/deployer@" + fixture["values"]["image"]["digest"]
            def identity(name):
                token = command(["create", "token", name, *ns, "--duration=10m"]).stdout.strip()
                private = {"apiVersion": "v1", "kind": "Config", "clusters": [copy.deepcopy(cluster)],
                           "users": [{"name": "restricted", "user": {"token": token}}],
                           "contexts": [{"name": "restricted", "context": {"cluster": cluster["name"],
                                         "user": "restricted", "namespace": args.namespace}}], "current-context": "restricted"}
                path = pathlib.Path(temporary) / (name + ".json")
                with os.fdopen(os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600), "w") as stream:
                    json.dump(private, stream)
                return {**os.environ, "KUBECONFIG": str(path)}

            def cleanup(env, name, original_uid):
                env = {**env, "NAMESPACE": args.namespace, "DATATUG_DEPLOYER_SA_NAME": name,
                       "DATATUG_DEPLOYER_SA_UID": original_uid, "KUBECTL": args.kubectl}
                return subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("cleanup.py"))],
                                      env=env, text=True, capture_output=True, check=False).returncode

            self_name = inputs["name"] + "-deployer-sa"
            self_uid = account(self_name)
            final = pipeline.final_manifests(patched, fixture, inputs, self_name, self_uid,
                "22222222-2222-4222-8222-222222222222", deployer_image)
            final_rbac = [resource for resource in final if resource["kind"] in ("Role", "RoleBinding")]
            for resource in final_rbac:
                apply(resource)
            self_identity = identity(self_name)
            other_uid = account("other-deployer")
            if cleanup(self_identity, "other-deployer", other_uid) == 0 or uid("serviceaccount", "other-deployer") != other_uid:
                raise ValueError("Production pipeline allowed deletion of another identity")
            for verb in ("list", "create", "patch"):
                result = command(["auth", "can-i", verb, "serviceaccounts", *ns], env=self_identity, check=False)
                if result.stdout.strip() != "no":
                    raise ValueError("Production pipeline granted excess identity permissions")
            checks.append("final pipeline RBAC denies other-account deletion and ServiceAccount list/create/patch")
            if cleanup(self_identity, self_name, self_uid) != 0:
                raise ValueError("Exact-name self cleanup failed")
            if command(["get", "serviceaccount", self_name, *ns], check=False).returncode == 0:
                raise ValueError("Self cleanup did not delete its account")
            checks.append("exact-name self deletion succeeded with a real scoped ServiceAccount token")
            for resource in final_rbac:
                command(["wait", "--for=delete", resource["kind"].lower() + "/" + resource["metadata"]["name"], *ns, "--timeout=15s"])
            checks.append("all four actual final-pipeline Roles/RoleBindings garbage-collected after self deletion")

            previous = account("preexisting-deployer-sa")
            try:
                bootstrap_inputs.create_account("preexisting-deployer-sa", args.namespace, "preexisting",
                    "22222222-2222-4222-8222-222222222222", "runner-existing", kubectl=args.kubectl, env=admin_env)
            except ValueError:
                pass
            else:
                raise ValueError("Create-only bootstrap adopted a pre-existing account")
            if uid("serviceaccount", "preexisting-deployer-sa") != previous:
                raise ValueError("A pre-existing operator account changed")
            checks.append("production create-only bootstrap rejects account collision and preserves its UID")
            collision = account("runtime-collision-deployer-sa")
            try:
                bootstrap_inputs.create_account("runtime-collision-deployer-sa", args.namespace, "runtime-collision",
                    "22222222-2222-4222-8222-222222222222", "runtime-collision-deployer-sa", kubectl=args.kubectl, env=admin_env)
            except ValueError:
                pass
            else:
                raise ValueError("Runtime/deployer account equality was accepted")
            if uid("serviceaccount", "runtime-collision-deployer-sa") != collision:
                raise ValueError("Runtime identity collision changed the account")
            checks.append("runtime/deployer identity equality fails closed and preserves the runtime account UID")

            account("cleanup-actor")
            old_uid = account("deployer-replaced")
            role("cleanup-actor", "deployer-replaced")
            actor = identity("cleanup-actor")
            other_uid = uid("serviceaccount", "other-deployer")
            if cleanup(actor, "other-deployer", other_uid) == 0 or uid("serviceaccount", "other-deployer") != other_uid:
                raise ValueError("Unrelated account was not protected")
            checks.append("unrelated ServiceAccount deletion denied and object retained")
            for verb in ("list", "create", "patch"):
                result = command(["auth", "can-i", verb, "serviceaccounts", *ns], env=actor, check=False)
                if result.stdout.strip() != "no":
                    raise ValueError("Unexpected identity permission")
            checks.append("ServiceAccount list/create/patch denied")
            command(["delete", "serviceaccount", "deployer-replaced", *ns, "--wait=true"])
            new_uid = account("deployer-replaced")
            if old_uid == new_uid or cleanup(actor, "deployer-replaced", old_uid) == 0 or uid("serviceaccount", "deployer-replaced") != new_uid:
                raise ValueError("Replacement UID was not protected")
            checks.append("stale UID cleanup failed and replacement account retained")
            if cleanup(actor, "deployer-replaced", new_uid) != 0:
                raise ValueError("Named account deletion with current UID failed")
            checks.append("exact-name current-UID deletion succeeded")
            denied_uid = account("cleanup-denied")
            if cleanup(identity("cleanup-denied"), "cleanup-denied", denied_uid) == 0 or uid("serviceaccount", "cleanup-denied") != denied_uid:
                raise ValueError("Cleanup denial was ignored")
            checks.append("cleanup denial propagated nonzero and installation cannot claim success")
            if any(uid(kind, name) != expected for kind, name, expected in sentinels):
                raise ValueError("Operator-owned runtime resources changed")
            checks.append("Runner ServiceAccount, credential Secret and private PVC retained with original UIDs")
    finally:
        if created:
            command(["delete", "namespace", args.namespace, "--wait=true", "--timeout=45s"])
    receipt = {"scope": "actual local restricted-identity tests; not Marketplace UI/GKE acceptance",
               "checks": checks, "passed": True, "testNamespaceDeleted": True,
               "cloudApiCalls": False, "clusterCreatedByHarness": False}
    args.output.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(receipt, indent=2))


if __name__ == "__main__":
    raise SystemExit(main())
