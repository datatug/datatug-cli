"""Offline semantic fixtures; no Docker, network, execution or extraction."""
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("verify_provenance", Path(__file__).with_name("verify_provenance.py"))
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)
SOURCE = "a" * 40
RUN = "40591945176"


class Fixture:
    def __init__(self, directory, *, arch="amd64", config_change=None, statement_change=None,
                 reference_change=None, index_change=None, layer_change=None, wrappers=1):
        self.directory = Path(directory)
        self.blobs = {}
        self.arch = arch
        config = {"os": "linux", "architecture": arch, "config": {"Labels": {
            "org.opencontainers.image.revision": SOURCE, "io.datatug.source-state": "clean",
            "io.datatug.source-status-digest": "none"}}}
        if config_change:
            config_change(config)
        self.config = self.blob(config, validator.CONFIG)
        self.layer = self.blob(b"\x00\xffnot JSON: binary layer fixture", "application/vnd.oci.image.layer.v1.tar")
        if layer_change:
            layer_change(self.layer)
        self.target = self.blob({"schemaVersion": 2, "config": self.config, "layers": [self.layer]}, validator.MANIFEST)
        self.target["platform"] = {"os": "linux", "architecture": arch}
        statement = {"_type": "https://in-toto.io/Statement/v1", "predicateType": validator.PREDICATE,
                     "subject": [{"name": "datatug-everywhere:candidate", "digest": {"sha256": self.target["digest"][7:]}}],
                     "predicate": {"buildDefinition": {"buildType": validator.BUILD_TYPE, "internalParameters": {
                         "github_repository": "datatug/datatug-cli", "github_job": "container", "github_workflow_sha": SOURCE,
                         "github_run_id": RUN, "github_run_attempt": "1"}}, "runDetails": {"builder": {"id": ""}}}}
        if statement_change:
            statement_change(statement)
        self.statement = self.blob(statement, validator.STATEMENT)
        self.statement["annotations"] = {"in-toto.io/predicate-type": validator.PREDICATE}
        att_config = self.blob({}, validator.EMPTY_CONFIG)
        self.attestation = self.blob({"schemaVersion": 2, "config": att_config, "layers": [self.statement]}, validator.MANIFEST)
        self.attestation["platform"] = {"os": "unknown", "architecture": "unknown"}
        self.attestation["annotations"] = {"vnd.docker.reference.type": "attestation-manifest",
                                          "vnd.docker.reference.digest": self.target["digest"]}
        if reference_change:
            reference_change(self.attestation)
        children = [self.target, self.attestation]
        if index_change:
            index_change(children)
        image_index = {"schemaVersion": 2, "mediaType": validator.INDEX, "manifests": children}
        self.image_index = self.blob(image_index, validator.INDEX)
        self.root = {"schemaVersion": 2, "mediaType": validator.INDEX, "manifests": [self.image_index]}
        for _ in range(wrappers - 1):
            self.root = {"schemaVersion": 2, "manifests": [self.blob(self.root, validator.INDEX)]}
        self.meta = {"containerimage.digest": self.image_index["digest"], "containerimage.descriptor": self.image_index,
                     "containerimage.config.digest": self.config["digest"]}
        self.runtime = self.config["digest"]
        self.oci = self.directory / "candidate.oci.tar"
        self.metadata = self.directory / "build.json"

    def blob(self, value, media):
        raw = value if isinstance(value, bytes) else json.dumps(value, sort_keys=True).encode()
        digest = hashlib.sha256(raw).hexdigest()
        self.blobs["blobs/sha256/" + digest] = raw
        return {"digest": "sha256:" + digest, "size": len(raw), "mediaType": media}

    def write(self, extras=()):
        with tarfile.open(self.oci, "w") as tar:
            items = [("oci-layout", b'{"imageLayoutVersion":"1.0.0"}'), ("index.json", json.dumps(self.root).encode()), *self.blobs.items()]
            for name, raw in items:
                entry = tarfile.TarInfo(name)
                entry.size = len(raw)
                tar.addfile(entry, io.BytesIO(raw))
            for entry, raw in extras:
                tar.addfile(entry, io.BytesIO(raw))
        self.metadata.write_text(json.dumps(self.meta))
        return self

    def verify(self, **changes):
        args = dict(oci=self.oci, metadata=self.metadata, arch=self.arch, source_sha=SOURCE,
                    runtime_config_id=self.runtime, run_id=RUN, run_attempt="1")
        args.update(changes)
        return validator.verify(**args)


