#!/usr/bin/env python3
"""Build a source-free, auditable Docker context for the DataTug CLI image."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
CORE_MODULE = "github.com/datatug/datatug-core"
CORE_VERSION = "v0.80.0"
ARCHITECTURES = ("amd64", "arm64")
GO_TOOLCHAIN = "go1.27.3"
DEFAULT_VERSION = "0.0.0-candidate"
SEMVER = re.compile(
    r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
    r"(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
)
LICENSE_NAMES = {
    "license",
    "license.md",
    "license.txt",
    "licence",
    "licence.md",
    "licence.txt",
    "copying",
    "copying.md",
    "copying.txt",
    "notice",
    "notice.md",
    "notice.txt",
    "copyright",
}


def run(command: list[str], *, cwd: Path, env: dict[str, str]) -> str:
    result = subprocess.run(command, cwd=cwd, env=env, check=False, text=True, capture_output=True)
    if result.returncode:
        sys.stderr.write(result.stderr)
        raise RuntimeError(f"command failed ({result.returncode}): {' '.join(command)}")
    return result.stdout


def module_inventory(env: dict[str, str]) -> dict[str, dict[str, str]]:
    modules: dict[str, dict[str, str]] = {}
    decoder = json.JSONDecoder()
    for arch in ARCHITECTURES:
        target = {**env, "GOOS":"linux", "GOARCH":arch, "CGO_ENABLED":"0"}
        raw = run(["go", "list", "-mod=readonly", "-deps", "-json", "-tags=netgo,osusergo", ".", "./distribution/entrypoint"], cwd=ROOT, env=target)
        offset = 0
        while offset < len(raw):
            while offset < len(raw) and raw[offset].isspace():
                offset += 1
            if offset >= len(raw):
                break
            package, offset = decoder.raw_decode(raw, offset)
            module = package.get("Module") or {}
            path = module.get("Path")
            if path:
                modules[path] = {"version":module.get("Version", ""), "dir":module.get("Dir", "")}
    modules.pop("github.com/datatug/datatug-cli", None)
    return dict(sorted(modules.items()))


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def license_files(module_dir: Path) -> list[Path]:
    if not module_dir.is_dir():
        return []
    return sorted(
        path for path in module_dir.iterdir()
        if path.is_file() and not path.is_symlink() and path.name.lower() in LICENSE_NAMES
    )


def safe_name(value: str) -> str:
    return "_".join(part for part in value.replace("@", "_").replace("/", "_").split("_") if part)


def write_module_notices(context: Path, modules: dict[str, dict[str, str]]) -> list[str]:
    target = context / "licenses" / "modules"
    target.mkdir(parents=True)
    inventory: list[str] = [
        "# Go module license and notice inventory",
        "",
        "Generated from the union of both Linux target architectures with CGO disabled, for the CLI and entrypoint; read-only module graph.",
        "Each listed file is copied into the final image under `/usr/share/licenses/datatug/`.",
        "A missing notice is a publication gate; this development build does not interpret license terms.",
        "",
        "| Module | Version | Included notice files |",
        "|---|---|---|",
    ]
    missing: list[str] = []
    for module, details in modules.items():
        version = details["version"] or "(unknown)"
        source_dir = Path(details["dir"])
        files = license_files(source_dir)
        module_target = target / f"{safe_name(module)}@{safe_name(version)}"
        included: list[str] = []
        if files:
            module_target.mkdir()
            for source in files:
                destination = module_target / source.name
                shutil.copyfile(source, destination)
                destination.chmod(0o444)
                included.append(f"modules/{module_target.name}/{source.name}")
        else:
            missing.append(f"{module}@{version}")
        notice_text = ", ".join(f"`{name}`" for name in included) if included else "**MISSING**"
        inventory.append(f"| `{module}` | `{version}` | {notice_text} |")

    if missing:
        inventory.extend(["", "## Publication gate: missing license or notice files", ""])
        inventory.extend(f"- `{item}`" for item in missing)
    else:
        inventory.extend(["", "No missing top-level license or notice files were found."])
    inventory.append("")
    (context / "licenses" / "DEPENDENCY-LICENSE-INVENTORY.md").write_text("\n".join(inventory), encoding="utf-8")
    return missing


# Closed own-source notice/provenance map; no recursive source-tree copy.
PROJECT_NOTICES = (
    ("LICENSE", "PROJECT-LICENSE", "Repository default license; scoped exceptions are in LICENSING.md"),
    ("THIRD_PARTY_NOTICES.md", "PROJECT-THIRD_PARTY_NOTICES.md", "Third-party attributions, including embedded MCP App notices"),
    ("LICENSING.md", "PROJECT-LICENSING.md", "Own-source licensing scope map; not aggregate binary permission"),
    ("LICENSES/Apache-2.0.txt", "PROJECT-LICENSES/Apache-2.0.txt", "Apache-2.0 own-source scopes named by LICENSING.md"),
    ("LICENSES/COMMERCIAL-TERMS.md", "PROJECT-LICENSES/COMMERCIAL-TERMS.md", "commercial/ scope; draft terms pending qualified legal review"),
    ("pkg/mcpserver/app/build-manifest.json", "PROJECT-MCP-APP-BUILD-MANIFEST.json", "Embedded MCP App build provenance; notices retained in PROJECT-THIRD_PARTY_NOTICES.md"),
)


def copy_project_notices(context: Path, env: dict[str, str]) -> None:
    target = context / "licenses"
    retained = []
    # Validate all fixed inputs before copying. Symlinked ancestors are rejected
    # as well as symlink files so the closed map cannot read outside the source.
    for relative, destination, scope in PROJECT_NOTICES:
        source = ROOT / relative
        try:
            if any((ROOT.joinpath(*Path(relative).parts[:i])).is_symlink()
                   for i in range(1, len(Path(relative).parts) + 1)) or not source.is_file():
                raise ValueError("not a regular source file")
            if not 0 < source.stat().st_size <= 4 * 1024 * 1024:
                raise ValueError("empty or oversized source text")
            content = source.read_bytes()
            if not content.decode("utf-8").strip() or b"\0" in content:
                raise ValueError("unsuitable source text")
        except (OSError, ValueError) as error:
            raise RuntimeError(f"required project notice/provenance file unavailable or unsuitable: {relative}") from error
        retained.append((relative, destination, scope, content))
    inventory = []
    for relative, name, scope, content in retained:
        destination = target / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(content)
        destination.chmod(0o444)
        inventory.append({"source": relative, "destination": "licenses/" + name,
                          "scope": scope, "sha256": hashlib.sha256(content).hexdigest()})
    inventory_path = target / "PROJECT-LICENSE-INVENTORY.json"
    inventory_path.write_text(json.dumps({"schemaVersion": 1, "files": inventory}, indent=2) + "\n", encoding="utf-8")
    inventory_path.chmod(0o444)
    goroot = Path(run(["go", "env", "GOROOT"], cwd=ROOT, env=env).strip())
    shutil.copyfile(goroot / "LICENSE", target / "GO-LICENSE")
    # Include notices of standard-library vendored dependencies without sources.
    for source in sorted((goroot / "src" / "vendor").rglob("LICENSE*")):
        if source.is_file() and not source.is_symlink():
            name = safe_name(str(source.relative_to(goroot / "src" / "vendor")))
            shutil.copyfile(source, target / ("GO-VENDOR-" + name))


def make_state_dirs(context: Path) -> None:
    base = context / "rootfs" / "var" / "lib" / "datatug"
    for name in ("coordinator", "evidence", "state"):
        directory = base / name
        directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        directory.chmod(0o700)
        marker = directory / ".keep"
        marker.write_text("", encoding="utf-8")
        marker.chmod(0o600)


def source_provenance(env: dict[str, str]) -> tuple[str, str, str]:
	"""Return commit, clean/dirty state, and a path-free status digest."""
	revision = run(["git", "rev-parse", "--verify", "HEAD"], cwd=ROOT, env=env).strip()
	status = run(["git", "status", "--porcelain", "--untracked-files=all"], cwd=ROOT, env=env)
	state = "dirty (working-copy content included)" if status else "clean"
	status_digest = hashlib.sha256(status.encode("utf-8")).hexdigest() if status else "none"
	return revision, state, status_digest


def validate_version(value: str) -> str:
    """Accept only SemVer characters safe in the Go linker argument grammar."""
    if SEMVER.fullmatch(value) is None:
        raise ValueError("version must be strict SemVer without a v prefix (for example 1.2.3 or 1.2.3-rc.1)")
    return value


def build_context(output: Path, env: dict[str, str], version: str = DEFAULT_VERSION) -> tuple[list[str], int]:
    version = validate_version(version)
    revision, working_tree_state, working_tree_status_sha = source_provenance(env)
    # A stable release must not identify uncommitted content as a release commit.
    # Check before dependency resolution or either architecture's compilation.
    if SEMVER.fullmatch(version).group(4) is None and working_tree_status_sha != "none":
        raise RuntimeError("stable release version requires a clean source working tree")
    modules = module_inventory(env)
    source_date = run(["git", "show", "-s", "--format=%cI", "HEAD"], cwd=ROOT, env=env).strip()
    core = modules.get(CORE_MODULE)
    if core is None or core["version"] != CORE_VERSION:
        actual = core["version"] if core else "not present in dependency graph"
        raise RuntimeError(f"expected {CORE_MODULE} {CORE_VERSION} in dependency graph; found {actual}")

    with tempfile.TemporaryDirectory(prefix="datatug-docker-context-") as staging_dir:
        context = Path(staging_dir)
        (context / "bin").mkdir()
        for arch in ARCHITECTURES:
            bin_dir = context / "bin" / arch
            bin_dir.mkdir(parents=True)
            target_env = {**env, "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"}
            ldflags = ("-s -w -buildid= "
                       f"-X github.com/strongo/buildinfo.version={version} "
                       f"-X github.com/strongo/buildinfo.commit={revision} "
                       f"-X github.com/strongo/buildinfo.date={source_date}")
            common = ["go", "build", "-mod=readonly", "-trimpath", "-tags=netgo,osusergo", "-buildvcs=false", "-ldflags="+ldflags, "-o"]
            run([*common, str(bin_dir / "datatug"), "."], cwd=ROOT, env=target_env)
            run([*common, str(bin_dir / "datatug-entrypoint"), "./distribution/entrypoint"], cwd=ROOT, env=target_env)
            for binary in bin_dir.iterdir():
                binary.chmod(0o555)

        dockerfile = (ROOT / "distribution" / "Dockerfile").read_text()
        dockerfile = dockerfile.replace("ARG DATATUG_SOURCE_REVISION=unknown", f"ARG DATATUG_SOURCE_REVISION={revision}")
        dockerfile = dockerfile.replace("ARG DATATUG_SOURCE_STATE=unknown", "ARG DATATUG_SOURCE_STATE="+("dirty" if working_tree_status_sha != "none" else "clean"))
        dockerfile = dockerfile.replace("ARG DATATUG_SOURCE_STATUS_DIGEST=unknown", f"ARG DATATUG_SOURCE_STATUS_DIGEST={working_tree_status_sha}")
        (context / "Dockerfile").write_text(dockerfile)
        make_state_dirs(context)
        missing = write_module_notices(context, modules)
        copy_project_notices(context, env)

        inventory = [
            "# Generated Docker context inventory",
            "",
            "This context contains only the pinned Dockerfile, static linux/amd64 and linux/arm64 executables, private state-directory markers, and license/notice/provenance texts.",
            "Go source, module caches, project fixtures, credentials and local configuration are excluded.",
            f"Binary version: `{version}`",
            f"Source commit: `{revision}`",
            f"Source working tree: **{working_tree_state}**",
            f"Source working-tree status digest (paths omitted): `{working_tree_status_sha}`",
            "",
            "| Path | SHA-256 or mode |",
            "|---|---|",
        ]
        for path in sorted(context.rglob("*")):
            if path.is_file() and path.name != "CONTEXT-INVENTORY.md":
                value = oct(path.stat().st_mode & 0o777) if path.name == ".keep" else sha256(path)
                inventory.append(f"| `{path.relative_to(context)}` | `{value}` |")
        (context / "CONTEXT-INVENTORY.md").write_text("\n".join(inventory) + "\n", encoding="utf-8")
        shutil.copytree(context, output, dirs_exist_ok=False)
    return missing, len(modules)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path, help="new context directory outside this repository")
    parser.add_argument("--version", type=validate_version, default=DEFAULT_VERSION,
                        help="binary SemVer (default: %(default)s); stable versions require a clean source tree")
    parser.add_argument("--fail-on-missing-licenses", action="store_true", help="return nonzero if any used module has no recognized top-level notice file")
    args = parser.parse_args()
    output = args.output.expanduser().resolve()
    if output == ROOT or ROOT in output.parents:
        parser.error("--output must be outside the repository so generated context cannot become source input")
    if output.exists():
        parser.error(f"--output already exists: {output}")

    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": GO_TOOLCHAIN, "GOWORK": "off", "GOFLAGS": "", "DATATUG_TELEMETRY": "0"})
    try:
        missing, module_count = build_context(output, env, args.version)
    except (OSError, RuntimeError) as exc:
        print(f"Docker context generation failed: {exc}", file=sys.stderr)
        return 1

    print(f"Docker context generated: {output}")
    print("Architectures: linux/amd64, linux/arm64; CGO_ENABLED=0")
    print(f"Go dependency modules inventoried: {module_count}")
    if missing:
        print("PUBLICATION GATE: missing module license/notice files:")
        for item in missing:
            print(f"  - {item}")
        print(f"See {output / 'licenses' / 'DEPENDENCY-LICENSE-INVENTORY.md'}")
        if args.fail_on_missing_licenses:
            return 2
    else:
        print("No missing top-level module license/notice files found.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
