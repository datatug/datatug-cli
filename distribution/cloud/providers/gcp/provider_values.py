"""Strict scalar JSON -> common Helm arrays, used only by the deployer."""
import ipaddress
import json
import re

if __package__:
    from .bootstrap_inputs import dns
else:
    from datatug_bootstrap_inputs import dns


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate JSON property")
        result[key] = value
    return result


def array(text):
    if not isinstance(text, str) or len(text.encode()) > 65536:
        raise ValueError("Bounded JSON string required")
    value = json.loads(text, object_pairs_hook=unique_object)
    if not isinstance(value, list) or len(value) > 50:
        raise ValueError("Common array limit exceeded")
    return value


def expand(values):
    try:
        name, namespace = dns(values.pop("name"), 51), dns(values.pop("namespace"))
    except (KeyError, TypeError):
        raise ValueError("Required provider names are missing or malformed") from None
    if values.get("scope", {}).get("namespace") != namespace:
        raise ValueError("Provider and runtime namespace differ")
    if dns(values.get("serviceAccount")) == name + "-deployer-sa":
        raise ValueError("Runtime and disposable deployer accounts must differ")
    helpers = values.pop("marketplaceInputs", {})
    if not isinstance(helpers, dict):
        raise ValueError("Provider helper inputs must be an object")
    if set(helpers) - {"sourceCredentialsJson", "databaseEgressJson"}:
        raise ValueError("Unknown provider input")
    credentials = array(helpers.get("sourceCredentialsJson", "[]"))
    seen = set()
    for item in credentials:
        if not isinstance(item, dict) or set(item) != {"envName", "secretName", "key"}:
            raise ValueError("Only existing source Secret references allowed")
        env, secret, key = item["envName"], item["secretName"], item["key"]
        if not all(isinstance(v, str) for v in (env, secret, key)):
            raise ValueError("Secret reference fields must be strings")
        if env in seen or not re.fullmatch(r"PGPASSWORD|[A-Z][A-Z0-9_]*_URL", env) or re.match(r"^(DATATUG|NODE|LD_|DYLD_|VSCODE|ELECTRON|GIT|SSH)", env):
            raise ValueError("Duplicate/reserved/unsafe source environment name")
        if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*", secret) or len(secret) > 253:
            raise ValueError("Invalid existing Secret name")
        if not re.fullmatch(r"[A-Za-z0-9._-]{1,253}", key):
            raise ValueError("Invalid existing Secret key")
        seen.add(env)
    egress = array(helpers.get("databaseEgressJson", "[]"))
    seen_rules = set()
    for item in egress:
        if not isinstance(item, dict) or set(item) != {"cidr", "port"}:
            raise ValueError("Only explicit database CIDR/port rules allowed")
        if not isinstance(item["cidr"], str) or not isinstance(item["port"], int) or isinstance(item["port"], bool) or not 1 <= item["port"] <= 65535:
            raise ValueError("Invalid database port or CIDR")
        network = ipaddress.ip_network(item["cidr"], strict=True)
        if network.version != 4:
            raise ValueError("Common chart requires IPv4 database CIDRs")
        identity = (str(network), item["port"])
        if identity in seen_rules:
            raise ValueError("Duplicate database egress rule")
        seen_rules.add(identity)
    values["sourceCredentials"] = credentials
    values.setdefault("networkPolicy", {})["databaseEgress"] = egress
    return values