class BindingTests(unittest.TestCase):
    def reject(self, fixture, code=None, **changes):
        with self.assertRaises(validator.InvalidProvenance) as caught:
            fixture.verify(**changes)
        if code:
            self.assertEqual(str(caught.exception), code)

    def test_valid_nested_architectures_and_unsigned_scope(self):
        for arch in ("amd64", "arm64"):
            with self.subTest(arch=arch), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory, arch=arch).write()
                receipt = f.verify()
                self.assertTrue(receipt["accepted"])
                self.assertEqual(receipt["subjectDigest"], f.target["digest"])
                self.assertEqual(receipt["runtimeConfigDigest"], f.config["digest"])
                self.assertFalse(receipt["signatureVerified"])
                self.assertFalse(receipt["builderIdentityVerified"])
                self.assertFalse(receipt["completeDependencyCensus"])
                self.assertFalse(receipt["slsaLevelClaimed"])
                self.assertNotIn("predicate", receipt)

    def test_empty_missing_foreign_and_ambiguous_subjects(self):
        cases = [(lambda s: s.update(subject=[]), "empty_statement_subject"),
                 (lambda s: s.pop("subject"), "empty_statement_subject"),
                 (lambda s: s["subject"][0]["digest"].update(sha256="b" * 64), "statement_subject_mismatch"),
                 (lambda s: s["subject"].append(copy.deepcopy(s["subject"][0])), "ambiguous_statement_subject"),
                 (lambda s: s["subject"][0].update(digest={"sha512": "b" * 128}), "invalid_statement_subject"),
                 (lambda s: s["subject"][0].update(name=""), "invalid_statement_subject")]
        for change, code in cases:
            with self.subTest(code=code), tempfile.TemporaryDirectory() as directory:
                self.reject(Fixture(directory, statement_change=change).write(), code)

    def test_statement_predicate_and_existing_source_run_fields(self):
        changes = [lambda s: s.update(_type="wrong"), lambda s: s.update(predicateType="wrong"),
                   lambda s: s["predicate"]["buildDefinition"].update(buildType="wrong")]
        for key in ("github_run_id", "github_run_attempt", "github_workflow_sha", "github_repository", "github_job"):
            changes.append(lambda s, key=key: s["predicate"]["buildDefinition"]["internalParameters"].update({key: "wrong"}))
        for change in changes:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                self.reject(Fixture(directory, statement_change=change).write())

    def test_descriptor_reference_and_platform_ambiguity(self):
        cases = [dict(reference_change=lambda d: d["annotations"].update({"vnd.docker.reference.digest": "sha256:" + "b" * 64})),
                 dict(reference_change=lambda d: d["annotations"].update({"vnd.docker.reference.type": "wrong"})),
                 dict(index_change=lambda ds: ds.append(copy.deepcopy(ds[0]))),
                 dict(index_change=lambda ds: ds.append(copy.deepcopy(ds[1]))),
                 dict(index_change=lambda ds: ds.append({**copy.deepcopy(ds[1]), "annotations": {
                     "vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": ds[1]["digest"]}})),
                 dict(index_change=lambda ds: ds[0].update(platform={"os": "linux", "architecture": "foreign"}))]
        for case in cases:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                self.reject(Fixture(directory, **case).write())

    def test_source_and_runtime_binding(self):
        changes = [lambda c: c.update(architecture="foreign"),
                   lambda c: c["config"]["Labels"].update({"org.opencontainers.image.revision": "b" * 40}),
                   lambda c: c["config"]["Labels"].update({"io.datatug.source-state": "dirty"}),
                   lambda c: c["config"]["Labels"].update({"io.datatug.source-status-digest": "digest"})]
        for change in changes:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                self.reject(Fixture(directory, config_change=change).write())
        with tempfile.TemporaryDirectory() as directory:
            f = Fixture(directory).write()
            self.reject(f, "runtime_config_mismatch", runtime_config_id="sha256:" + "b" * 64)
            self.reject(f, "provenance_source_run_mismatch", run_id="42")

    def test_metadata_must_bind_reachable_image_index_and_config(self):
        for key in ("containerimage.digest", "containerimage.config.digest"):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory)
                f.meta[key] = "sha256:" + "b" * 64
                self.reject(f.write())
        with tempfile.TemporaryDirectory() as directory:
            f = Fixture(directory)
            f.meta["containerimage.digest"] = f.target["digest"]
            self.reject(f.write(), "metadata_index_not_reachable")
        with tempfile.TemporaryDirectory() as directory:
            f = Fixture(directory)
            f.meta["containerimage.descriptor"] = {**f.image_index, "size": f.image_index["size"] + 1}
            self.reject(f.write(), "descriptor_size_mismatch")

    def test_all_referenced_layer_bytes_are_hashed_without_json_parsing(self):
        for kind in ("corrupt", "missing", "size", "hash"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory, layer_change=(lambda d: d.update(size=d["size"] + 1)) if kind == "size" else
                            (lambda d: d.update(digest="sha512:" + "a" * 128)) if kind == "hash" else None)
                if kind in ("corrupt", "missing"):
                    name = "blobs/sha256/" + f.layer["digest"][7:]
                    if kind == "missing":
                        f.blobs.pop(name)
                    else:
                        f.blobs[name] = b"x" * len(f.blobs[name])
                self.reject(f.write())

    def test_missing_corrupt_json_blobs_and_descriptor_sizes(self):
        for kind in ("missing", "corrupt", "size"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory)
                name = "blobs/sha256/" + f.target["digest"][7:]
                if kind == "missing":
                    f.blobs.pop(name)
                elif kind == "corrupt":
                    f.blobs[name] = b"x" * len(f.blobs[name])
                else:
                    f.image_index["size"] += 1
                self.reject(f.write())

    def test_archive_names_types_and_duplicate_entries(self):
        for name, kind in [("../outside", tarfile.REGTYPE), ("/outside", tarfile.REGTYPE),
                           ("blobs\\outside", tarfile.REGTYPE), ("index.json", tarfile.REGTYPE),
                           ("blobs/sha256/" + "c" * 64, tarfile.SYMTYPE),
                           ("blobs/sha256/" + "c" * 64, tarfile.LNKTYPE)]:
            with self.subTest(name=name, kind=kind), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory)
                entry = tarfile.TarInfo(name)
                entry.type = kind
                entry.linkname = "../../private"
                self.reject(f.write([(entry, b"")]))

    def test_json_duplicates_types_numbers_and_depth_fail_closed(self):
        cases = [b'{"x":1,"x":2}', b'[]', b'{"x":NaN}', b'{"x":1e400}', b'\xff',
                 json.dumps({"nested": [[[[None]]]]}).encode()]
        for raw in cases:
            with self.subTest(raw=raw), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory).write()
                f.metadata.write_bytes(raw)
                self.reject(f)
        with tempfile.TemporaryDirectory() as directory:
            f = Fixture(directory).write()
            nested = None
            for _ in range(70):
                nested = [nested]
            f.metadata.write_text(json.dumps({**f.meta, "nested": nested}))
            self.reject(f, "json_structure_limit")

    def test_archive_blob_json_and_graph_limits(self):
        for key, value in [("MAX_TAR", 512), ("MAX_ENTRIES", 1), ("MAX_JSON", 8),
                           ("MAX_BLOB", 8), ("MAX_DESCRIPTORS", 1)]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                f = Fixture(directory).write()
                with patch.object(validator, key, value):
                    self.reject(f)
        with tempfile.TemporaryDirectory() as directory:
            self.reject(Fixture(directory, wrappers=6).write(), "index_cycle_or_depth")
        with tempfile.TemporaryDirectory() as directory:
            f = Fixture(directory)
            entry = tarfile.TarInfo("pax-metadata")
            entry.type = tarfile.XHDTYPE
            entry.size = validator.MAX_JSON + 1024
            self.reject(f.write([(entry, b"x" * entry.size)]), "unsafe_archive_type")

    def test_cli_output_is_closed_and_excludes_raw_paths_or_errors(self):
        with tempfile.TemporaryDirectory(prefix="private-marker-") as directory:
            f = Fixture(directory).write()
            f.oci.write_bytes(b"not a tar")
            output, diagnostics = io.StringIO(), io.StringIO()
            args = ["--oci", str(f.oci), "--metadata", str(f.metadata), "--arch", f.arch,
                    "--source-sha", SOURCE, "--runtime-config-id", f.runtime, "--run-id", RUN, "--run-attempt", "1"]
            with contextlib.redirect_stdout(output), contextlib.redirect_stderr(diagnostics):
                code = validator.main(args)
            self.assertEqual(code, 1)
            self.assertEqual(json.loads(output.getvalue())["failure"], "malformed_input")
            self.assertNotIn("private-marker", output.getvalue() + diagnostics.getvalue())
            self.assertEqual(diagnostics.getvalue(), "")
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            self.assertEqual(validator.main(["--unrecognized-private-marker"]), 1)
        self.assertEqual(json.loads(output.getvalue())["failure"], "invalid_arguments")
        self.assertNotIn("private-marker", output.getvalue())

    def test_workflow_requires_tag_and_binding_gate_before_upload(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/everywhere.yml").read_text()
        self.assertEqual(workflow.count("--tag datatug-everywhere:candidate"), 2)
        gate = workflow.split("      - name: Require OCI provenance subject and tested-runtime binding\n", 1)[1].split("      - name:", 1)[0]
        self.assertNotIn("continue-on-error", gate)
        self.assertNotIn("\n        if:", gate)
        self.assertIn("docker image inspect --format '{{.Id}}' datatug-everywhere:candidate", gate)
        self.assertIn('--source-sha "$SOURCE_REVISION"', gate)
        self.assertIn('--run-id "$GITHUB_RUN_ID" --run-attempt "$GITHUB_RUN_ATTEMPT"', gate)
        self.assertLess(workflow.index("Require OCI provenance subject and tested-runtime binding"), workflow.index("Retain the private candidate only after all audit gates pass"))
        self.assertIn("test_verify_provenance.py", workflow)
        self.assertIn("/image-evidence/provenance-binding.json", workflow)


if __name__ == "__main__":
    unittest.main()
