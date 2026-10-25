"""Generate CNAB input metadata; this never builds/uploads a CNAB."""
import json


def flatten(value, prefix=""):
    if isinstance(value, dict):
        for key, item in value.items():
            yield from flatten(item, f"{prefix}.{key}" if prefix else key)
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from flatten(item, f"{prefix}[{index}]")
    else:
        yield prefix, value


def parameter_name(key):
    return key.replace(".", "-").replace("[", "-").replace("]", "")


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def prepare(bundle, config):
    metadata = config["providerMetadata"]
    write_json(bundle / "manifest.yaml", {
        "applicationName": metadata["applicationName"], "publisher": metadata["publisher"],
        "description": "Outbound DataTug Check Runner; existing AKS and private block state required",
        "version": config["packageVersion"], "helmChart": "./chart",
        "clusterArmTemplate": "./mainTemplate.json", "uiDefinition": "./createUIDefinition.json",
        "registryServer": metadata["registryServer"],
        "extensionRegistrationParameters": {"defaultScope": "namespace"},
    })
    parameters = {
        "clusterResourceId": {"type": "string", "minLength": 1},
        "extensionResourceName": {"type": "string", "minLength": 1},
        "extensionVersion": {"type": "string", "minLength": 1},
    }
    elements, outputs, settings = [], {}, {}
    elements.append({"name": "clusterLookup", "type": "Microsoft.Solutions.ArmApiControl",
                     "request": {"method": "GET", "path": "[concat(subscription().id, '/resourcegroups/', resourceGroup().name, '/providers/Microsoft.ContainerService/managedClusters?api-version=2024-09-01')]"}})
    for name, definition in parameters.items():
        element = {"name": name, "type": "Microsoft.Common.TextBox", "label": name,
                   "constraints": {"required": True}}
        if name == "clusterResourceId":
            element["type"] = "Microsoft.Common.DropDown"
            element["constraints"]["allowedValues"] = "[map(steps('runner').clusterLookup.value, (item) => parse(concat('{\"label\":\"', item.name, '\",\"value\":\"', item.id, '\"}')))]"
        elements.append(element)
        outputs[name] = f"[steps('runner').{name}]"
    for index, (key, value) in enumerate(flatten(config["values"])):
        if key.startswith(("image.", "resources.", "podAnnotations.")):
            # Marketplace owns images. Resource quantities remain typed chart
            # defaults: Helm's --set parser turns a whole CPU string into int.
            continue
        name, ui_name = parameter_name(key), f"input{index}"
        if name in parameters:
            raise ValueError("Ambiguous Azure parameter mapping")
        parameters[name] = {"type": "string"}
        settings[key] = f"[parameters('{name}')]"
        element = {"name": ui_name, "type": "Microsoft.Common.TextBox", "label": key,
                   "constraints": {"required": value != ""}}
        if isinstance(value, bool):
            element.update(type="Microsoft.Common.OptionsGroup", defaultValue="No",
                           constraints={"required": True, "allowedValues": [
                               {"label": "No", "value": "false"}, {"label": "Yes", "value": "true"}]})
        elif key == "stateFilesystem":
            element.update(defaultValue="node-local-block")
        # Names/references are entered per installation; no customer config defaults.
        elements.append(element)
        outputs[name] = f"[steps('runner').{ui_name}]"
    write_json(bundle / "mainTemplate.json", {
        "$schema": "https://schema.management.azure.com/schemas/2019-04-01/deploymentTemplate.json#",
        "contentVersion": "1.0.0.0", "parameters": parameters,
        "variables": {name: "DONOTMODIFY" for name in
                      ("plan-name", "plan-publisher", "plan-offerID", "clusterExtensionTypeName")},
        "resources": [{
            "type": "Microsoft.KubernetesConfiguration/extensions", "apiVersion": "2024-11-01",
            "name": "[parameters('extensionResourceName')]", "scope": "[parameters('clusterResourceId')]",
            "dependsOn": [],
            "plan": {"name": "[variables('plan-name')]", "publisher": "[variables('plan-publisher')]",
                     "product": "[variables('plan-offerID')]"},
            "properties": {"extensionType": "[variables('clusterExtensionTypeName')]",
                           "autoUpgradeMinorVersion": False, "version": "[parameters('extensionVersion')]",
                           "scope": {"namespace": {"targetNamespace": "[parameters('scope-namespace')]"}},
                           "configurationSettings": settings, "configurationProtectedSettings": {}},
        }], "outputs": {},
    })
    write_json(bundle / "createUIDefinition.json", {
        "$schema": "https://schema.management.azure.com/schemas/0.1.2-preview/CreateUIDefinition.MultiVm.json#",
        "handler": "Microsoft.Azure.CreateUIDef", "version": "0.1.2-preview",
        "parameters": {"config": {"basics": {"location": {"visible": False},
                        "resourceGroup": {"allowExisting": True}}}, "basics": [],
                       "steps": [{"name": "runner", "label": "Existing AKS and Runner references", "elements": elements}],
                       "outputs": outputs},
    })
