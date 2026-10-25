#!/usr/bin/env python3
"""Run Google's pinned, official offline deployer-schema validator."""
import argparse
import hashlib
import pathlib
import subprocess
import sys

REVISION = "e6008fba2e5e5da014ce43782c64481b775475fd"
HASHES = {
    "validate_schema.py": "6f118bdaac4be606226be34a97c07131b76daf9c02ac7f5b5471b8d41e46a754",
    "config_helper.py": "b41208ccdf1434ad2e858f0f258d5c712fbbdf7d20f63244188658160e518f61",
    "schema_values_common.py": "48f1f749956ecf94887b217db324d56b911d59ff9ab690bfccd866b8364c8317",
}


def validate(schema, source):
    directory = source / "marketplace" / "deployer_util"
    for name, expected in HASHES.items():
        if hashlib.sha256((directory / name).read_bytes()).hexdigest() != expected:
            raise ValueError("Google offline validator source hash mismatch")
    result = subprocess.run([sys.executable, str(directory / "validate_schema.py"),
                             "--schema_file", str(schema)],
                            text=True, capture_output=True, check=False)
    if result.returncode:
        # Do not echo arbitrary operator inputs from provider diagnostics.
        raise ValueError("Official Google schema validator rejected the schema")
    return {"officialValidatorRevision": REVISION, "schemaValidation": "passed",
            "scope": "schema only; no GKE/deployer/Marketplace acceptance"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("schema", type=pathlib.Path)
    parser.add_argument("--source", required=True, type=pathlib.Path)
    args = parser.parse_args()
    try:
        print(validate(args.schema.resolve(), args.source.resolve()))
    except (ValueError, OSError):
        print("Official schema validation refused; check pinned source and inputs privately", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
