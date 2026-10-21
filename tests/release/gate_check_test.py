#!/usr/bin/env python3
"""Tests of tests/release/gate_check.py (#5522): the required check "gate" against a fake GitHub. Run: python3 -I tests/release/gate_check_test.py"""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gate_check as g  # noqa: E402

SHA = "a" * 40
CFG = {
    "branch_prefix": "ci/", "always": ["go-tests", "go-race", "cdp-gate"],
    "when": [{"workflow": "gofmt", "paths": ["coral-go/**.go", ".github/workflows/**"]}, {"workflow": "windows-tests", "paths": ["coral-go/**", "tests/windows/**"]}],
    "events": ["push", "pull_request", "workflow_dispatch"], "appear_within_minutes": 10, "timeout_minutes": 60, "poll_seconds": 30,
}


def run(name, status="completed", conclusion="success", event="push", branch="ci/pr-1", sha=SHA, created="2026-10-21T01:00:00Z", rid=1, attempt=1):
    return {"id": rid, "name": name, "status": status, "conclusion": conclusion, "event": event, "head_branch": branch, "head_sha": sha, "created_at": created, "run_attempt": attempt}


class Clock:
    def __init__(self):
        self.t = 0.0

    def now(self):
        return self.t

    def sleep(self, s):
        self.t += s


def gate(runs_by_poll, files, cfg=CFG):
    """runs_by_poll: a list of run lists, one per poll (the last repeats). Returns (exit code, output lines, polls made)."""
    clock, calls, lines = Clock(), [], []

    def get(path):
        assert path.startswith("/actions/runs?head_sha=" + SHA), path
        i = min(len(calls), len(runs_by_poll) - 1)
        calls.append(i)
        return {"workflow_runs": runs_by_poll[i] if "page=1" in path else []}
    code = g.Gate(cfg, get, files, SHA, clock.now, clock.sleep).run(lines.append)
    return code, lines, len(calls)


GREEN = [run("go-tests", rid=1), run("go-race", rid=2), run("cdp-gate", rid=3)]


