"""Real Helm and provider parameter/serialization checks, without cloud resources."""
import copy
import json
import os
import pathlib
import shutil
import tempfile
import tarfile
import unittest

import yaml

import validate
import package
from azure import assets
from azure import chart as azure_chart
from gcp import assets as google_assets
from gcp import chart as google_chart

ROOT = pathlib.Path(__file__).resolve().parent
HELM = os.environ.get("HELM", "helm")


def fixture():
    return json.loads((ROOT / "fixture.json").read_text(encoding="utf-8"))


class ProviderAssetTests(unittest.TestCase):
    def render(self, values):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "values.json"
            assets.write_json(path, values)
            return validate.render(validate.COMMON, path, "runner", "datatug", HELM)

    def test_real_helm_runtime_and_security_mutation_rejections(self):
        rendered = self.render(fixture()["values"])
        validate.check_runtime(rendered, "datatug")
        original = validate.docs(rendered)
        deployment_index = next(i for i, d in enumerate(original) if d["kind"] == "Deployment")
        changes = (
            lambda d: d["spec"].update(replicas=2),
            lambda d: d["spec"]["strategy"].update(type="RollingUpdate"),
            lambda d: d["spec"]["template"]["spec"].update(automountServiceAccountToken=True),
            lambda d: d["spec"]["template"]["spec"]["containers"][0]["securityContext"].update(readOnlyRootFilesystem=False),
            lambda d: d["spec"]["template"]["spec"]["containers"][0].update(image="registry.test/cli:latest"),
        )
        for change in changes:
            mutated = copy.deepcopy(original)
            change(mutated[deployment_index])
            with self.subTest(mutation=change), self.assertRaises(ValueError):
                validate.check_runtime(yaml.safe_dump_all(mutated), "datatug")
        with self.assertRaises(ValueError):
            validate.check_runtime(rendered + "\n---\nkind: Service\nmetadata: {namespace: datatug}\n", "datatug")

    def test_azure_assets_have_existing_cluster_only_and_complete_parameter_flow(self):
        with tempfile.TemporaryDirectory() as directory:
            bundle = pathlib.Path(directory)
            assets.prepare(bundle, fixture())
            arm = validate.read_json(bundle / "mainTemplate.json")
            ui = validate.read_json(bundle / "createUIDefinition.json")
            manifest = yaml.safe_load((bundle / "manifest.yaml").read_text())
        self.assertEqual(manifest["extensionRegistrationParameters"], {"defaultScope": "namespace"})
        self.assertEqual(set(arm["parameters"]), set(ui["parameters"]["outputs"]))
        self.assertEqual(len(arm["resources"]), 1)
        extension = arm["resources"][0]
        self.assertEqual(extension["type"], "Microsoft.KubernetesConfiguration/extensions")
        self.assertEqual(extension["scope"], "[parameters('clusterResourceId')]")
        props = extension["properties"]
        self.assertFalse(props["autoUpgradeMinorVersion"])
        self.assertEqual(props["configurationProtectedSettings"], {})
        self.assertEqual(props["scope"]["namespace"]["targetNamespace"], "[parameters('scope-namespace')]")
        keys = props["configurationSettings"]
        self.assertIn("sourceCredentials[0].secretName", keys)
        self.assertIn("networkPolicy.databaseEgress[0].cidr", keys)
        self.assertNotIn("image.repository", keys)
        self.assertNotIn("image.digest", keys)
        for key, expression in keys.items():
            self.assertEqual(expression, f"[parameters('{assets.parameter_name(key)}')]")

    def test_indexed_azure_settings_render_the_same_runtime_with_real_helm(self):
        values = fixture()["values"]
        args = []
        for key, value in assets.flatten(values):
            if key.startswith(("resources.", "podAnnotations.")):
                continue
            value = json.dumps(value) if isinstance(value, bool) else str(value)
            args.extend(["--set", f"{key}={value}"])
        result = validate.run([HELM, "template", "runner", str(validate.COMMON), "--namespace", "datatug", *args])
        self.assertEqual(validate.runtime(result), validate.runtime(self.render(values)))

    def test_json_manifest_serialization_cannot_inject_sibling_fields(self):
        config = fixture()
        config["providerMetadata"]["publisher"] = 'quoted "name"\nregistryServer: attacker.test'
        with tempfile.TemporaryDirectory() as directory:
            bundle = pathlib.Path(directory)
            assets.prepare(bundle, config)
            manifest = yaml.safe_load((bundle / "manifest.yaml").read_text())
        self.assertEqual(manifest["registryServer"], "fixture.azurecr.io")
        self.assertEqual(manifest["publisher"], config["providerMetadata"]["publisher"])

    def test_fixture_admission_and_invalid_common_values_produce_no_package(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            output = root / "candidate"
            with self.assertRaises(ValueError):
                package.package(ROOT / "fixture.json", output, "aws", HELM)
            self.assertFalse(output.exists())
            config = fixture()
            config["values"]["runnerSecret"]["token"] = "must-never-be-a-manifest-input"
            path = root / "bad.json"
            assets.write_json(path, config)
            with self.assertRaises(ValueError):
                package.package(path, output, "aws", HELM, fixture=True)
            self.assertFalse(output.exists())

    def test_actual_aws_package_retains_common_chart_schema_license_and_parity(self):
        with tempfile.TemporaryDirectory() as directory:
            output = pathlib.Path(directory) / "aws"
            receipt = package.package(ROOT / "fixture.json", output, "aws", HELM, fixture=True)
            archive = pathlib.Path(receipt["sourceArchive"])
            self.assertTrue(archive.is_file())
            with tarfile.open(archive, "r:gz") as tar:
                names = tar.getnames()
            self.assertIn("aws/chart/values.schema.json", names)
            self.assertIn("aws/chart/LICENSE", names)
            self.assertIn("aws/docs/providers/aws/README.md", names)
            marker = validate.read_json(output / "CANDIDATE-NOT-FOR-SUBMISSION.json")
            self.assertTrue(marker["offlineFixture"])
            self.assertFalse(marker["publicationApproved"])
            self.assertFalse(marker["imagePullDigestVerified"])
            self.assertEqual(validate.read_json(output / "validation.json")["providerInstall"], "not-run")
            for original in validate.COMMON.rglob("*"):
                if original.is_file():
                    self.assertEqual(original.read_bytes(), (output / "chart" / original.relative_to(validate.COMMON)).read_bytes())
            with self.assertRaises(ValueError):
                package.package(ROOT / "fixture.json", output, "aws", HELM, fixture=True)

    def test_google_schema_uses_scalar_inputs_and_no_privileged_runtime_role(self):
        schema = google_assets.schema(fixture())
        self.assertIn("image.digest", schema["required"])
        self.assertFalse(schema["properties"]["trustedLocalOwner"]["default"])
        for key, prop in schema["properties"].items():
            self.assertNotIn("[", key)
            self.assertIn(prop["type"], ("string", "integer", "boolean"))
        market = schema["x-google-marketplace"]
        self.assertEqual(market["images"][""]["properties"]["image.repository"]["type"], "REPO_WITH_REGISTRY")
        for role in market["deployerServiceAccount"]["roles"]:
            self.assertEqual(role["type"], "Role")
            self.assertEqual(role["rulesType"], "CUSTOM")
            for rule in role["rules"]:
                self.assertNotIn("*", rule["verbs"])
                self.assertTrue(set(rule["resources"]).isdisjoint({"secrets", "persistentvolumeclaims", "serviceaccounts", "roles", "rolebindings", "customresourcedefinitions"}))

    def test_azure_image_projection_supports_registry_prefix_and_rejects_unsafe_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            chart = root / "chart"
            shutil.copytree(validate.COMMON, chart)
            values = azure_chart.prepare(chart, fixture()["values"])
            path = root / "values.json"
            image = values["global"]["azure"]["images"]["runner"]
            image["registry"], image["image"] = "registry.example.test/prefix", "datatug/cli"
            assets.write_json(path, values)
            rendered = validate.render(chart, path, "runner", "datatug", HELM)
            self.assertIn("registry.example.test/prefix/datatug/cli@sha256:", rendered)
            for key, bad in (("registry", "https://registry.test"), ("registry", "user@registry.test"),
                             ("registry", "registry.test:443"), ("registry", "registry.test/prefix:tag"),
                             ("registry", "registry.test/ white"), ("image", "cli:latest"),
                             ("image", "../cli"), ("digest", "sha256:" + "A" * 64), ("digest", "")):
                mutated = copy.deepcopy(values)
                mutated["global"]["azure"]["images"]["runner"][key] = bad
                assets.write_json(path, mutated)
                with self.subTest(key=key, bad=bad), self.assertRaises(ValueError):
                    validate.render(chart, path, "runner", "datatug", HELM)

    def test_azure_image_adapter_fails_when_common_template_anchor_drifts(self):
        with tempfile.TemporaryDirectory() as directory:
            chart = pathlib.Path(directory) / "chart"
            shutil.copytree(validate.COMMON, chart)
            source = chart / "templates" / "runner.yaml"
            source.write_text(source.read_text().replace(".Values.image.digest", ".Values.changed.digest"))
            with self.assertRaises(ValueError):
                azure_chart.prepare(chart, fixture()["values"])

    def test_google_application_groups_generated_resources_and_preserves_runtime(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            chart = root / "chart"
            shutil.copytree(validate.COMMON, chart)
            config = fixture()
            config["providerMetadata"]["partnerName"] = 'quoted VERSION }} {{ fail "injection" }} name'
            google_chart.prepare(chart, config)
            path = root / "values.json"
            assets.write_json(path, config["values"])
            rendered = validate.render(chart, path, "runner", "datatug", HELM)
            self.assertEqual(validate.runtime(rendered, google_metadata=True), validate.runtime(self.render(config["values"])))
            resources = validate.docs(rendered)
            app = next(r for r in resources if r["kind"] == "Application")
            info = json.loads(app["metadata"]["annotations"]["marketplace.cloud.google.com/deploy-info"])
            self.assertEqual(info["partner_name"], config["providerMetadata"]["partnerName"])
            for resource in resources:
                self.assertEqual(resource["metadata"]["labels"]["app.kubernetes.io/instance"], "runner")
            self.assertEqual({c["kind"] for c in app["spec"]["componentKinds"]}, {"Deployment", "ConfigMap", "NetworkPolicy"})
            mutated = copy.deepcopy(resources)
            next(r for r in mutated if r["kind"] == "ConfigMap")["metadata"]["labels"]["unexpected"] = "cannot-normalize-away"
            self.assertNotEqual(validate.runtime(yaml.safe_dump_all(mutated), google_metadata=True), validate.runtime(self.render(config["values"])))


if __name__ == "__main__":
    unittest.main()
