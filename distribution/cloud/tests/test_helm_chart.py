#!/usr/bin/env python3
"""Offline contract tests for the candidate cloud Helm chart."""

import json
import os
import pathlib
import re
import subprocess
import tarfile
import tempfile
import unittest


CLOUD = pathlib.Path(__file__).resolve().parents[1]
CHART = CLOUD / "helm"
HELM = os.environ.get("HELM", "helm")
DIGEST = "sha256:" + "a" * 64


def valid_values():
    return {
        "image": {"repository": "registry.example.test/datatug/cli", "digest": DIGEST},
        "trustedLocalOwner": True,
        "scope": {"namespace": "datatug", "environment": "staging"},
        "coordinator": {"endpoint": "https://coord.example.test/v0/datatug/watch", "port": 443, "caConfigMap": ""},
        "projectClaim": "project-data",
        "stateClaim": "private-state",
        "stateFilesystem": "node-local-block",
        "runnerSecret": {"name": "runner-token", "key": "token"},
        "serviceAccount": "datatug-runner",
        "podAnnotations": {},
        "sourceCredentials": [{"envName": "PGPASSWORD", "secretName": "db-creds", "key": "password"}],
        "resources": {"requests": {"cpu": "100m", "memory": "256Mi"}, "limits": {"cpu": "1", "memory": "768Mi"}},
        "networkPolicy": {"enabled": True, "dnsNamespace": "kube-system", "databaseEgress": [{"cidr": "10.10.0.0/16", "port": 5432}]},
    }


