import importlib.util
from pathlib import Path
import unittest
from urllib.error import HTTPError

spec = importlib.util.spec_from_file_location('hybrid', Path(__file__).with_name('everywhere-hybrid.py'))
hybrid = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hybrid)


class Clock:
    def __init__(self):
        self.now = 0

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


class HybridPollingTests(unittest.TestCase):
    def run_poll(self, request):
        clock = Clock()
        return hybrid.wait_for_completed_run(request, 'demo-once', timeout=3,
                                             sleep=clock.sleep, monotonic=clock.monotonic)

    def test_temporary_unavailability_requires_a_real_completed_run(self):
        calls = []

        def request(path):
            calls.append(path)
            if len(calls) < 3:
                raise HTTPError(path, 503, 'unavailable', {}, None)
            return {'runs': [{'id': 'real-run', 'executionStatus': 'completed', 'outcome': 'pass'}]}

        run, unavailable = self.run_poll(request)
        self.assertEqual((run['id'], run['outcome'], unavailable, len(calls)), ('real-run', 'pass', 2, 3))

    def test_persistent_unavailability_fails_at_the_deadline(self):
        calls = []

        def request(path):
            calls.append(path)
            raise HTTPError(path, 503, 'unavailable', {}, None)

        with self.assertRaisesRegex(AssertionError, 'deadline'):
            self.run_poll(request)
        self.assertEqual(len(calls), 3)

    def test_denied_access_is_not_retried(self):
        calls = []

        def request(path):
            calls.append(path)
            raise HTTPError(path, 403, 'denied', {}, None)

        with self.assertRaises(HTTPError) as raised:
            self.run_poll(request)
        self.assertEqual((raised.exception.code, len(calls)), (403, 1))

    def test_pending_runs_never_satisfy_completion(self):
        with self.assertRaisesRegex(AssertionError, 'deadline'):
            self.run_poll(lambda _: {'runs': [{'executionStatus': 'running'}]})

    def test_business_failure_is_not_rewritten_to_pass(self):
        run, unavailable = self.run_poll(lambda _: {'runs': [{'id': 'failed', 'executionStatus': 'completed', 'outcome': 'fail'}]})
        self.assertEqual((run['outcome'], unavailable), ('fail', 0))


if __name__ == '__main__':
    unittest.main()
