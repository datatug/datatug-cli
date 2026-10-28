#!/usr/bin/env python3
"""Fail closed on AG Grid/Charts markers in every layer of a docker-save archive.

This is an artifact inspection control, not a legal opinion or a substitute for
SBOM/dependency review. No archive entry is extracted to the host filesystem.
"""
import argparse
import json
import re
import tarfile

FORBIDDEN = re.compile(rb'ag[-_]grid|ag[-_]charts|@ag-grid/|@ag-charts/|agGrid\.|agCharts\.', re.I)


def audit(path):
    findings = []
    file_count = 0
    with tarfile.open(path, 'r:*') as archive:
        manifest_file = archive.extractfile('manifest.json')
        if manifest_file is None:
            raise ValueError('docker-save manifest missing')
        manifest = json.load(manifest_file)
        layers = sorted({layer for image in manifest for layer in image['Layers']})
        if not layers:
            raise ValueError('image has no inspectable layers')
        for layer_name in layers:
            layer_file = archive.extractfile(layer_name)
            if layer_file is None:
                raise ValueError('image layer missing')
            with tarfile.open(fileobj=layer_file, mode='r|*') as layer:
                for member in layer:
                    if FORBIDDEN.search((member.name+' '+member.linkname).encode()):
                        findings.append({'layer': layer_name, 'path': member.name, 'kind': 'restricted-name'})
                    if not member.isfile():
                        continue
                    file_count += 1
                    source = layer.extractfile(member)
                    if source is None:
                        raise ValueError('file payload missing')
                    previous = b''
                    while True:
                        chunk = source.read(1024 * 1024)
                        if not chunk:
                            break
                        data = previous+chunk
                        if FORBIDDEN.search(data):
                            findings.append({'layer': layer_name, 'path': member.name, 'kind': 'restricted-content'})
                            break
                        previous = data[-128:]
    return {'schemaVersion': 1, 'layersInspected': len(layers), 'filesInspected': file_count,
            'restrictedAssetFindings': findings,
            'scope': 'all layer names and uncompressed regular-file bytes; dependency/SBOM and contractual review remain separate'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive')
    args = parser.parse_args()
    result = audit(args.archive)
    print(json.dumps(result, indent=2))
    return 1 if result['restrictedAssetFindings'] else 0


if __name__ == '__main__':
    raise SystemExit(main())
