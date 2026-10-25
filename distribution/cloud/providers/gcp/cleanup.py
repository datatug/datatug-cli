#!/usr/bin/env python3
"""Delete exactly the bootstrap-owned deployer account, guarded by its UID."""
import json
import os
import re
import subprocess
import sys
import uuid


def delete_owned_account(namespace, name, uid, kubectl="kubectl"):
    label = r"[a-z0-9]([-a-z0-9]*[a-z0-9])?"
    if not all(isinstance(v, str) and len(v) <= 63 and re.fullmatch(label, v) for v in (namespace, name)):
        raise ValueError("Explicit bootstrap namespace/account names required")
    if str(uuid.UUID(uid)) != uid:
        raise ValueError("Canonical bootstrap account UID required")
    options = {"apiVersion": "v1", "kind": "DeleteOptions",
               "preconditions": {"uid": uid}, "propagationPolicy": "Background"}
    # --raw with stdin sends DeleteOptions directly. No list/get/label delete,
    # shell evaluation, token argument, ignore-not-found or fallback permissions.
    result = subprocess.run([kubectl, "delete", "--raw",
                             f"/api/v1/namespaces/{namespace}/serviceaccounts/{name}", "-f", "-"],
                            input=json.dumps(options), text=True, capture_output=True, check=False)
    if result.returncode:
        raise ValueError("Named deployer cleanup denied or UID changed; installation must fail")


def main():
    try:
        delete_owned_account(os.environ["NAMESPACE"], os.environ["DATATUG_DEPLOYER_SA_NAME"],
                             os.environ["DATATUG_DEPLOYER_SA_UID"], os.environ.get("KUBECTL", "kubectl"))
    except (KeyError, ValueError, OSError):
        print("Owned deployer account cleanup failed; installation failed", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
