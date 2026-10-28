#!/usr/bin/env python3
"""Prepare a local provider candidate; never publish or contact cloud APIs."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import sys
import tarfile
import tempfile

import yaml

import validate
from azure import assets as azure_assets
from azure import chart as azure_chart
from gcp import assets as google_assets
from gcp import validate_schema as google_schema
from gcp import pipeline as google_pipeline

ROOT = pathlib.Path(__file__).resolve().parent
REPOSITORY = ROOT.parents[2]


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def load_config(path, fixture, provider):
    config = validate.read_json(path)
    if not isinstance(config, dict):
        raise ValueError("Package input must be an object")
    if set(config) != {"offlineFixture", "releaseName", "packageVersion", "providerMetadata", "values"}:
        raise ValueError("Unexpected package input fields")
    if not isinstance(config["offlineFixture"], bool):
        raise ValueError("offlineFixture must be a boolean")
    if config["offlineFixture"] and not fixture:
        raise ValueError("Offline fixtures require explicit --fixture; never use them for submission")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", config["packageVersion"]):
        raise ValueError("A numeric package version is required")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,54}[a-z0-9]|[a-z0-9]", config["releaseName"]):
        raise ValueError("A DNS release name of at most 56 characters is required")
    metadata = config["providerMetadata"]
    if not isinstance(metadata, dict) or not isinstance(config["values"], dict):
        raise ValueError("Provider metadata and chart values must be objects")
    names = {"applicationName", "publisher", "registryServer", "partnerId", "solutionId", "partnerName", "deployerBase", "bootstrapBase"}
    required = {"aws": set(), "azure": {"applicationName", "publisher", "registryServer"},
                "gcp": {"partnerId", "solutionId", "partnerName", "deployerBase", "bootstrapBase"}}[provider]
    if not required.issubset(metadata) or not set(metadata).issubset(names) or any(not isinstance(v, str) or not v.strip() for v in metadata.values()):
        raise ValueError("Explicit nonempty provider packaging metadata required")
    if provider == "azure" and not re.fullmatch(r"[a-z0-9]+\.azurecr\.io", metadata["registryServer"]):
        raise ValueError("Azure registry must be an ACR login server")
    if provider == "gcp" and not re.fullmatch(r"gcr\.io/cloud-marketplace-tools/k8s/deployer_helm/onbuild@sha256:[a-f0-9]{64}", metadata["deployerBase"]):
        raise ValueError("A reviewed immutable official Google Helm deployer base is required")
    if provider == "gcp" and not re.fullmatch(r"gcr\.io/cloud-marketplace-tools/k8s/dev@sha256:[a-f0-9]{64}", metadata["bootstrapBase"]):
        raise ValueError("A reviewed immutable official Google bootstrap base is required")
    if not fixture and ("example.test" in json.dumps(config) or "UNVERIFIED" in json.dumps(config)):
        raise ValueError("Placeholder identities/images require --fixture")
    if not fixture and provider == "aws" and not re.fullmatch(
            r"[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com(?:\.cn)?/.+",
            config["values"]["image"]["repository"]):
        raise ValueError("AWS requires an explicit Marketplace-owned regional ECR repository; ownership remains externally verified")
    if config["values"].get("podAnnotations"):
        raise ValueError("Provider candidate annotations require separate workload-identity review")
    return config


def copy_common(bundle):
    for path in validate.COMMON.rglob("*"):
        if path.is_symlink():
            raise ValueError("Common chart symlinks are not permitted")
    shutil.copytree(validate.COMMON, bundle / "chart")
    for name in ("sources.json", "listing-draft.json"):
        shutil.copyfile(ROOT / name, bundle / name)
    docs_dir = bundle / "docs" / "providers"
    docs_dir.mkdir(parents=True)
    shared = (ROOT / "README.md").read_text(encoding="utf-8").replace("(../helm)", "(../../chart)")
    (docs_dir / "README.md").write_text(shared, encoding="utf-8")
    for provider in ("aws", "azure", "gcp"):
        (docs_dir / provider).mkdir()
        shutil.copyfile(ROOT / provider / "README.md", docs_dir / provider / "README.md")
    shutil.copyfile(ROOT.parent / "contract.md", bundle / "docs" / "contract.md")
    (bundle / "README.md").write_text(
        "# Offline DataTug provider candidate\n\n"
        "Not installed, submitted or published. See [package documentation](docs/providers/README.md) "
        "and CANDIDATE-NOT-FOR-SUBMISSION.json.\n", encoding="utf-8")
    shutil.copyfile(validate.COMMON / "LICENSE", bundle / "LICENSE")


def package(config_path, output, provider, helm, fixture=False, google_source=None):
    config = load_config(config_path, fixture, provider)
    if provider == "gcp" and not google_source:
        raise ValueError("Pinned official Google tools source is required")
    output = output.resolve()
    if output.is_relative_to(REPOSITORY) or output.exists() or output.with_suffix(".tar.gz").exists():
        raise ValueError("Use a new output directory outside the source repository")
    # Validate admission before creating an artifact or copying operator inputs.
    with tempfile.TemporaryDirectory() as temp:
        values = pathlib.Path(temp) / "values.json"
        write_json(values, config["values"])
        validate.render(validate.COMMON, values, config["releaseName"], config["values"]["scope"]["namespace"], helm)
    output.mkdir(parents=False)
    copy_common(output)
    write_json(output / "common-values.json", config["values"])
    write_json(output / "operator-values.json", config["values"])
    if provider == "azure":
        write_json(output / "operator-values.json", azure_chart.prepare(output / "chart", config["values"]))
        azure_assets.prepare(output, config)
    if provider == "gcp":
        google_assets.prepare(output, config, pathlib.Path(google_source))
        write_json(output / "official-schema-validation.json", google_schema.validate(output / "schema.yaml", pathlib.Path(google_source)))
        sdk_values = google_pipeline.values(output / "sdk-patch", config, google_pipeline.schema_inputs(config),
            config["values"]["image"]["repository"] + "/deployer@" + config["values"]["image"]["digest"])
        write_json(output / "sdk-expanded-values.json", sdk_values)
        sdk_render = validate.render(output / "chart", output / "sdk-expanded-values.json", config["releaseName"], config["values"]["scope"]["namespace"], helm)
        expected = validate.render(validate.COMMON, output / "common-values.json", config["releaseName"], config["values"]["scope"]["namespace"], helm)
        if validate.runtime(sdk_render, google_metadata=True) != validate.runtime(expected):
            raise ValueError("Actual SDK input expansion changed the runtime contract")
        write_json(output / "sdk-data-pipeline-validation.json", {
            "schemaInputsToMakeParamsToExpandConfigToOutputYamlToHelm": "passed",
            "providerGeneratedImageRepository": True, "providerOnlyInputsRemoved": True,
            "runtimeParity": True, "scope": "offline; no provider installation"})
    write_json(output / "CANDIDATE-NOT-FOR-SUBMISSION.json", {
        "provider": provider, "releaseName": config["releaseName"],
        "offlineFixture": bool(fixture), "status": "offline-source-candidate",
        "imagePullDigestVerified": False, "providerInstallVerified": False,
        "accountEligibilityVerified": False, "publicationApproved": False,
        "commonChartFilesSha256": {str(path.relative_to(validate.COMMON)): hashlib.sha256(path.read_bytes()).hexdigest()
                                  for path in sorted(validate.COMMON.rglob("*")) if path.is_file()},
    })
    shutil.copyfile(ROOT / provider / "README.md", output / "PROVIDER.md")
    receipt = validate.validate(output, helm)
    write_json(output / "validation.json", receipt)
    validate.run([helm, "package", str(output / "chart"), "--destination", str(output)])
    if any(path.suffix == ".pyc" or "__pycache__" in path.parts for path in output.rglob("*")):
        raise ValueError("Generated bytecode/cache files must not enter a source candidate")
    inventory = {str(path.relative_to(output)): hashlib.sha256(path.read_bytes()).hexdigest()
                 for path in sorted(output.rglob("*")) if path.is_file()}
    write_json(output / "sha256.json", inventory)
    archive = output.with_suffix(".tar.gz")
    if archive.exists():
        raise ValueError("Candidate archive already exists; refusing overwrite")
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(output, arcname=output.name)
    return {"provider": provider, "directory": str(output), "sourceArchive": str(archive),
            "archiveSha256": hashlib.sha256(archive.read_bytes()).hexdigest(),
            "status": "offline-only; no provider build/install/submission"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("provider", choices=["aws", "azure", "gcp"])
    parser.add_argument("--config", required=True, type=pathlib.Path)
    parser.add_argument("--output", required=True, type=pathlib.Path)
    parser.add_argument("--helm", default=os.environ.get("HELM", "helm"))
    parser.add_argument("--fixture", action="store_true")
    parser.add_argument("--google-tools-source", type=pathlib.Path, default=os.environ.get("GOOGLE_TOOLS_SOURCE"))
    args = parser.parse_args()
    try:
        print(json.dumps(package(args.config, args.output, args.provider, args.helm, args.fixture, args.google_tools_source), indent=2))
    except (ValueError, KeyError, OSError, yaml.YAMLError) as error:
        print(f"Candidate preparation refused: {type(error).__name__}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
