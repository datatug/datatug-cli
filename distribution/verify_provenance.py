#!/usr/bin/env python3
"""Check unsigned local OCI binding without extraction, execution or network.

Caps: 1 GiB raw OCI tar, 4096 entries, 512 MiB/blob, 2 MiB/JSON,
128 descriptors, four nested indexes, JSON depth64/100000values.
Referenced layers are streamed as bytes, never parsed as JSON. This proves no
signature, builder identity, complete dependency census or SLSA level.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile

INDEX = "application/vnd.oci.image.index.v1+json"
MANIFEST = "application/vnd.oci.image.manifest.v1+json"
CONFIG = "application/vnd.oci.image.config.v1+json"
EMPTY_CONFIG = "application/vnd.oci.empty.v1+json"
STATEMENT = "application/vnd.in-toto+json"
PREDICATE = "https://slsa.dev/provenance/v1"
BUILD_TYPE = "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md"
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
SHA = re.compile(r"[0-9a-f]{40}\Z")
MAX_TAR, MAX_ENTRIES, MAX_BLOB = 1024**3, 4096, 512 * 1024**2
MAX_JSON, MAX_DESCRIPTORS, MAX_DEPTH = 2 * 1024**2, 128, 4


class InvalidProvenance(ValueError):
    """Only fixed failure codes reach CLI output."""


def require(condition, code):
    if not condition:
        raise InvalidProvenance(code)


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate_json_key")
        result[key] = value
    return result


def invalid_number(_value):
    raise InvalidProvenance("invalid_json_number")


def parse_json(data):
    require(len(data) <= MAX_JSON, "json_size_limit")
    try:
        value = json.loads(data.decode("utf-8"), object_pairs_hook=object_pairs, parse_constant=invalid_number)
    except (UnicodeError, json.JSONDecodeError, RecursionError) as error:
        raise InvalidProvenance("invalid_json") from error
    pending, count = [(value, 0)], 0
    while pending:
        item, depth = pending.pop()
        count += 1
        require(depth <= 64 and count <= 100000, "json_structure_limit")
        require(not isinstance(item, float) or math.isfinite(item), "invalid_json_number")
        if isinstance(item, dict):
            pending.extend((v, depth + 1) for v in item.values())
        elif isinstance(item, list):
            pending.extend((v, depth + 1) for v in item)
    require(isinstance(value, dict), "json_object_required")
    return value


def read_json(path):
    require(not path.is_symlink() and path.is_file(), "input_not_regular")
    require(path.stat().st_size <= MAX_JSON, "json_size_limit")
    with path.open("rb") as stream:
        return parse_json(stream.read(MAX_JSON + 1))


class OCITarInfo(tarfile.TarInfo):
    def _proc_member(self, archive):
        # Reject extension/sparse/link headers before tarfile expands metadata.
        # OCI blob names fit plain headers; no extended-header parser is needed.
        require(self.type in (tarfile.REGTYPE, tarfile.AREGTYPE, tarfile.DIRTYPE), "unsafe_archive_type")
        return super()._proc_member(archive)


class Archive:
    def __init__(self, path):
        require(not path.is_symlink() and path.is_file(), "input_not_regular")
        require(path.stat().st_size <= MAX_TAR, "archive_size_limit")
        self.tar = tarfile.open(path, mode="r:", tarinfo=OCITarInfo)
        self.entries, self.verified = {}, set()
        total = 0
        try:
            for entry in self.tar:
                name = entry.name.rstrip("/") if entry.isdir() else entry.name
                require(name and not name.startswith("/") and "\\" not in name
                        and ".." not in PurePosixPath(name).parts
                        and str(PurePosixPath(name)) == name, "unsafe_archive_path")
                require(name not in self.entries, "duplicate_archive_entry")
                require(entry.type in (tarfile.REGTYPE, tarfile.AREGTYPE, tarfile.DIRTYPE), "unsafe_archive_type")
                require(0 <= entry.size <= MAX_BLOB and (entry.isfile() or entry.size == 0), "blob_size_limit")
                allowed_dir = name in ("blobs", "blobs/sha256")
                allowed_file = name in ("index.json", "oci-layout") or re.fullmatch(r"blobs/sha256/[0-9a-f]{64}", name)
                require(allowed_dir if entry.isdir() else allowed_file, "unsupported_archive_entry")
                self.entries[name] = entry
                total += entry.size
                require(len(self.entries) <= MAX_ENTRIES and total <= MAX_TAR, "archive_structure_limit")
        except BaseException:
            self.tar.close()
            raise

    def top(self, name):
        require(name in self.entries and self.entries[name].isfile(), "missing_layout_file")
        entry = self.entries[name]
        require(entry.size <= MAX_JSON, "json_size_limit")
        return parse_json(self.tar.extractfile(entry).read(MAX_JSON + 1))

    def blob(self, descriptor, *, document=False):
        require(isinstance(descriptor, dict), "invalid_descriptor")
        digest, size, media = descriptor.get("digest"), descriptor.get("size"), descriptor.get("mediaType")
        require(isinstance(digest, str) and DIGEST.fullmatch(digest), "invalid_descriptor_digest")
        require(type(size) is int and 0 < size <= MAX_BLOB and isinstance(media, str) and 0 < len(media) <= 128, "invalid_descriptor")
        name = "blobs/sha256/" + digest[7:]
        require(name in self.entries and self.entries[name].isfile(), "missing_blob")
        entry = self.entries[name]
        require(entry.size == size, "descriptor_size_mismatch")
        if document:
            require(size <= MAX_JSON, "json_size_limit")
        if digest not in self.verified or document:
            hasher, chunks, count = hashlib.sha256(), [], 0
            with self.tar.extractfile(entry) as stream:
                while chunk := stream.read(1024 * 1024):
                    count += len(chunk)
                    require(count <= size, "descriptor_size_mismatch")
                    hasher.update(chunk)
                    if document:
                        chunks.append(chunk)
            require(count == size and "sha256:" + hasher.hexdigest() == digest, "blob_digest_mismatch")
            self.verified.add(digest)
            if document:
                return parse_json(b"".join(chunks))
        return None

    def document(self, descriptor, media):
        require(isinstance(descriptor, dict) and descriptor.get("mediaType") == media, "unexpected_media_type")
        return self.blob(descriptor, document=True)


def verify(oci, metadata, arch, source_sha, runtime_config_id, run_id, run_attempt):
    require(arch in ("amd64", "arm64") and isinstance(source_sha, str) and SHA.fullmatch(source_sha), "invalid_expectation")
    require(isinstance(runtime_config_id, str) and DIGEST.fullmatch(runtime_config_id), "invalid_expectation")
    require(all(isinstance(x, str) and re.fullmatch(r"[1-9][0-9]{0,19}", x) for x in (run_id, run_attempt)), "invalid_expectation")
    meta = read_json(Path(metadata))
    metadata_digest = meta.get("containerimage.digest")
    require(isinstance(metadata_digest, str) and DIGEST.fullmatch(metadata_digest), "invalid_metadata_digest")
    archive = Archive(Path(oci))
    try:
        require(archive.top("oci-layout").get("imageLayoutVersion") == "1.0.0", "invalid_oci_layout")
        root = archive.top("index.json")
        root_entry = archive.entries["index.json"]
        root_digest = "sha256:" + hashlib.sha256(archive.tar.extractfile(root_entry).read(MAX_JSON + 1)).hexdigest()
        indexes, active, seen, descriptors = {}, set(), set(), [0]

        def walk(document, digest, depth):
            require(depth <= MAX_DEPTH and digest not in active, "index_cycle_or_depth")
            require(digest not in seen, "ambiguous_index")
            active.add(digest)
            seen.add(digest)
            require(type(document.get("schemaVersion")) is int and document["schemaVersion"] == 2
                    and document.get("mediaType", INDEX) == INDEX, "invalid_index")
            children = document.get("manifests")
            require(isinstance(children, list) and 0 < len(children) <= MAX_DESCRIPTORS, "invalid_index")
            leaves = []
            for child in children:
                descriptors[0] += 1
                require(descriptors[0] <= MAX_DESCRIPTORS and isinstance(child, dict), "descriptor_limit")
                if child.get("mediaType") == INDEX:
                    nested = archive.document(child, INDEX)
                    leaves.extend(walk(nested, child["digest"], depth + 1))
                else:
                    require(child.get("mediaType") == MANIFEST, "unexpected_media_type")
                    manifest = archive.document(child, MANIFEST)
                    require(type(manifest.get("schemaVersion")) is int and manifest["schemaVersion"] == 2, "invalid_manifest")
                    config_descriptor = manifest.get("config")
                    if isinstance(config_descriptor, dict) and config_descriptor.get("mediaType") == EMPTY_CONFIG:
                        require(isinstance(child.get("annotations"), dict)
                                and child["annotations"].get("vnd.docker.reference.type") == "attestation-manifest", "unexpected_media_type")
                        require(archive.document(config_descriptor, EMPTY_CONFIG) == {}, "invalid_empty_config")
                    else:
                        archive.document(config_descriptor, CONFIG)
                    layers = manifest.get("layers")
                    require(isinstance(layers, list) and 0 < len(layers) <= MAX_DESCRIPTORS, "invalid_layers")
                    for layer in layers:
                        archive.blob(layer)
                    leaves.append((child, manifest))
            active.remove(digest)
            indexes[digest] = leaves
            return leaves

        walk(root, root_digest, 0)
        require(metadata_digest in indexes, "metadata_index_not_reachable")
        leaves = indexes[metadata_digest]
        for descriptor, _ in leaves:
            require(isinstance(descriptor.get("platform"), dict), "invalid_platform")
        targets = [(d, m) for d, m in leaves if d["platform"].get("os") == "linux" and d["platform"].get("architecture") == arch]
        require(len(targets) == 1, "ambiguous_platform")
        target, manifest = targets[0]
        require(type(manifest.get("schemaVersion")) is int and manifest["schemaVersion"] == 2, "invalid_manifest")
        config_descriptor = manifest.get("config")
        config = archive.document(config_descriptor, CONFIG)
        require(config_descriptor["digest"] == runtime_config_id, "runtime_config_mismatch")
        require(config.get("os") == "linux" and config.get("architecture") == arch, "config_platform_mismatch")
        require(isinstance(config.get("config"), dict), "invalid_config")
        labels = config["config"].get("Labels")
        require(isinstance(labels, dict) and labels.get("org.opencontainers.image.revision") == source_sha
                and labels.get("io.datatug.source-state") == "clean"
                and labels.get("io.datatug.source-status-digest") == "none", "source_labels_mismatch")
        require(meta.get("containerimage.config.digest", runtime_config_id) == runtime_config_id, "metadata_config_mismatch")
        if "containerimage.descriptor" in meta:
            descriptor = meta["containerimage.descriptor"]
            require(isinstance(descriptor, dict) and descriptor.get("digest") == metadata_digest, "metadata_descriptor_mismatch")
            if metadata_digest == root_digest:
                require(descriptor.get("mediaType") == INDEX and type(descriptor.get("size")) is int
                        and descriptor["size"] == root_entry.size, "metadata_descriptor_mismatch")
            else:
                archive.document(descriptor, INDEX)
        layers = manifest.get("layers")
        require(isinstance(layers, list) and 0 < len(layers) <= MAX_DESCRIPTORS, "invalid_layers")
        for layer in layers:
            archive.blob(layer)
        candidates = []
        for descriptor, document in leaves:
            annotations = descriptor.get("annotations", {})
            require(isinstance(annotations, dict), "invalid_annotations")
            if annotations.get("vnd.docker.reference.digest") == target["digest"]:
                require(annotations.get("vnd.docker.reference.type") == "attestation-manifest", "invalid_attestation_reference")
                candidates.append((descriptor, document))
            elif annotations.get("vnd.docker.reference.type") == "attestation-manifest":
                require(any(d["digest"] == annotations.get("vnd.docker.reference.digest")
                            and d["platform"].get("os") == "linux" for d, _ in leaves), "foreign_attestation_reference")
        require(len(candidates) == 1, "ambiguous_attestation")
        attestation, att_manifest = candidates[0]
        require(type(att_manifest.get("schemaVersion")) is int and att_manifest["schemaVersion"] == 2, "invalid_manifest")
        att_config = att_manifest.get("config")
        if isinstance(att_config, dict) and att_config.get("mediaType") == EMPTY_CONFIG:
            require(archive.document(att_config, EMPTY_CONFIG) == {}, "invalid_empty_config")
        else:
            archive.document(att_config, CONFIG)
        statements = att_manifest.get("layers")
        require(isinstance(statements, list) and len(statements) == 1, "ambiguous_statement")
        statement_descriptor = statements[0]
        statement = archive.document(statement_descriptor, STATEMENT)
        require(isinstance(statement_descriptor.get("annotations"), dict), "invalid_annotations")
        require(statement_descriptor["annotations"].get("in-toto.io/predicate-type") == PREDICATE
                and statement.get("_type") == "https://in-toto.io/Statement/v1"
                and statement.get("predicateType") == PREDICATE, "invalid_statement_type")
        subjects = statement.get("subject")
        require(isinstance(subjects, list) and len(subjects) > 0, "empty_statement_subject")
        require(len(subjects) == 1, "ambiguous_statement_subject")
        subject = subjects[0]
        require(isinstance(subject, dict) and isinstance(subject.get("name"), str) and 0 < len(subject["name"]) <= 512
                and isinstance(subject.get("digest"), dict) and set(subject["digest"]) == {"sha256"}
                and isinstance(subject["digest"]["sha256"], str)
                and DIGEST.fullmatch("sha256:" + subject["digest"]["sha256"]), "invalid_statement_subject")
        require("sha256:" + subject["digest"]["sha256"] == target["digest"], "statement_subject_mismatch")
        predicate = statement.get("predicate")
        require(isinstance(predicate, dict), "invalid_predicate")
        definition = predicate.get("buildDefinition", {})
        require(isinstance(definition, dict) and definition.get("buildType") == BUILD_TYPE, "invalid_build_type")
        internal = definition.get("internalParameters")
        expected = {"github_repository": "datatug/datatug-cli", "github_job": "container", "github_workflow_sha": source_sha,
                    "github_run_id": run_id, "github_run_attempt": run_attempt}
        require(isinstance(internal, dict) and all(internal.get(k) == v for k, v in expected.items()), "provenance_source_run_mismatch")
        return {"schemaVersion": "datatug.provenance.binding/v1", "accepted": True, "architecture": arch,
                "sourceRevision": source_sha, "imageIndexDigest": metadata_digest, "platformManifestDigest": target["digest"],
                "runtimeConfigDigest": runtime_config_id, "attestationManifestDigest": attestation["digest"],
                "statementBlobDigest": statement_descriptor["digest"], "subjectDigest": target["digest"],
                "verifiedBlobs": len(archive.verified), "archiveEntries": len(archive.entries),
                "signatureVerified": False, "builderIdentityVerified": False, "completeDependencyCensus": False, "slsaLevelClaimed": False}
    finally:
        archive.tar.close()


class Parser(argparse.ArgumentParser):
    def error(self, _message):
        raise InvalidProvenance("invalid_arguments")


def main(argv=None):
    try:
        parser = Parser(description=__doc__)
        for name in ("oci", "metadata", "arch", "source-sha", "runtime-config-id", "run-id", "run-attempt"):
            parser.add_argument("--" + name, required=True)
        args = parser.parse_args(argv)
        result = verify(args.oci, args.metadata, args.arch, args.source_sha, args.runtime_config_id, args.run_id, args.run_attempt)
    except InvalidProvenance as error:
        print(json.dumps({"schemaVersion": "datatug.provenance.binding/v1", "accepted": False, "failure": str(error)}))
        return 1
    except (OSError, ValueError, TypeError, KeyError, AttributeError, tarfile.TarError, RecursionError):
        print(json.dumps({"schemaVersion": "datatug.provenance.binding/v1", "accepted": False, "failure": "malformed_input"}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
