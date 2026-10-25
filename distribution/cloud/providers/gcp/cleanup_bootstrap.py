"""Bootstrap-only creation of exact-name cleanup authority and Job inputs."""
import os
import uuid

if __package__:
    from .bootstrap_inputs import dns
else:
    from datatug_bootstrap_inputs import dns


def decorate(manifests, namespace, account, app_name, parameters):
    uid = os.environ.get("DATATUG_DEPLOYER_SA_UID", "")
    if str(uuid.UUID(uid)) != uid:
        raise ValueError("Bootstrap must supply the observed deployer account UID")
    jobs = [m for m in manifests if m.get("kind") == "Job"]
    if len(jobs) != 1:
        raise ValueError("Exactly one one-shot deployer Job required")
    pod = jobs[0]["spec"]["template"]["spec"]
    if pod["serviceAccountName"] != account:
        raise ValueError("Cleanup identity must match the actual deployer Job")
    dns(account)
    if dns(parameters.get("serviceAccount")) == account:
        raise ValueError("The runtime ServiceAccount cannot be a disposable deployer identity")
    container = pod["containers"][0]
    container.setdefault("env", []).extend([
        {"name": "DATATUG_DEPLOYER_SA_NAME", "value": account},
        {"name": "DATATUG_DEPLOYER_SA_UID", "value": uid},
    ])
    labels = {"app.kubernetes.io/component": "deployer-rbac.marketplace.cloud.google.com",
              "marketplace.cloud.google.com/deployer": "Dependent"}
    role_name = app_name + "-deployer-cleanup"
    manifests.extend([
        {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role",
         "metadata": {"name": role_name, "namespace": namespace, "labels": labels},
         "rules": [{"apiGroups": [""], "resources": ["serviceaccounts"],
                    "resourceNames": [account], "verbs": ["delete"]}]},
        {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
         "metadata": {"name": role_name, "namespace": namespace, "labels": labels},
         "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role_name},
         "subjects": [{"kind": "ServiceAccount", "name": account, "namespace": namespace}]},
    ])
    # Do not rely on the SDK's mismatched deployer/deployer-rbac label test to
    # establish ownership. Bootstrap already knows this object's observed UID.
    owner = {"apiVersion": "v1", "kind": "ServiceAccount", "name": account,
             "uid": uid, "blockOwnerDeletion": False}
    for resource in manifests:
        if resource.get("kind") in ("Role", "RoleBinding"):
            metadata = resource["metadata"]
            if metadata.get("namespace") != namespace or metadata.get("ownerReferences"):
                raise ValueError("Ambiguous bootstrap RBAC ownership")
            metadata["ownerReferences"] = [owner.copy()]
    # scripts/install created this fresh account atomically. Never apply/adopt
    # an account from the final provisioning stream a second time.
    accounts = [resource for resource in manifests if resource.get("kind") == "ServiceAccount"]
    if len(accounts) != 1 or accounts[0]["metadata"].get("name") != account or accounts[0]["metadata"].get("namespace") != namespace:
        raise ValueError("Unexpected account in the bootstrap provisioning stream")
    return [resource for resource in manifests if resource.get("kind") != "ServiceAccount"]
