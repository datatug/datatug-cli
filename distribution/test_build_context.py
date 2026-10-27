"""Version contract tests without resolving modules or compiling Go binaries."""

import contextlib
import importlib.util
import io
import hashlib
import json
from pathlib import Path
import shlex
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("build_context", Path(__file__).with_name("build_context.py"))
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class VersionTests(unittest.TestCase):
    def test_strict_semver(self):
        for version in ("0.0.0-candidate", "1.2.3", "1.2.3-rc.1+build.04", "1.2.3+001", "1.2.3-0", "1.2.3-01a"):
            with self.subTest(version=version):
                self.assertEqual(builder.validate_version(version), version)

    def test_rejects_invalid_versions_and_linker_injection(self):
        for version in ("", "v1.2.3", "01.2.3", "1.02.3", "1.2.03", "1.2", "1.2.3-01",
                        "1.2.3-", "1.2.3+", "1.2.3-rc..1", "1.2.3\n", "1.2.3\x00",
                        "1.2.3 -X other=value", "1.2.3;echo", "1.2.3+$(id)", "1.2.3+é"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                builder.validate_version(version)

    def test_invalid_version_fails_before_source_or_build_commands(self):
        with patch.object(builder, "source_provenance") as provenance, patch.object(builder, "run") as run:
            with self.assertRaises(ValueError):
                builder.build_context(Path("unused"), {}, "1.2.3 -X injected=value")
        provenance.assert_not_called()
        run.assert_not_called()

    def test_dirty_stable_release_fails_before_expensive_work(self):
        for version in ("1.2.3", "1.2.3+build.1"):
            with self.subTest(version=version), patch.object(builder, "source_provenance", return_value=("abc", "dirty", "digest")), \
                    patch.object(builder, "module_inventory") as modules, patch.object(builder, "run") as run:
                with self.assertRaisesRegex(RuntimeError, "clean source working tree"):
                    builder.build_context(Path("unused"), {}, version)
                modules.assert_not_called()
                run.assert_not_called()

    def build_fake_context(self, version=None, dirty=False):
        commands = []

        def fake_run(command, **kwargs):
            commands.append(command)
            if command[:2] == ["go", "build"]:
                Path(command[command.index("-o") + 1]).write_bytes(b"fake binary")
                return ""
            return "2026-10-25T00:00:00+00:00\n"

        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "context"
            state = "dirty (working-copy content included)" if dirty else "clean"
            with patch.object(builder, "source_provenance", return_value=("a" * 40, state, "digest" if dirty else "none")), \
                    patch.object(builder, "module_inventory", return_value={builder.CORE_MODULE: {"version": builder.CORE_VERSION}}), \
                    patch.object(builder, "run", side_effect=fake_run), \
                    patch.object(builder, "write_module_notices", return_value=[]), \
                    patch.object(builder, "copy_project_notices"):
                result = builder.build_context(output, {}) if version is None else builder.build_context(output, {}, version)
            inventory = (output / "CONTEXT-INVENTORY.md").read_text()
            dockerfile = (output / "Dockerfile").read_text()
        self.assertEqual(result, ([], 1))
        builds = [command for command in commands if command[:2] == ["go", "build"]]
        self.assertEqual(len(builds), 4)
        expected_version = version or builder.DEFAULT_VERSION
        for command in builds:
            flags = [arg for arg in command if arg.startswith("-ldflags=")]
            self.assertEqual(len(flags), 1)
            tokens = shlex.split(flags[0].removeprefix("-ldflags="))
            self.assertEqual(tokens.count("-X"), 3)
            self.assertIn(f"github.com/strongo/buildinfo.version={expected_version}", tokens)
        self.assertIn(f"Binary version: `{expected_version}`", inventory)
        self.assertIn(f"Source working tree: **{state}**", inventory)
        self.assertIn("ARG DATATUG_SOURCE_STATE=" + ("dirty" if dirty else "clean"), dockerfile)

    def test_default_candidate_keeps_dirty_working_copy_compatibility(self):
        self.build_fake_context(dirty=True)

    def test_stable_version_reaches_all_binaries_and_inventory(self):
        self.build_fake_context("1.2.3+build.1")

    def test_prerelease_retains_explicit_dirty_provenance(self):
        self.build_fake_context("1.2.3-rc.1", dirty=True)

    def test_cli_passes_explicit_version_and_keeps_default(self):
        with tempfile.TemporaryDirectory() as directory:
            for version in (None, "1.2.3"):
                argv = ["build_context.py", "--output", str(Path(directory) / "context")]
                if version:
                    argv.extend(["--version", version])
                with self.subTest(version=version), patch("sys.argv", argv), \
                        patch.object(builder, "build_context", return_value=([], 0)) as build, \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(builder.main(), 0)
                    self.assertEqual(build.call_args.args[2], version or builder.DEFAULT_VERSION)


class ProjectNoticeTests(unittest.TestCase):
    REQUIRED = {
        "LICENSE": "PROJECT-LICENSE",
        "THIRD_PARTY_NOTICES.md": "PROJECT-THIRD_PARTY_NOTICES.md",
        "LICENSING.md": "PROJECT-LICENSING.md",
        "LICENSES/Apache-2.0.txt": "PROJECT-LICENSES/Apache-2.0.txt",
        "LICENSES/COMMERCIAL-TERMS.md": "PROJECT-LICENSES/COMMERCIAL-TERMS.md",
    }

    def fixture(self, directory):
        root = Path(directory) / "source"
        context = Path(directory) / "context"
        goroot = Path(directory) / "go"
        context.mkdir()
        (context / "licenses").mkdir()
        for source in self.REQUIRED:
            path = root / source
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(("DRAFT — PENDING QUALIFIED LEGAL REVIEW\n" + source + "\n").encode())
        manifest = root / "pkg/mcpserver/app/build-manifest.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text('{"format":"datatug.mcp-app.build.v1"}\n')
        (goroot / "src/vendor/example").mkdir(parents=True)
        (goroot / "LICENSE").write_bytes(b"Go license\n")
        (goroot / "src/vendor/example/LICENSE").write_bytes(b"Vendored notice\n")
        return root, context, goroot

    def copy(self, root, context, goroot):
        with patch.object(builder, "ROOT", root), patch.object(builder, "run", return_value=str(goroot)):
            builder.copy_project_notices(context, {})

    def test_real_copier_retains_scoped_bytes_and_closed_inventory(self):
        with tempfile.TemporaryDirectory() as directory:
            root, context, goroot = self.fixture(directory)
            self.copy(root, context, goroot)
            entries = json.loads((context / "licenses/PROJECT-LICENSE-INVENTORY.json").read_text())["files"]
            expected = {**self.REQUIRED, "pkg/mcpserver/app/build-manifest.json": "PROJECT-MCP-APP-BUILD-MANIFEST.json"}
            self.assertEqual({entry["source"]: entry["destination"] for entry in entries},
                             {source: "licenses/" + dest for source, dest in expected.items()})
            for entry in entries:
                retained = context / entry["destination"]
                self.assertEqual(retained.read_bytes(), (root / entry["source"]).read_bytes())
                self.assertEqual(entry["sha256"], hashlib.sha256(retained.read_bytes()).hexdigest())
                self.assertTrue(entry["scope"])
                self.assertEqual(retained.stat().st_mode & 0o777, 0o444)
            self.assertEqual((context / "licenses/GO-LICENSE").read_bytes(), b"Go license\n")
            self.assertEqual((context / "licenses/GO-VENDOR-example_LICENSE").read_bytes(), b"Vendored notice\n")

    def test_every_required_own_file_is_mandatory(self):
        for source in self.REQUIRED:
            with self.subTest(source=source), tempfile.TemporaryDirectory() as directory:
                root, context, goroot = self.fixture(directory)
                (root / source).unlink()
                with self.assertRaisesRegex(RuntimeError, "required project"):
                    self.copy(root, context, goroot)

    def test_missing_mcp_build_manifest_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root, context, goroot = self.fixture(directory)
            (root / "pkg/mcpserver/app/build-manifest.json").unlink()
            with self.assertRaisesRegex(RuntimeError, "required project"):
                self.copy(root, context, goroot)

    def test_unsuitable_required_text_and_symlink_parent_fail_closed(self):
        for kind in ("empty", "whitespace", "oversized", "non-utf8", "nul", "directory", "symlink", "parent-symlink"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                root, context, goroot = self.fixture(directory)
                path = root / "LICENSES/COMMERCIAL-TERMS.md"
                original = path.read_bytes()
                path.unlink()
                if kind == "directory":
                    path.mkdir()
                elif kind == "symlink":
                    path.symlink_to(root / "LICENSE")
                elif kind == "parent-symlink":
                    (root / "LICENSES").rename(root / "real-licenses")
                    (root / "real-licenses/COMMERCIAL-TERMS.md").write_bytes(original)
                    (root / "LICENSES").symlink_to(root / "real-licenses", target_is_directory=True)
                else:
                    path.write_bytes({"empty": b"", "whitespace": b" \n", "oversized": b"x" * (4 * 1024 * 1024 + 1), "non-utf8": b"\xff", "nul": b"license\0text"}[kind])
                with self.assertRaisesRegex(RuntimeError, "required project"):
                    self.copy(root, context, goroot)

    def test_core_mit_dependency_notice_still_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            module = root / "core"
            module.mkdir()
            (module / "LICENSE").write_bytes(b"MIT License\nCore notice\n")
            self.assertEqual(builder.write_module_notices(root / "context", {
                builder.CORE_MODULE: {"version": builder.CORE_VERSION, "dir": str(module)}}), [])
            retained = list((root / "context/licenses/modules").rglob("LICENSE"))
            self.assertEqual(len(retained), 1)
            self.assertEqual(retained[0].read_bytes(), (module / "LICENSE").read_bytes())

    def test_image_metadata_discloses_composite_scope_without_aggregate_grant(self):
        root = Path(__file__).resolve().parents[1]
        dockerfile = (root / "distribution/Dockerfile").read_text()
        self.assertNotIn("org.opencontainers.image.licenses=", dockerfile)
        listing = json.loads((root / "distribution/cloud/providers/listing-draft.json").read_text())
        for term in ("commercial/licensing", "DRAFT", "MCP", "LICENSING.md"):
            self.assertIn(term, listing["sourceLicense"])
        self.assertFalse(listing["ownerPublicationApproved"])
        self.assertFalse(listing["providerInstallationVerified"])


if __name__ == "__main__":
    unittest.main()
