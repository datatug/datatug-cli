"""Narrow, checked image indirection over the single common runtime chart."""
import json

import yaml

REGISTRY = r"^[a-z0-9]+([.-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$"
IMAGE = r"^[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*$"


def replace_once(text, old, new):
    if text.count(old) != 1:
        raise ValueError("Common image anchor changed; review provider adaptation")
    return text.replace(old, new)


def image_values(image):
    registry, name = image["repository"].split("/", 1)
    return {"registry": registry, "image": name, "digest": image["digest"]}


def prepare(chart, common_values):
    template = chart / "templates" / "runner.yaml"
    content = template.read_text(encoding="utf-8")
    content = replace_once(content, '{{- $repository := required "image.repository is required" .Values.image.repository -}}',
                           '{{- $registry := required "global.azure.images.runner.registry is required" .Values.global.azure.images.runner.registry -}}\n'
                           '{{- $image := required "global.azure.images.runner.image is required" .Values.global.azure.images.runner.image -}}\n'
                           '{{- $repository := printf "%s/%s" $registry $image -}}')
    content = replace_once(content, '.Values.image.digest', '.Values.global.azure.images.runner.digest')
    template.write_text(content, encoding="utf-8")
    values = yaml.safe_load((chart / "values.yaml").read_text(encoding="utf-8"))
    del values["image"]
    values["global"] = {"azure": {"images": {"runner": image_values(common_values["image"])}}}
    (chart / "values.yaml").write_text(json.dumps(values, indent=2) + "\n", encoding="utf-8")
    schema_path = chart / "values.schema.json"
    schema = json.loads(schema_path.read_text(encoding="utf-8"))
    del schema["properties"]["image"]
    schema["required"].remove("image")
    schema["required"].append("global")
    runner = {"type": "object", "additionalProperties": False, "required": ["registry", "image", "digest"],
              "properties": {"registry": {"type": "string", "pattern": REGISTRY},
                             "image": {"type": "string", "pattern": IMAGE},
                             "digest": {"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"}}}
    schema["properties"]["global"] = {"type": "object", "required": ["azure"], "properties": {
        "azure": {"type": "object", "required": ["images"], "properties": {
            "images": {"type": "object", "additionalProperties": False, "required": ["runner"], "properties": {"runner": runner}}
        }}
    }}
    schema_path.write_text(json.dumps(schema, indent=2) + "\n", encoding="utf-8")
    operator_values = json.loads(json.dumps(common_values))
    del operator_values["image"]
    operator_values["global"] = values["global"]
    return operator_values