class HelmChartContractTests(unittest.TestCase):
    def render(self, values, release="runner", namespace="datatug"):
        with tempfile.NamedTemporaryFile(mode="w", suffix=".json", encoding="utf-8") as f:
            json.dump(values, f)
            f.flush()
            return subprocess.run(
                [HELM, "template", release, str(CHART), "--namespace", namespace, "--values", f.name],
                text=True, capture_output=True, check=False,
            )

    def test_valid_chart_renders_safe_single_runner(self):
        result = self.render(valid_values())
        self.assertEqual(result.returncode, 0, result.stderr)
        rendered = result.stdout
        self.assertIn("registry.example.test/datatug/cli@" + DIGEST, rendered)
        self.assertIn('claimName: "project-data"', rendered)
        self.assertIn("readOnly: true", rendered)
        self.assertIn("claimName: \"private-state\"", rendered)
        self.assertIn("- name: DATATUG_WATCH_RUNNER_TOKEN", rendered)
        self.assertIn("secretKeyRef:", rendered)
        self.assertIn("name: \"PGPASSWORD\"", rendered)
        self.assertIn("automountServiceAccountToken: false", rendered)
        self.assertIn("policyTypes: [Ingress, Egress]", rendered)
        self.assertIn("ingress: []", rendered)
        self.assertIn("port: 443", rendered)
        self.assertIn('cidr: "10.10.0.0/16"', rendered)
        self.assertIn("readOnlyRootFilesystem: true", rendered)
        self.assertIn("runAsUser: 65532", rendered)
        self.assertIn("datatug.io/project-id-source: project-manifest", rendered)
        self.assertNotIn("kind: Service", rendered)
        self.assertNotIn("kind: Ingress", rendered)
        self.assertNotIn("project:", rendered)

    def test_rejects_owner_image_scope_and_storage_admission_failures(self):
        cases = [
            (lambda v: v.update(trustedLocalOwner=False), "trustedLocalOwner"),
            (lambda v: v["image"].update(digest="latest"), "digest"),
            (lambda v: v["image"].update(repository="registry.test/app:latest"), "repository"),
            (lambda v: v["coordinator"].update(endpoint="http://coord.example.test"), "endpoint"),
            (lambda v: v["coordinator"].update(endpoint="https://user:pass@coord.example.test"), "endpoint"),
            (lambda v: v["coordinator"].update(endpoint="https://coord.example.test/path?token=x"), "endpoint"),
            (lambda v: v["coordinator"].update(endpoint="https://coord.example.test/#fragment"), "endpoint"),
            (lambda v: (v["coordinator"].update(endpoint="https://coord.example.test:8443"), v["coordinator"].update(port=443)), "explicit HTTPS endpoint port"),
            (lambda v: v["coordinator"].update(port=8443), "implicit HTTPS endpoint port"),
            (lambda v: v.update(stateFilesystem="network-filesystem"), "stateFilesystem"),
            (lambda v: v.update(stateClaim="project-data"), "distinct claims"),
            (lambda v: v.update(projectClaim="Bad_Name"), "projectClaim"),
            (lambda v: v["scope"].update(namespace="Bad_Name"), "/scope/namespace"),
        ]
        for mutate, expected in cases:
            with self.subTest(expected=expected):
                values = valid_values()
                mutate(values)
                result = self.render(values)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected.lower(), result.stderr.lower())

    def test_rejects_duplicate_and_reserved_source_secret_environment_names(self):
        for creds in (
            [
                {"envName": "PGPASSWORD", "secretName": "db-a", "key": "password"},
                {"envName": "PGPASSWORD", "secretName": "db-b", "key": "password"},
            ],
            [{"envName": "DATATUG_WATCH_RUNNER_TOKEN", "secretName": "bad", "key": "token"}],
            [{"envName": "SSL_CERT_FILE", "secretName": "bad", "key": "ca"}],
            [{"envName": "DATATUG_INTERNAL_URL", "secretName": "bad", "key": "url"}],
        ):
            with self.subTest(credentials=creds):
                values = valid_values()
                values["sourceCredentials"] = creds
                result = self.render(values)
                self.assertNotEqual(result.returncode, 0)
                if creds[0]["envName"] == "PGPASSWORD" and len(creds) == 2:
                    self.assertIn("duplicate or reserved", result.stderr.lower())

    def test_rejects_release_and_values_namespace_mismatch(self):
        result = self.render(valid_values(), release="A_bad")
        self.assertNotEqual(result.returncode, 0)
        result = self.render(valid_values(), namespace="other")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release namespace", result.stderr.lower())

    def test_configuration_only_upgrades_change_pod_template_checksum(self):
        values = valid_values()
        baseline = self.render(values)
        self.assertEqual(baseline.returncode, 0, baseline.stderr)
        def checksum(rendered):
            return re.search(r'datatug.io/config-checksum: "([a-f0-9]{64})"', rendered).group(1)
        original = checksum(baseline.stdout)
        for group, key, changed in (("scope", "environment", "production"),
                                    ("coordinator", "endpoint", "https://second.example.test")):
            with self.subTest(setting=key):
                updated = valid_values()
                updated[group][key] = changed
                result = self.render(updated)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotEqual(checksum(result.stdout), original)
        values["podAnnotations"]["datatug.io/config-checksum"] = "override"
        result = self.render(values)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("reserved", result.stderr)

    def test_chart_distribution_contains_apache_license(self):
        self.assertTrue((CHART / "LICENSE").is_file())
        self.assertTrue((CHART / "LICENSE").read_text(encoding="utf-8").startswith("                                 Apache License"))

    def test_actual_helm_lint_and_package_include_license_and_schema(self):
        with tempfile.TemporaryDirectory() as temp:
            values_path = pathlib.Path(temp) / "valid-values.json"
            values_path.write_text(json.dumps(valid_values()), encoding="utf-8")
            lint = subprocess.run([HELM, "lint", str(CHART), "--namespace", "datatug", "--values", str(values_path)], text=True, capture_output=True)
            self.assertEqual(lint.returncode, 0, lint.stdout + lint.stderr)
            package = subprocess.run([HELM, "package", str(CHART), "--destination", temp], text=True, capture_output=True)
            self.assertEqual(package.returncode, 0, package.stdout + package.stderr)
            archive = next(pathlib.Path(temp).glob("datatug-execution-*.tgz"))
            with tarfile.open(archive, "r:gz") as chart:
                names = chart.getnames()
            self.assertIn("datatug-execution/LICENSE", names)
            self.assertIn("datatug-execution/values.schema.json", names)


if __name__ == "__main__":
    unittest.main()
