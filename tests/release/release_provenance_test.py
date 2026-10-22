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


def run(name, sha, conclusion="success", status="completed", event="push", branch="ci/v14144", rid=1, repo=REPO, at="2026-10-21T01:00:00Z"):
    return {"id": rid, "name": name, "status": status, "conclusion": conclusion, "event": event, "head_branch": branch, "head_sha": sha,
            "created_at": at, "run_attempt": 1, "head_repository": {"full_name": repo},
            "path": f".github/workflows/{name}.yml@refs/heads/{branch}"}


def api(commits, compare, runs):
    """commits: sha -> {files, parents}; compare: sha -> status; runs: list of run dicts"""
    def get(path):
        if path.startswith("/commits/"):
            c = commits[path.split("/")[2]]
            return {"files": [{"filename": f} for f in c["files"]], "parents": [{"sha": s} for s in c["parents"]], "commit": {"message": c.get("message", "")}}
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

    # ---- by name (#6067): the notes commit's "Gated-commit" trailer ----
    def named(self, sha=GATED, files=("RELEASE_NOTES.md",), parents=(GATED,)):
        return {TAGGED: {"files": list(files), "parents": list(parents), "message": f"release notes: v1.4.157\n\nGated-commit: {sha}\n"}}

    def test_a_named_gated_commit_is_judged_and_the_tagged_commits_own_runs_are_ignored(self):
        # 156's second stop: a cdp-gate dispatched on main sat on the tagged commit; with the old rule it made the tagged commit the judged one
        # and go-tests, go-race showed "no run". Named, it is the parent that is judged, whatever ran on the tagged commit.
        runs = ALL_GREEN + [run("cdp-gate", TAGGED, rid=9, event="workflow_dispatch", branch="main")]
        rc, out = go(self.named(), {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 0, out)
        self.assertIn(f"gated commit: {GATED} (named by the notes commit's Gated-commit trailer)", out)
        self.assertIn("| cdp-gate | success | run 3 |", out)
        # a RED run on the tagged commit does not matter either: only the named commit is read
        runs = ALL_GREEN + [run("go-tests", TAGGED, conclusion="failure", rid=9, event="workflow_dispatch", branch="main")]
        rc, out = go(self.named(), {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 0, out)

    def test_a_flake_repeat_dispatched_on_the_gated_commit_does_not_replace_its_push_run(self):
        # 156's first stop: a 10x repeat of one suite (a cdp-gate dispatch, red by design for a flaky suite) was newer than the push run and became the one read
        runs = ALL_GREEN + [run("cdp-gate", GATED, conclusion="failure", rid=9, event="workflow_dispatch", branch="ci/v14144", at="2026-10-21T02:00:00Z")]
        rc, out = go(self.named(), {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 0, out)
        self.assertIn("| cdp-gate | success | run 3 |", out)

    def test_a_red_push_run_of_the_named_commit_still_fails_the_release(self):
        runs = [run("go-tests", GATED), run("go-race", GATED, rid=2), run("cdp-gate", GATED, conclusion="failure", rid=3),
                run("cdp-gate", GATED, rid=9, event="workflow_dispatch", at="2026-10-21T02:00:00Z")]
        rc, out = go(self.named(), {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 1, out)
        self.assertIn("| cdp-gate | failure | run 3 |", out)

    def test_a_gate_with_no_push_run_on_the_named_commit_may_use_a_dispatched_one(self):
        runs = [run("go-tests", GATED), run("go-race", GATED, rid=2), run("cdp-gate", GATED, rid=3, event="workflow_dispatch")]
        rc, out = go(self.named(), {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 0, out)

    def test_a_missing_gate_on_the_named_commit_fails_and_runs_elsewhere_do_not_fill_in(self):
        runs = [run("go-tests", GATED), run("go-race", GATED, rid=2), run("cdp-gate", TAGGED, rid=3, event="workflow_dispatch", branch="main")]
        rc, out = go(self.named(), {TAGGED: "identical"}, runs)
        self.assertEqual(rc, 1, out)
        self.assertIn("| cdp-gate | none |", out)

    def test_a_trailer_that_does_not_name_the_parent_or_a_commit_that_is_not_notes_only_is_refused(self):
        other = "c" * 40
        for commits in (self.named(sha=other),
                        self.named(files=("RELEASE_NOTES.md", "coral-go/main.go")),
                        self.named(parents=(GATED, other))):
            rc, out = go(commits, {TAGGED: "identical"}, ALL_GREEN + [run("go-tests", other, rid=8), run("go-race", other, rid=7), run("cdp-gate", other, rid=6)])
            self.assertEqual(rc, 1, out)
            self.assertIn("| trailer | refused |", out)

    def test_two_different_trailers_are_refused_even_when_the_first_is_the_parent(self):
        other = "c" * 40
        commits = {TAGGED: {"files": ["RELEASE_NOTES.md"], "parents": [GATED], "message": f"release notes: v1.4.157\n\nGated-commit: {GATED}\nGated-commit: {other}\n"}}
        rc, out = go(commits, {TAGGED: "identical"}, ALL_GREEN)
        self.assertEqual(rc, 1, out)
        self.assertIn("| trailer | refused |", out)

    def test_a_mistyped_trailer_is_refused_and_shown_not_silently_ignored(self):
        for bad in (GATED.upper(), GATED[:12], GATED + " (the parent)", GATED + "x"):
            commits = {TAGGED: {"files": ["RELEASE_NOTES.md"], "parents": [GATED], "message": f"release notes: v1.4.157\n\nGated-commit: {bad}\n"}}
            rc, out = go(commits, {TAGGED: "identical"}, ALL_GREEN)
            self.assertEqual(rc, 1, out)
            self.assertIn("| trailer | refused | a Gated-commit line is not in the exact form", out)
            self.assertIn(bad[:12], out)

    def test_main_rejects_a_bad_argument_before_any_request(self):
        for argv in (["x"], ["x", "a/b", "c" * 40], ["x", "a/b", "c" * 40, "bad actor;rm"], ["x", "a b", "c" * 40, "ok"], ["x", "a/b", "zz", "ok"]):
            self.assertEqual(p.main(argv), 2, argv)


if __name__ == "__main__":
    unittest.main()