class GateTests(unittest.TestCase):
    def test_all_green_passes(self):
        code, lines, _ = gate([GREEN], ["README.md"])
        self.assertEqual(code, 0, lines)

    def test_a_failed_run_fails_at_once(self):
        code, lines, polls = gate([[run("go-tests", rid=1), run("go-race", conclusion="failure", rid=2), run("cdp-gate", status="in_progress", conclusion=None, rid=3)]], ["README.md"])
        self.assertEqual(code, 1)
        self.assertEqual(polls, 1)  # no waiting for cdp-gate once go-race has failed
        self.assertTrue(any("FAILED   go-race" in l for l in lines))

    def test_cancelled_and_timed_out_and_skipped_are_not_success(self):
        for c in ("cancelled", "timed_out", "skipped", "neutral", "action_required"):
            code, _, _ = gate([[run("go-tests", rid=1), run("go-race", conclusion=c, rid=2), run("cdp-gate", rid=3)]], ["README.md"])
            self.assertEqual(code, 1, c)

    def test_a_running_workflow_is_waited_for(self):
        running = [run("go-tests", rid=1), run("go-race", status="in_progress", conclusion=None, rid=2), run("cdp-gate", status="queued", conclusion=None, rid=3)]
        code, _, polls = gate([running, running, GREEN], ["README.md"])
        self.assertEqual((code, polls), (0, 3))

    def test_a_missing_run_fails_after_the_appear_limit_and_says_what_to_push(self):
        code, lines, polls = gate([[run("go-tests", rid=1), run("go-race", rid=2)]], ["README.md"])  # cdp-gate never appears
        self.assertEqual(code, 1)
        self.assertGreater(polls, 10)
        self.assertTrue(any("no run for cdp-gate" in l and "push this commit to a ci/ branch" in l for l in lines), lines)

    def test_a_run_that_appears_late_is_accepted(self):
        late = [GREEN[0], GREEN[1]]
        code, _, _ = gate([late, late, late, GREEN], ["README.md"])
        self.assertEqual(code, 0)

    def test_a_workflow_that_never_ends_times_out(self):
        stuck = [run("go-tests", rid=1), run("go-race", rid=2), run("cdp-gate", status="in_progress", conclusion=None, rid=3)]
        code, lines, _ = gate([stuck], ["README.md"])
        self.assertEqual(code, 1)
        self.assertTrue(any("still waiting after 60 minutes" in l for l in lines))

    def test_the_newest_run_of_a_workflow_decides(self):
        old_fail_new_ok = [run("go-tests", conclusion="failure", created="2026-10-21T01:00:00Z", rid=1), run("go-tests", created="2026-10-21T02:00:00Z", rid=4), GREEN[1], GREEN[2]]
        self.assertEqual(gate([old_fail_new_ok], ["README.md"])[0], 0)
        old_ok_new_fail = [run("go-tests", created="2026-10-21T01:00:00Z", rid=1), run("go-tests", conclusion="failure", created="2026-10-21T02:00:00Z", rid=4), GREEN[1], GREEN[2]]
        self.assertEqual(gate([old_ok_new_fail], ["README.md"])[0], 1)

    def test_a_rerun_attempt_decides_over_the_first_attempt(self):
        runs = [run("go-tests", conclusion="failure", rid=1, attempt=1), run("go-tests", rid=1, attempt=2), GREEN[1], GREEN[2]]
        self.assertEqual(gate([runs], ["README.md"])[0], 0)

    def test_a_run_for_another_commit_or_event_or_branch_does_not_count(self):
        other = [run("go-tests", sha="b" * 40, rid=1), run("go-race", rid=2, event="schedule", branch="main"), run("cdp-gate", rid=3, event="push", branch="main")]
        self.assertEqual(gate([other], ["README.md"])[0], 1)  # none counts: go-tests is another commit, go-race a schedule, cdp-gate a push to main

    def test_a_push_to_a_branch_that_is_not_ci_is_not_the_gate(self):
        runs = [run("go-tests", rid=1, branch="trial/x"), GREEN[1], GREEN[2]]
        self.assertEqual(gate([runs], ["README.md"])[0], 1)

    def test_the_prs_own_run_and_a_dispatch_count(self):
        runs = [run("go-tests", event="workflow_dispatch", rid=1), run("go-race", rid=2), run("cdp-gate", rid=3)]
        self.assertEqual(gate([runs], ["README.md"])[0], 0)

    def test_a_when_workflow_is_required_only_when_its_paths_match(self):
        self.assertEqual(g.Gate(CFG, None, ["README.md"], SHA).required(), ["go-tests", "go-race", "cdp-gate"])
        self.assertEqual(g.Gate(CFG, None, ["coral-go/internal/x/y.go"], SHA).required(), ["go-tests", "go-race", "cdp-gate", "gofmt", "windows-tests"])
        self.assertEqual(g.Gate(CFG, None, ["tests/windows/a.ps1"], SHA).required(), ["go-tests", "go-race", "cdp-gate", "windows-tests"])
        self.assertEqual(g.Gate(CFG, None, [".github/workflows/x.yml"], SHA).required(), ["go-tests", "go-race", "cdp-gate", "gofmt"])

    def test_a_path_filtered_workflow_that_did_not_run_does_not_fail_a_docs_only_pr(self):
        self.assertEqual(gate([GREEN], ["docs/x.md", "README.md"])[0], 0)  # no gofmt or windows-tests run exists, and none is required

    def test_a_required_when_workflow_must_have_succeeded(self):
        files = ["coral-go/a.go"]
        self.assertEqual(gate([GREEN + [run("gofmt", rid=7), run("windows-tests", rid=8, event="pull_request")]], files)[0], 0)
        self.assertEqual(gate([GREEN + [run("gofmt", conclusion="failure", rid=7), run("windows-tests", rid=8)]], files)[0], 1)

    def test_glob_semantics(self):
        self.assertTrue(g.matches(["coral-go/**.go"], ["coral-go/internal/a/b.go"]))
        self.assertTrue(g.matches(["coral-go/**.go"], ["coral-go/main.go"]))
        self.assertFalse(g.matches(["coral-go/**.go"], ["coral-go/a.txt"]))
        self.assertTrue(g.matches([".github/workflows/**"], [".github/workflows/x/y.yml"]))
        self.assertFalse(g.matches(["tests/*.sh"], ["tests/a/b.sh"]))
        self.assertTrue(g.matches(["tests/*.sh"], ["tests/b.sh"]))
        self.assertTrue(g.matches(["**/*_windows.go"], ["coral-go/x/y_windows.go"]))

    def test_the_real_config_loads_and_names_the_heavy_workflows(self):
        cfg = g.load_config(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", ".github", "gate.json"))
        self.assertEqual(cfg["always"], ["go-tests", "go-race", "cdp-gate"])

    def test_a_broken_config_is_refused(self):
        import json
        import tempfile
        for bad in ({}, {**CFG, "always": []}, {**CFG, "poll_seconds": 1}, {**CFG, "when": [{"workflow": "x"}]}, {**CFG, "events": "push"}):
            with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
                json.dump(bad, f)
            try:
                with self.assertRaises(ValueError):
                    g.load_config(f.name)
            finally:
                os.unlink(f.name)

    def test_usage_errors_exit_2(self):
        self.assertEqual(g.main(["gate_check.py"]), 2)
        self.assertEqual(g.main(["gate_check.py", "not a repo", "1", SHA]), 2)
        self.assertEqual(g.main(["gate_check.py", "o/r", "x", SHA]), 2)
        self.assertEqual(g.main(["gate_check.py", "o/r", "1", "abc"]), 2)


if __name__ == "__main__":
    unittest.main(verbosity=1)
