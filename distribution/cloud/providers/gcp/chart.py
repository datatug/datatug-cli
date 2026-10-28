"""Application metadata only; common Runner execution resources remain intact."""
import json

from .patch_sdk import once


def prepare(chart, config):
    runner = chart / "templates" / "runner.yaml"
    text = runner.read_text(encoding="utf-8")
    text = once(text, "  namespace: {{ .Release.Namespace }}\ndata:",
                "  namespace: {{ .Release.Namespace }}\n  labels:\n    app.kubernetes.io/instance: {{ .Release.Name }}\ndata:")
    runner.write_text(text, encoding="utf-8")
    network = chart / "templates" / "network.yaml"
    text = once(network.read_text(encoding="utf-8"), "  namespace: {{ .Release.Namespace }}\nspec:",
                "  namespace: {{ .Release.Namespace }}\n  labels:\n    app.kubernetes.io/instance: {{ .Release.Name }}\nspec:")
    network.write_text(text, encoding="utf-8")
    metadata = config["providerMetadata"]
    info = "dict " + " ".join(json.dumps(value) for value in (
        "partner_id", metadata["partnerId"], "product_id", metadata["solutionId"], "partner_name", metadata["partnerName"]))
    application = '''apiVersion: app.k8s.io/v1beta1
kind: Application
metadata:
  name: {{ .Release.Name }}
  namespace: {{ .Release.Namespace }}
  labels:
    app.kubernetes.io/instance: {{ .Release.Name }}
  annotations:
    marketplace.cloud.google.com/deploy-info: {{ INFO | toJson | quote }}
spec:
  descriptor:
    type: DataTug outbound Check Runner
    version: VERSION
    notes: "Requires an existing approved HTTPS Coordinator and scoped token. Pod readiness is not authenticated Check readiness. Retain operator-owned project/state claims and Secrets."
  selector:
    matchLabels:
      app.kubernetes.io/instance: {{ .Release.Name }}
  addOwnerRef: true
  componentKinds:
    - {group: apps, kind: Deployment}
    - {group: '', kind: ConfigMap}
    - {group: networking.k8s.io, kind: NetworkPolicy}
'''.replace("version: VERSION", "version: " + json.dumps(config["packageVersion"])).replace("{{ INFO |", "{{ " + info + " |")
    (chart / "templates" / "application.yaml").write_text(application, encoding="utf-8")
