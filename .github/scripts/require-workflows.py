#!/usr/bin/env python3
"""Fail-closed exact-commit GitHub Actions gate; GH_TOKEN stays in gh's environment."""

import argparse
import json
import math
import re
import subprocess
import sys
import time
from urllib.parse import urlencode


class GuardError(Exception):
    """A fixed, credential-safe diagnostic suitable for a public Actions log."""


class GhAPI:
    def __init__(self, deadline, now=time.monotonic, run=subprocess.run):
        self.deadline, self.now, self.run = deadline, now, run

    def __call__(self, endpoint):
        remaining = self.deadline - self.now()
        if remaining <= 0:
            raise GuardError("deadline exceeded")
        try:
            result = self.run(
                ["gh", "api", "--method", "GET", endpoint],
                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                text=True, check=False, timeout=min(30.0, remaining),
            )
        except subprocess.TimeoutExpired:
            raise GuardError("GitHub API request timed out") from None
        except (OSError, UnicodeError):
            raise GuardError("GitHub API transport unavailable") from None
        if self.now() >= self.deadline:
            raise GuardError("deadline exceeded")
        if result.returncode:
            raise GuardError("GitHub API request failed")
        try:
            value = json.loads(result.stdout)
        except (ValueError, TypeError):
            raise GuardError("malformed GitHub API response") from None
        if not isinstance(value, dict):
            raise GuardError("malformed GitHub API response")
        return value


def positive_int(value):
    return type(value) is int and value > 0


def pages(api, endpoint, key, params=None):
    """Use explicit pages, never API-provided URLs or shell interpolation."""
    entries = []
    for page in range(1, 101):
        query = dict(params or {}, per_page=100, page=page)
        response = api(endpoint + "?" + urlencode(query))
        if not isinstance(response, dict):
            raise GuardError("malformed GitHub API response")
        count, batch = response.get("total_count"), response.get(key)
        if (type(count) is not int or count < 0 or not isinstance(batch, list)
                or len(batch) > 100 or any(not isinstance(x, dict) for x in batch)):
            raise GuardError("malformed GitHub API pagination")
        entries.extend(batch)
        if len(entries) >= count:
            if len(entries) != count:
                raise GuardError("inconsistent GitHub API pagination")
            return entries
        if not batch:
            raise GuardError("incomplete GitHub API pagination")
    raise GuardError("GitHub API pagination limit exceeded")


def workflow_ids(api, repo, names):
    found = {}
    for workflow in pages(api, f"repos/{repo}/actions/workflows", "workflows"):
        name, workflow_id = workflow.get("name"), workflow.get("id")
        if not isinstance(name, str) or not positive_int(workflow_id):
            raise GuardError("malformed workflow identity")
        if name in names:
            if name in found:
                raise GuardError("ambiguous workflow name")
            found[name] = workflow_id
    return found


def latest_run(api, repo, workflow_id, sha, branch):
    runs = pages(api, f"repos/{repo}/actions/workflows/{workflow_id}/runs",
                 "workflow_runs", {"event": "push", "branch": branch, "head_sha": sha})
    matching = []
    for run in runs:
        if (not isinstance(run.get("head_sha"), str)
                or not isinstance(run.get("event"), str)
                or not isinstance(run.get("head_branch"), str)):
            raise GuardError("malformed workflow run identity")
        if (run["head_sha"] != sha or run["event"] != "push"
                or run["head_branch"] != branch):
            continue
        if (not positive_int(run.get("workflow_id"))
                or run["workflow_id"] != workflow_id
                or any(not positive_int(run.get(key))
                       for key in ("id", "run_number", "run_attempt"))):
            raise GuardError("malformed workflow run identity")
        matching.append(run)
    # Reruns update run_attempt on the existing run. A later push run has a
    # higher run_number; an earlier success must never mask that newer run.
    return max(matching, key=lambda x: (x["run_number"], x["id"], x["run_attempt"]),
               default=None)


def successful_job(api, repo, run, sha):
    # Pin the attempt: successes from earlier attempts cannot satisfy a rerun.
    endpoint = (f"repos/{repo}/actions/runs/{run['id']}/attempts/"
                f"{run['run_attempt']}/jobs")
    jobs = pages(api, endpoint, "jobs")
    passed = False
    for job in jobs:
        if (not positive_int(job.get("run_id")) or job["run_id"] != run["id"]
                or job.get("head_sha") != sha):
            raise GuardError("malformed workflow job identity")
        if not isinstance(job.get("status"), str):
            raise GuardError("malformed workflow job status")
        if job["status"] == "completed" and job.get("conclusion") == "success":
            passed = True
    return passed


def require_workflows(repo, sha, names, branch="main", timeout=1800, poll=15,
                      api=None, now=time.monotonic, sleep=time.sleep, output=print):
    deadline = now() + timeout
    api = api or GhAPI(deadline, now=now)
    last_report = None
    while now() < deadline:
        ids = workflow_ids(api, repo, names)
        pending = []
        for name in names:
            if name not in ids:
                pending.append((name, "workflow missing"))
                continue
            run = latest_run(api, repo, ids[name], sha, branch)
            if run is None:
                pending.append((name, "push run missing"))
                continue
            status, conclusion = run.get("status"), run.get("conclusion")
            if status == "completed":
                if conclusion != "success":
                    raise GuardError(f"workflow {name}: completed without success")
                if not successful_job(api, repo, run, sha):
                    raise GuardError(f"workflow {name}: no successful job in latest attempt")
            elif isinstance(status, str) and status in {
                    "queued", "in_progress", "requested", "waiting", "pending"}:
                if conclusion is not None:
                    raise GuardError(f"workflow {name}: inconsistent run status")
                pending.append((name, status))
            else:
                raise GuardError(f"workflow {name}: unrecognized run status")
        if now() >= deadline:
            break
        if not pending:
            for name in names:
                output(f"workflow {name}: completed successfully")
            return
        report = tuple(pending)
        if report != last_report:
            for name, reason in pending:
                output(f"workflow {name}: waiting ({reason})")
            last_report = report
        sleep(min(poll, max(0, deadline - now())))
    raise GuardError("deadline exceeded before all required workflows succeeded")


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        # argparse's normal errors can echo arbitrary argument values.
        self.exit(2, "require-workflows: invalid arguments; see --help\n")


def main(argv=None):
    parser = SafeParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--workflow", action="append", required=True)
    parser.add_argument("--branch", default="main")
    parser.add_argument("--timeout-seconds", type=float, default=1800)
    parser.add_argument("--poll-seconds", type=float, default=15)
    args = parser.parse_args(argv)
    if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+", args.repo)
            or args.repo.rsplit("/", 1)[-1] in {".", ".."}
            or not re.fullmatch(r"[0-9a-fA-F]{40}", args.sha)
            or any(not text or len(text) > 255 or not text.isprintable()
                   for text in [args.branch] + args.workflow)
            or len(set(args.workflow)) != len(args.workflow)
            or any(not math.isfinite(value) or value <= 0
                   for value in [args.timeout_seconds, args.poll_seconds])):
        parser.error("invalid value")
    try:
        require_workflows(args.repo, args.sha.lower(), args.workflow, args.branch,
                          args.timeout_seconds, args.poll_seconds)
    except GuardError as error:
        print(f"require-workflows: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
