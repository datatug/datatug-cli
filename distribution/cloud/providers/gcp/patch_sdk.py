"""Hash-checked changes to the pinned provider SDK; no silent cleanup bypass."""
import difflib
import hashlib
import json
import pathlib
import shutil

ROOT = pathlib.Path(__file__).resolve().parent
ORIGINALS = {
    "scripts/install": "e23f605a573223dab7f2fa49690ebe681a9866425be248d359e3b4835930268a",
    "marketplace/deployer_util/provision.py": "f2e7ec1b6a84f471ae15211b64bee5e8a983b030caad670bdf6b281741d87b08",
    "marketplace/deployer_util/print_config.py": "de4a8372902fc885927da06b27254db6ba73a0a8a3ca60c9f43bee7fc7b30dcf",
    "marketplace/deployer_util/expand_config.py": "79d8ccae1aa88e8cfeb6e5f22263c24a7bb60b3a66aaefe8c0d9946b5d27dd82",
    "marketplace/deployer_util/config_helper.py": "b41208ccdf1434ad2e858f0f258d5c712fbbdf7d20f63244188658160e518f61",
    "marketplace/deployer_util/schema_values_common.py": "48f1f749956ecf94887b217db324d56b911d59ff9ab690bfccd866b8364c8317",
    "marketplace/deployer_util/set_ownership.py": "fae0e7cf6ac3cd72ab4c246bd152f2f2bed9dd46f74188118e7da25172cacd29",
    "marketplace/deployer_util/set_app_labels.py": "f2e82040f57e85551cbde18f5a42a73bb51ff0a083b5bed51aaf0e5e772875e8",
    "marketplace/deployer_util/resources.py": "0c6a784a95c80e598436d8a4fa6dc4e0207e5cefc79d16f99c1806c536a5a5bc",
    "marketplace/deployer_util/yaml_util.py": "d492d3194682080d69d709eea5583e2b0ee6f97ce0bb0a17cdad66090e5e8166",
    "marketplace/deployer_util/log_util.py": "ce7a5827957b18088e7d7de7075d2ba4778bd2489b49338aa16b23c8008e8488",
}


def once(text, old, new):
    if text.count(old) != 1:
        raise ValueError("Pinned SDK patch anchor drifted")
    return text.replace(old, new)


def prepare(source, destination):
    destination.mkdir()
    records, patches = {}, []
    for relative, expected in ORIGINALS.items():
        original = (source / relative).read_text(encoding="utf-8")
        if hashlib.sha256(original.encode()).hexdigest() != expected:
            raise ValueError("Pinned SDK input hash mismatch")
        if relative == "scripts/install":
            changed = once(original, "  | provision.py \\", "  | DATATUG_DEPLOYER_SA_UID=\"${deployer_uid}\" provision.py \\")
            changed = once(changed, '[[ -z "$entrypoint" ]]',
                'printf \'%s\' "$parameters" | datatug_bootstrap_inputs.py preflight\n\n[[ -z "$entrypoint" ]]')
            app_start = changed.index('# Create Application instance.\n')
            app_end = changed.index('\napp_uid=', app_start)
            changed = changed[:app_start] + (
                '# Create Application using validated names and JSON serialization.\n'
                'datatug_bootstrap_inputs.py application --name "$name" --namespace "$namespace" \\\n'
                '  | kubectl apply --namespace="$namespace" --filename=-\n') + changed[app_end:]
            sa_start = changed.index('# Create ServiceAccount instance.\n')
            sa_end = changed.index('\n# Provisions external resource dependencies', sa_start)
            changed = changed[:sa_start] + (
                '# Create-only: a collision is an explicit failed attempt, never adoption.\n'
                'deployer_service_account_name="${name}-deployer-sa"\n'
                'runtime_service_account="$(printf \'%s\' "$parameters" | python3 -c \'import json,sys; print(json.load(sys.stdin)["serviceAccount"])\')"\n'
                'deployer_uid="$(datatug_bootstrap_inputs.py create-account --name "$deployer_service_account_name" \\\n'
                '  --namespace "$namespace" --app-name "$name" --app-uid "$app_uid" \\\n'
                '  --runtime-account "$runtime_service_account" --image-pull-secret "$image_pull_secret")"\n') + changed[sa_end:]
        elif relative.endswith("provision.py"):
            changed = once(original, "import yaml\n", "import yaml\nimport datatug_cleanup_bootstrap\n")
            changed = once(changed,
                           "                                          deployer_service_account_name)\n  return manifests\n",
                           "                                          deployer_service_account_name)\n  return datatug_cleanup_bootstrap.decorate(\n      manifests, namespace, deployer_service_account_name, app_name, app_params)\n")
        elif relative.endswith("print_config.py"):
            changed = once(original, "import yaml\n", "import yaml\nimport datatug_provider_values\n")
            changed = once(changed, "  return yaml.safe_dump(new_values,", "  new_values = datatug_provider_values.expand(new_values)\n  return yaml.safe_dump(new_values,")
        elif relative.endswith("set_ownership.py"):
            changed = once(original,
                '      "app.kubernetes.io/component") != "deployer.marketplace.cloud.google.com":',
                '      "app.kubernetes.io/component") not in (\n'
                '          "deployer.marketplace.cloud.google.com",\n'
                '          "deployer-rbac.marketplace.cloud.google.com"):')
        else:
            changed = original
        name = pathlib.Path(relative).name
        (destination / name).write_text(changed, encoding="utf-8")
        (destination / name).chmod(0o755)
        records[relative] = {"originalSha256": expected, "patchedSha256": hashlib.sha256(changed.encode()).hexdigest()}
        patches.extend(difflib.unified_diff(original.splitlines(True), changed.splitlines(True), fromfile="a/" + relative, tofile="b/" + relative))
    for source_name, target_name in (("cleanup_bootstrap.py", "datatug_cleanup_bootstrap.py"),
                                     ("bootstrap_inputs.py", "datatug_bootstrap_inputs.py"),
                                     ("provider_values.py", "datatug_provider_values.py"), ("cleanup.py", "datatug_cleanup.py")):
        shutil.copyfile(ROOT / source_name, destination / target_name)
        (destination / target_name).chmod(0o755)
    (destination / "clean_iam_resources.sh").write_text("#!/bin/sh\nset -eu\nexec python3 /bin/datatug_cleanup.py\n", encoding="utf-8")
    (destination / "clean_iam_resources.sh").chmod(0o755)
    for name in ("datatug_cleanup_bootstrap.py", "datatug_bootstrap_inputs.py", "datatug_provider_values.py", "datatug_cleanup.py", "clean_iam_resources.sh"):
        records["new/" + name] = {"newSha256": hashlib.sha256((destination / name).read_bytes()).hexdigest()}
    shutil.copyfile(source / "LICENSE", destination / "UPSTREAM-LICENSE")
    (destination / "sdk.patch").write_text("".join(patches), encoding="utf-8")
    (destination / "patch-hashes.json").write_text(json.dumps(records, indent=2) + "\n", encoding="utf-8")
    return records
