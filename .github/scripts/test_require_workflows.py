"""Release-gate regressions; no network or GitHub credentials required."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from urllib.parse import parse_qs, urlsplit
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "require_workflows", Path(__file__).with_name("require-workflows.py"))
guard = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guard)
SHA = "a" * 40
REPO = "datatug/datatug-cli"
NAMES = ["Go CI", "Everywhere candidate acceptance"]


def run(workflow=1, **changes):
    value = dict(id=100 + workflow, workflow_id=workflow, run_number=10,
                 run_attempt=1, head_sha=SHA, head_branch="main", event="push",
                 status="completed", conclusion="success")
    value.update(changes)
    return value


def job(run_id=101, **changes):
    value = dict(run_id=run_id, head_sha=SHA, status="completed", conclusion="success")
    value.update(changes)
    return value


class Clock:
    def __init__(self):
        self.value = 0
        self.sleeps = []

    def now(self):
        return self.value

    def sleep(self, duration):
        self.sleeps.append(duration)
        self.value += duration


class API:
    def __init__(self, runs=None, workflows=None, jobs=None):
        self.workflows = workflows if workflows is not None else [
            {"id": i + 1, "name": name} for i, name in enumerate(NAMES)]
        self.runs = runs if runs is not None else {1: [run()], 2: [run(2)]}
        self.jobs = jobs if jobs is not None else {101: [job()], 102: [job(102)]}
        self.calls = []

    def __call__(self, endpoint):
        self.calls.append(endpoint)
        parsed = urlsplit(endpoint)
        query = parse_qs(parsed.query)
        page = int(query["page"][0])
        if parsed.path.endswith("/workflows"):
            key, items = "workflows", self.workflows
        elif parsed.path.endswith("/runs"):
            workflow_id = int(parsed.path.split("/")[-2])
            key, items = "workflow_runs", self.runs.get(workflow_id, [])
            assert query["event"] == ["push"]
            assert query["branch"] == ["main"]
            assert query["head_sha"] == [SHA]
        elif parsed.path.endswith("/jobs"):
            parts = parsed.path.split("/")
            key, items = "jobs", self.jobs.get(int(parts[-4]), [])
        else:
            raise AssertionError(endpoint)
        return {"total_count": len(items), key: items[(page - 1) * 100:page * 100]}


class GateTests(unittest.TestCase):
    def check_gate(self, api, timeout=3, poll=2, names=NAMES):
        self.clock = Clock()
        self.log = []
        return guard.require_workflows(REPO, SHA, names, timeout=timeout, poll=poll,
                                       api=api, now=self.clock.now,
                                       sleep=self.clock.sleep, output=self.log.append)

    def test_both_required_workflows_and_jobs_succeed(self):
        api = API()
        self.check_gate(api)
        self.assertEqual(len(self.log), 2)
        self.assertTrue(all("completed successfully" in line for line in self.log))
        self.assertTrue(any("/runs/101/attempts/1/jobs?" in c for c in api.calls))

    def test_every_unsuccessful_conclusion_blocks(self):
        for conclusion in ("skipped", "failure", "cancelled", "timed_out",
                           "startup_failure", "neutral", "action_required", None, "unknown"):
            with self.subTest(conclusion=conclusion):
                with self.assertRaisesRegex(guard.GuardError, "without success"):
                    self.check_gate(API(runs={1: [run(conclusion=conclusion)]}))

    def test_missing_workflow_waits_and_fails_by_deadline(self):
        with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
            self.check_gate(API(workflows=[]))
        self.assertEqual(self.clock.value, 3)
        self.assertEqual(self.clock.sleeps, [2, 1])
        self.assertIn("workflow missing", self.log[0])

    def test_missing_run_waits_and_fails(self):
        with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
            self.check_gate(API(runs={}))
        self.assertIn("push run missing", self.log[0])

    def test_other_sha_event_and_branch_cannot_satisfy_gate(self):
        candidates = [run(head_sha="b" * 40), run(event="pull_request"),
                      run(event="workflow_dispatch"), run(head_branch="feature")]
        with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
            self.check_gate(API(runs={1: candidates}))

    def test_running_never_satisfies_gate(self):
        with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
            self.check_gate(API(runs={1: [run(status="in_progress", conclusion=None)]}))

    def test_running_can_complete_within_deadline(self):
        api = API(runs={1: [run(status="queued", conclusion=None)], 2: [run(2)]})
        clock = Clock()

        def finish_after_sleep(duration):
            clock.sleep(duration)
            api.runs[1] = [run()]

        guard.require_workflows(REPO, SHA, NAMES, timeout=3, poll=1, api=api,
                                now=clock.now, sleep=finish_after_sleep, output=lambda _: None)
        self.assertEqual(clock.value, 1)

    def test_older_success_cannot_mask_newer_failed_run(self):
        api = API(runs={1: [run(), run(id=201, run_number=11, conclusion="failure")]})
        with self.assertRaisesRegex(guard.GuardError, "without success"):
            self.check_gate(api)

    def test_successful_rerun_overrides_previous_failed_attempt(self):
        api = API(runs={1: [run(conclusion="failure"), run(run_attempt=2)], 2: [run(2)]})
        self.check_gate(api)
        self.assertTrue(any("/runs/101/attempts/2/jobs?" in c for c in api.calls))

    def test_failed_rerun_cannot_use_prior_success(self):
        api = API(runs={1: [run(), run(run_attempt=2, conclusion="failure")]})
        with self.assertRaisesRegex(guard.GuardError, "without success"):
            self.check_gate(api)

    def test_no_actual_successful_job_blocks(self):
        for jobs in ([], [job(conclusion="skipped")], [job(conclusion="failure")],
                     [job(status="in_progress", conclusion=None)]):
            with self.subTest(jobs=jobs):
                with self.assertRaisesRegex(guard.GuardError, "no successful job"):
                    self.check_gate(API(jobs={101: jobs}))

    def test_job_identity_must_match_commit_and_run(self):
        for jobs in ([job(head_sha="b" * 40)], [job(run_id=999)]):
            with self.assertRaisesRegex(guard.GuardError, "job identity"):
                self.check_gate(API(jobs={101: jobs}))

    def test_exact_workflow_name_required_and_duplicates_rejected(self):
        with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
            self.check_gate(API(workflows=[{"id": 1, "name": "Go CI extra"}]))
        with self.assertRaisesRegex(guard.GuardError, "ambiguous"):
            self.check_gate(API(workflows=[{"id": 1, "name": NAMES[0]},
                                           {"id": 2, "name": NAMES[0]}]))

    def test_workflow_run_and_job_pagination(self):
        workflows = [{"id": i + 10, "name": f"irrelevant {i}"} for i in range(100)]
        workflows += [{"id": 1, "name": NAMES[0]}, {"id": 2, "name": NAMES[1]}]
        older_runs = [run(id=i + 300, run_number=i + 20, conclusion="failure")
                      for i in range(100)]
        latest = run(id=999, run_number=999)
        api = API(workflows=workflows, runs={1: older_runs + [latest], 2: [run(2)]},
                  jobs={999: [job(999, conclusion="skipped") for _ in range(100)]
                        + [job(999)], 102: [job(102)]})
        self.check_gate(api)
        self.assertEqual(sum("page=2" in c for c in api.calls), 3)

    def test_malformed_responses_fail_closed(self):
        invalid = [None, {}, {"total_count": True, "workflows": []},
                   {"total_count": 1, "workflows": []},
                   {"total_count": 1, "workflows": ["bad"]}]
        for response in invalid:
            with self.subTest(response=response):
                with self.assertRaises(guard.GuardError):
                    self.check_gate(lambda _: response)

    def test_malformed_matching_run_cannot_pass(self):
        for changes in ({"workflow_id": 99}, {"run_attempt": None},
                        {"run_number": True}, {"status": "unexpected"}, {"status": []}):
            with self.subTest(changes=changes):
                with self.assertRaises(guard.GuardError):
                    self.check_gate(API(runs={1: [run(**changes)]}))


class TransportTests(unittest.TestCase):
    def test_fixed_argv_no_shell_and_remaining_budget_timeout(self):
        calls = []

        def runner(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0, '{"total_count": 0}')

        self.assertEqual(guard.GhAPI(10, now=lambda: 7, run=runner)("repos/a/b/actions/workflows"),
                         {"total_count": 0})
        argv, kwargs = calls[0]
        self.assertEqual(argv, ["gh", "api", "--method", "GET", "repos/a/b/actions/workflows"])
        self.assertEqual(kwargs["timeout"], 3)
        self.assertEqual(kwargs["stderr"], subprocess.DEVNULL)
        self.assertNotIn("shell", kwargs)

    def test_auth_errors_never_echo_raw_diagnostics(self):
        secret = "TOKEN_SHOULD_NEVER_APPEAR"
        runner = lambda *a, **k: subprocess.CompletedProcess(a, 1, secret, secret)
        with self.assertRaisesRegex(guard.GuardError, "^GitHub API request failed$"):
            guard.GhAPI(10, now=lambda: 0, run=runner)("repos/a/b/actions/workflows")

    def test_subprocess_timeouts_and_missing_binary_fail_closed(self):
        for error in (subprocess.TimeoutExpired("gh", 1, output="secret", stderr="secret"),
                      FileNotFoundError("secret")):
            with self.subTest(error=type(error).__name__):
                with patch.object(guard.subprocess, "run", side_effect=error):
                    with self.assertRaises(guard.GuardError) as result:
                        guard.GhAPI(10, now=lambda: 0, run=guard.subprocess.run)("endpoint")
                    self.assertNotIn("secret", str(result.exception))

    def test_deadline_prevents_any_new_subprocess(self):
        with patch.object(guard.subprocess, "run") as runner:
            with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
                guard.GhAPI(10, now=lambda: 10, run=runner)("endpoint")
            runner.assert_not_called()

    def test_response_finishing_after_deadline_is_not_success(self):
        ticks = iter([9, 11])
        runner = lambda *a, **k: subprocess.CompletedProcess(a, 0, "{}")
        with self.assertRaisesRegex(guard.GuardError, "deadline exceeded"):
            guard.GhAPI(10, now=lambda: next(ticks), run=runner)("endpoint")

    def test_malformed_json_is_safe_failure(self):
        for response in ("secret", "[]", "null"):
            runner = lambda *a, **k: subprocess.CompletedProcess(a, 0, response)
            with self.assertRaisesRegex(guard.GuardError, "malformed"):
                guard.GhAPI(10, now=lambda: 0, run=runner)("endpoint")


class CLITests(unittest.TestCase):
    def test_invalid_arguments_are_rejected_without_echoing_values(self):
        base = ["--repo", REPO, "--sha", SHA, "--workflow", NAMES[0]]
        for extra in (["--timeout-seconds", "0"], ["--poll-seconds", "nan"],
                      ["--poll-seconds", "inf"], ["--sha", "secret"],
                      ["--repo", "https://secret@github.com/a/b"],
                      ["--repo", "../.."],
                      ["--workflow", "secret\n::error::injected"], ["--unexpected-secret"]):
            with self.subTest(extra=extra), contextlib.redirect_stderr(io.StringIO()) as err:
                with self.assertRaises(SystemExit) as result:
                    guard.main(base + extra)
                self.assertEqual(result.exception.code, 2)
                self.assertNotIn("secret", err.getvalue())

    def test_cli_returns_nonzero_and_safe_error_on_api_failure(self):
        with patch.object(guard, "require_workflows", side_effect=guard.GuardError("GitHub API request failed")):
            with contextlib.redirect_stderr(io.StringIO()) as err:
                result = guard.main(["--repo", REPO, "--sha", SHA, "--workflow", NAMES[0]])
        self.assertEqual(result, 1)
        self.assertEqual(err.getvalue(), "require-workflows: GitHub API request failed\n")

    def test_real_cli_subprocess_uses_fake_gh_without_network(self):
        fixture = '''import json, sys
endpoint = sys.argv[-1].split("?")[0]
if endpoint.endswith("/workflows"):
    value = {"total_count": 1, "workflows": [{"id": 1, "name": "Go CI"}]}
elif endpoint.endswith("/runs"):
    value = {"total_count": 1, "workflow_runs": [RUN]}
elif endpoint.endswith("/runs/101/attempts/1/jobs"):
    value = {"total_count": 1, "jobs": [JOB]}
else:
    sys.stderr.write("unexpected endpoint; TOKEN_SHOULD_NEVER_APPEAR")
    sys.exit(1)
print(json.dumps(value))
'''.replace("[RUN]", repr([run()])).replace("[JOB]", repr([job()]))
        with tempfile.TemporaryDirectory() as directory:
            fake_gh = Path(directory, "gh")
            fake_gh.write_text(f"#!{sys.executable}\n" + fixture)
            fake_gh.chmod(0o700)
            env = dict(os.environ, PATH=directory + os.pathsep + os.environ.get("PATH", ""),
                       GH_TOKEN="TOKEN_SHOULD_NEVER_APPEAR")
            argv = [sys.executable, str(Path(__file__).with_name("require-workflows.py")),
                    "--repo", REPO, "--sha", SHA, "--workflow", "Go CI", "--timeout-seconds", "5"]
            result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("completed successfully", result.stdout)
            self.assertNotIn("TOKEN_SHOULD_NEVER_APPEAR", result.stdout + result.stderr)
            fake_gh.write_text(f"#!{sys.executable}\nimport sys\n"
                               "print('TOKEN_SHOULD_NEVER_APPEAR', file=sys.stderr)\nsys.exit(1)\n")
            result = subprocess.run(argv, env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 1)
            self.assertIn("GitHub API request failed", result.stderr)
            self.assertNotIn("TOKEN_SHOULD_NEVER_APPEAR", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
