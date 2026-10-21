#!/usr/bin/env python3
"""Tests of release_provenance.py (#5550) against a stand-in GitHub API: no network."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gate_check as g  # noqa: E402
import release_provenance as p  # noqa: E402

REPO = "subgentic/coral"
CFG = g.load_config(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", ".github", "gate.json"))
TAGGED = "b" * 40  # the notes commit that is tagged
GATED = "a" * 40  # its parent: the code the gates ran on


def run(name, sha, conclusion="success", status="completed", event="push", branch="ci/v14144", rid=1, repo=REPO):
    return {"id": rid, "name": name, "status": status, "conclusion": conclusion, "event": event, "head_branch": branch, "head_sha": sha,
            "created_at": "2026-10-21T01:00:00Z", "run_attempt": 1, "head_repository": {"full_name": repo},
            "path": f".github/workflows/{name}.yml@refs/heads/{branch}"}


def api(commits, compare, runs):
    """commits: sha -> {files, parents}; compare: sha -> status; runs: list of run dicts"""
    def get(path):
        if path.startswith("/commits/"):
            c = commits[path.split("/")[2]]
            return {"files": [{"filename": f} for f in c["files"]], "parents": [{"sha": s} for s in c["parents"]]}
        if path.startswith("/compare/main..."):
            return {"status": compare[path.split("...")[1]]}
        if path.startswith("/actions/runs?head_sha="):
            sha = path.split("head_sha=")[1].split("&")[0]
            return {"workflow_runs": [r for r in runs if r["head_sha"] == sha]}
        raise AssertionError(path)
    return get


def go(commits, compare, runs, actor="the-pusher"):
    lines = []
    rc = p.provenance(CFG, api(commits, compare, runs), REPO, TAGGED, actor, out=lines.append)
    return rc, "\n".join(lines)


NOTES_ONLY = {TAGGED: {"files": ["RELEASE_NOTES.md"], "parents": [GATED]}}
ALL_GREEN = [run("go-tests", GATED, rid=1), run("go-race", GATED, rid=2), run("cdp-gate", GATED, rid=3)]


class T(unittest.TestCase):
    def test_a_notes_commit_is_gated_by_its_parents_runs(self):
        rc, out = go(NOTES_ONLY, {TAGGED: "identical"}, ALL_GREEN)
        self.assertEqual(rc, 0, out)
        self.assertIn("tag pushed by: the-pusher", out)
        self.assertIn(f"tagged commit: {TAGGED}", out)
        self.assertIn("the tip of main", out)
        self.assertIn(f"gated commit: {GATED} (the tagged commit adds the release notes only)", out)
        self.assertIn("GATES GREEN", out)

    def test_the_record_says_when_main_has_moved_on(self):
        rc, out = go(NOTES_ONLY, {TAGGED: "behind"}, ALL_GREEN)
        self.assertEqual(rc, 0, out)
        self.assertIn("on main (main has moved on)", out)

    def test_a_commit_not_on_main_is_recorded_and_still_judged_by_its_gates(self):
        rc, out = go(NOTES_ONLY, {TAGGED: "ahead"}, ALL_GREEN)
        self.assertEqual(rc, 0, out)
        self.assertIn("NOT on main (compare says ahead)", out)

    def test_runs_on_the_tagged_commit_itself_count(self):
        runs = [run(n, TAGGED, rid=i) for i, n in enumerate(["go-tests", "go-race", "cdp-gate"], 1)]
        rc, out = go({TAGGED: {"files": ["coral-go/main.go"], "parents": [GATED]}}, {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 0, out)
        self.assertIn(f"gated commit: {TAGGED}", out)
        self.assertNotIn("adds the release notes only", out)

    def test_a_failed_gate_fails_the_release(self):
        runs = [run("go-tests", GATED), run("go-race", GATED, conclusion="failure", rid=2), run("cdp-gate", GATED, rid=3)]
        rc, out = go(NOTES_ONLY, {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 1)
        self.assertIn("| go-race | failure |", out)
        self.assertIn("NOT green", out)

    def test_a_gate_still_running_or_cancelled_is_not_green(self):
        for kw in ({"status": "in_progress", "conclusion": None}, {"conclusion": "cancelled"}, {"conclusion": "skipped"}, {"conclusion": "timed_out"}):
            runs = [run("go-tests", GATED), run("go-race", GATED, rid=2, **kw), run("cdp-gate", GATED, rid=3)]
            rc, _ = go(NOTES_ONLY, {TAGGED: "identical"}, runs)
            self.assertEqual(rc, 1, kw)

    def test_a_missing_gate_fails_and_says_which(self):
        rc, out = go(NOTES_ONLY, {TAGGED: "identical"}, [run("go-tests", GATED), run("go-race", GATED, rid=2)])
        self.assertEqual(rc, 1)
        self.assertIn("| cdp-gate | none |", out)

    def test_no_gates_at_all_fails(self):
        rc, _ = go(NOTES_ONLY, {TAGGED: "identical"}, [])
        self.assertEqual(rc, 1)

    def test_a_commit_that_changes_code_does_not_borrow_its_parents_gates(self):
        rc, out = go({TAGGED: {"files": ["RELEASE_NOTES.md", "coral-go/main.go"], "parents": [GATED]}}, {TAGGED: "identical"}, ALL_GREEN)
        self.assertEqual(rc, 1, out)
        self.assertIn(f"no run for {TAGGED[:9]}", out)

    def test_a_merge_commit_does_not_borrow_a_parents_gates(self):
        rc, _ = go({TAGGED: {"files": ["RELEASE_NOTES.md"], "parents": [GATED, "c" * 40]}}, {TAGGED: "identical"}, ALL_GREEN)
        self.assertEqual(rc, 1)

    def test_a_run_of_a_look_alike_or_another_repository_or_a_main_push_does_not_count(self):
        runs = [run("go-tests", GATED, repo="evil/coral"), run("go-race", GATED, rid=2, branch="main"), run("cdp-gate", GATED, rid=3, event="pull_request")]
        rc, _ = go(NOTES_ONLY, {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 1)

    def test_main_rejects_a_bad_argument_before_any_request(self):
        for argv in (["x"], ["x", "a/b", "c" * 40], ["x", "a/b", "c" * 40, "bad actor;rm"], ["x", "a b", "c" * 40, "ok"], ["x", "a/b", "zz", "ok"]):
            self.assertEqual(p.main(argv), 2, argv)


if __name__ == "__main__":
    unittest.main()
