#!/usr/bin/env python3
"""Exercise actual SDK data/ownership stages and real Helm, without API calls."""
import argparse
import copy
import json
import os
import pathlib
import subprocess
import sys
import tempfile

PROVIDERS = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROVIDERS))
import validate
from gcp import assets, patch_sdk, pipeline, validate_schema


def refused(operation):
    try:
        operation()
    except Exception as error:
        if type(error).__name__ not in ("ValueError", "JSONDecodeError", "InvalidProperty"):
            raise
    else:
        raise ValueError("An invalid provider input was accepted")


def verify(source, helm, fixtures=None):
    if not __debug__:
        raise ValueError("Run verification without Python optimization")
    config = validate.read_json(PROVIDERS / "fixture.json")
    with tempfile.TemporaryDirectory() as temporary:
        root = pathlib.Path(temporary)
        patched = root / "patch"
        patch_sdk.prepare(source, patched)
        assets.prepare_schema(root / "schema.yaml", config)
        validate_schema.validate(root / "schema.yaml", source)
        subprocess.run(["bash", "-n", str(patched / "install")], check=True, capture_output=True)
        inputs = pipeline.schema_inputs(config)
        assert "image.repository" not in inputs
        assert "image.repository" not in assets.schema(config)["properties"]
        image = config["values"]["image"]["repository"] + "/deployer@" + config["values"]["image"]["digest"]
        transformed = pipeline.values(patched, config, inputs, image)
        assert "name" not in transformed and "namespace" not in transformed
        assert transformed["image"]["repository"] == config["values"]["image"]["repository"]
        actual, expected = root / "actual.json", root / "expected.json"
        actual.write_text(json.dumps(transformed))
        expected.write_text(json.dumps(config["values"]))
        assert validate.runtime(validate.render(validate.COMMON, actual, "runner", "datatug", helm)) == validate.runtime(validate.render(validate.COMMON, expected, "runner", "datatug", helm))
        tuned = copy.deepcopy(config)
        tuned["values"]["resources"]["requests"] = {"cpu": "250m", "memory": "512Mi"}
        tuned["values"]["resources"]["limits"] = {"cpu": "2", "memory": "2Gi"}
        tuned["values"]["networkPolicy"]["dnsNamespace"] = "approved-dns"
        actual.write_text(json.dumps(pipeline.values(patched, tuned, pipeline.schema_inputs(tuned), image)))
        expected.write_text(json.dumps(tuned["values"]))
        assert validate.runtime(validate.render(validate.COMMON, actual, "runner", "datatug", helm)) == validate.runtime(validate.render(validate.COMMON, expected, "runner", "datatug", helm))
        wrong = {**inputs, "image.repository": "caller-controlled.test/image"}
        refused(lambda: pipeline.values(patched, config, wrong, image))
        uid, app_uid = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
        manifests = pipeline.final_manifests(patched, config, inputs, "runner-deployer-sa", uid, app_uid, image)
        assert not any(resource["kind"] == "ServiceAccount" for resource in manifests)
        if fixtures:
            fixtures.write_text(json.dumps({"offlineFixture": True, "source": "actual SDK process -> set_app_labels -> patched set_ownership --noapp",
                "manifests": manifests}, indent=2) + "\n", encoding="utf-8")
        original = (source / "marketplace/deployer_util/set_ownership.py").read_bytes()
        fixed = (patched / "set_ownership.py").read_bytes()
        (patched / "set_ownership.py").write_bytes(original)
        refused(lambda: pipeline.final_manifests(patched, config, inputs, "runner-deployer-sa", uid, app_uid, image))
        (patched / "set_ownership.py").write_bytes(fixed)
        bad_inputs = [
            ("name", "bad\nname"), ("name", "bad'name"), ("name", "has space"),
            ("name", "--all"), ("name", "../path"), ("name", "a" * 52),
            ("namespace", "bad\nnamespace"), ("namespace", "BadNamespace"),
            ("serviceAccount", "runner-deployer-sa"), ("scope.namespace", "different"),
        ]
        sentinels = root / "sentinels"
        sentinels.mkdir()
        log = root / "api-called"
        for command in ("kubectl", "docker", "gcloud", "wget"):
            path = sentinels / command
            path.write_text("#!/bin/sh\nprintf called >> " + str(log) + "\nexit 91\n")
            path.chmod(0o755)
        environment = {**os.environ, "PATH": str(patched) + os.pathsep + str(sentinels) + os.pathsep + os.environ["PATH"]}
        control = subprocess.run(["bash", str(patched / "install"), "--parameters=" + json.dumps(inputs), "--deployer=" + image],
            env=environment, text=True, capture_output=True, check=False)
        assert control.returncode != 0 and log.exists(), "Valid preflight control did not reach the sentinel Docker API"
        log.unlink()
        for key, value in bad_inputs:
            parameters = {**inputs, key: value}
            result = subprocess.run(["bash", str(patched / "install"), "--parameters=" + json.dumps(parameters), "--deployer=" + image],
                env=environment, text=True, capture_output=True, check=False)
            assert result.returncode != 0 and not log.exists(), "Invalid bootstrap input reached an API command"
        scalar_cases = [
            ("sourceCredentialsJson", "["),
            ("sourceCredentialsJson", '[{"envName":"PGPASSWORD","secretName":"db","key":"a","key":"b"}]'),
            ("sourceCredentialsJson", json.dumps(config["values"]["sourceCredentials"] * 2)),
            ("sourceCredentialsJson", '[{"envName":"PGPASSWORD","secretName":"db","key":"password","value":"forbidden"}]'),
            ("sourceCredentialsJson", json.dumps(config["values"]["sourceCredentials"] * 51)),
            ("sourceCredentialsJson", '[{"envName":"DATATUG_INTERNAL_URL","secretName":"db","key":"url"}]'),
            ("databaseEgressJson", '[{"cidr":"10.0.0.0/16","port":true}]'),
            ("databaseEgressJson", '[{"cidr":"999.0.0.0/16","port":5432}]'),
            ("databaseEgressJson", json.dumps(config["values"]["networkPolicy"]["databaseEgress"] * 2)),
            ("unknown", "no"),
        ]
        for key, value in scalar_cases:
            parameters = {**inputs, "marketplaceInputs." + key: value}
            refused(lambda: pipeline.values(patched, config, parameters, image))
        bad = assets.schema(config)
        bad["required"].append("undefinedInput")
        (root / "schema.yaml").write_text(json.dumps(bad))
        refused(lambda: validate_schema.validate(root / "schema.yaml", source))
    return {"pinnedSdkRevision": validate_schema.REVISION,
            "actualSchemaMakeParamsExpandOutputHelm": "passed",
            "providerGeneratedImageRepository": "passed",
            "finalProvisionLabelsOwnershipPipeline": "passed; exactly one observed ServiceAccount owner on four RBAC objects",
            "unpatchedOwnershipRegressionRejected": True,
            "bootstrapInvalidInputsBeforeAnyApiCases": len(bad_inputs),
            "typedScalarArrayNegativeCases": len(scalar_cases),
            "officialSchemaPositiveNegative": "passed", "scope": "offline; no Kubernetes or Marketplace API"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=pathlib.Path, required=True)
    parser.add_argument("--helm", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--fixtures", type=pathlib.Path)
    args = parser.parse_args()
    receipt = verify(args.source, args.helm, args.fixtures)
    args.output.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(receipt, indent=2))


if __name__ == "__main__":
    main()
