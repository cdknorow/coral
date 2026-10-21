#!/usr/bin/env python3
"""The required check "gate" (.github/workflows/gate.yml, #5522): a pull request may merge only when the heavy workflows that
ran on its head commit all succeeded.

The heavy gates (go-tests, go-race, cdp-gate, and gofmt and windows-tests where they apply) are run by pushing the PR's head to a ci/
branch (and by the PR's own run); they are too slow and too numerous to be separate required checks, and a path-filtered one that did
not run would leave a required check pending for ever. This script is one job that reads those runs, from the GitHub API, for the head
commit: every workflow of .github/gate.json "always", and every one of "when" whose paths match a file the PR changed, must have a run
for that commit whose latest attempt ended in success. A failed, cancelled or timed-out run fails the gate at once; a run that is not
finished is waited for (up to timeout_minutes); one that never appears fails after appear_within_minutes, naming what to push.

Usage (gate.yml):  gate_check.py REPO PR_NUMBER HEAD_SHA    (GITHUB_TOKEN in the environment, read-only: actions and pull requests)
Exits 0 (green), 1 (not green, the reason printed), 2 (usage or a broken configuration: never green).
"""
import json
import os
import re
import sys
import time
import urllib.request

API = "https://api.github.com"


def glob_re(pattern):
    """A path glob: ** crosses folders, * does not, ? is one character."""
    out, i = "", 0
    while i < len(pattern):
        c = pattern[i]
        if pattern.startswith("**/", i):
            out += "(?:.*/)?"
            i += 3
        elif pattern.startswith("**", i):
            out += ".*"
            i += 2
        elif c == "*":
            out += "[^/]*"
            i += 1
        elif c == "?":
            out += "[^/]"
            i += 1
        else:
            out += re.escape(c)
            i += 1
    return re.compile("^" + out + "$")


def matches(paths, files):
    res = [glob_re(p) for p in paths]
    return any(r.match(f) for f in files for r in res)


class Gate:
    def __init__(self, cfg, get, files, sha, now=time.monotonic, sleep=time.sleep, repo=None):
        self.cfg, self.get, self.files, self.sha, self.now, self.sleep, self.repo = cfg, get, files, sha, now, sleep, repo

    def required(self):
        names = list(self.cfg["always"])
        for w in self.cfg.get("when", []):
            if matches(w["paths"], self.files):
                names.append(w["workflow"])
        return names

    def latest_runs(self):
        """The newest run per workflow name for the head commit: a push to the ci/ prefix, the PR's own run or a dispatch."""
        runs, page = [], 1
        while True:
            data = self.get(f"/actions/runs?head_sha={self.sha}&per_page=100&page={page}")
            batch = data.get("workflow_runs", [])
            runs += batch
            if len(batch) < 100:
                break
            page += 1
        prefix, events = self.cfg["branch_prefix"], set(self.cfg["events"])
        always, files = set(self.cfg["always"]), self.cfg["workflow_files"]
        always_events = set(self.cfg.get("always_events", events))
        best = {}
        for r in runs:
            if r.get("head_sha") != self.sha or r.get("event") not in events:
                continue
            # A run counts only if it is THIS repository's own run of THE workflow file (#5534 review): a fork's pull request can run a workflow of the same display name that always
            # succeeds (its run is listed under this repository with the same head commit), and any file can declare "name: go-tests".
            if self.repo is not None and str((r.get("head_repository") or {}).get("full_name", "")).lower() != self.repo.lower():
                continue
            if str(r.get("path", "")).split("@")[0] != files.get(r.get("name")):
                continue
            # The heavy gates are proved by a push to a ci/ branch or a dispatch, never by a pull request's own run (whose workflow file is the pull request's).
            if r.get("name") in always and r.get("event") not in always_events:
                continue
            if r.get("event") == "push" and not str(r.get("head_branch", "")).startswith(prefix):
                continue  # a push to main or to a branch an agent chose is not the ci/ gate
            n = r["name"]
            if n not in best or (r.get("created_at", ""), r.get("run_attempt", 1), r["id"]) > (best[n].get("created_at", ""), best[n].get("run_attempt", 1), best[n]["id"]):
                best[n] = r
        return best

    def evaluate(self):
        """(done, ok, lines): done False means keep waiting."""
        need, best, lines, waiting, failed = self.required(), self.latest_runs(), [], [], []
        for n in need:
            r = best.get(n)
            if r is None:
                waiting.append(n)
                lines.append(f"  waiting  {n}: no run for {self.sha[:9]} yet")
            elif r["status"] != "completed":
                waiting.append(n)
                lines.append(f"  waiting  {n}: {r['status']}")
            elif r["conclusion"] == "success":
                lines.append(f"  ok       {n}: success (run {r['id']})")
            else:
                failed.append(n)
                lines.append(f"  FAILED   {n}: {r['conclusion']} (run {r['id']})")
        if failed:
            return True, False, lines
        return (not waiting), (not waiting), lines

    def run(self, out=print):
        cfg = self.cfg
        out(f"gate: head {self.sha[:9]}, required: {', '.join(self.required())}")
        start = self.now()
        while True:
            done, ok, lines = self.evaluate()
            for l in lines:
                out(l)
            if done:
                out("gate: GREEN" if ok else "gate: NOT green (a required workflow failed)")
                return 0 if ok else 1
            best = self.latest_runs()
            missing = [n for n in self.required() if n not in best]
            elapsed = self.now() - start
            if missing and elapsed > cfg["appear_within_minutes"] * 60:
                out(f"gate: NOT green: no run for {', '.join(missing)} on {self.sha[:9]} after {cfg['appear_within_minutes']} minutes: push this commit to a {cfg['branch_prefix']} branch (git push origin HEAD:refs/heads/{cfg['branch_prefix']}pr-<number>)")
                return 1
            if elapsed > cfg["timeout_minutes"] * 60:
                out(f"gate: NOT green: still waiting after {cfg['timeout_minutes']} minutes")
                return 1
            self.sleep(cfg["poll_seconds"])


def load_config(path):
    cfg = json.load(open(path))
    for k, t in (("branch_prefix", str), ("always", list), ("events", list), ("always_events", list), ("workflow_files", dict), ("appear_within_minutes", int), ("timeout_minutes", int), ("poll_seconds", int)):
        if not isinstance(cfg.get(k), t):
            raise ValueError(f"gate.json: {k} must be a {t.__name__}")
    if not cfg["always"] or not all(isinstance(x, str) and x for x in cfg["always"]):
        raise ValueError("gate.json: always must name at least one workflow")
    for w in cfg.get("when", []):
        if not (isinstance(w.get("workflow"), str) and isinstance(w.get("paths"), list) and w["paths"]):
            raise ValueError("gate.json: every when entry needs a workflow and paths")
    for n in list(cfg["always"]) + [w.get("workflow") for w in cfg.get("when", [])]:
        if not isinstance(cfg["workflow_files"].get(n), str) or not cfg["workflow_files"][n].startswith(".github/workflows/"):
            raise ValueError(f"gate.json: workflow_files must give the .github/workflows file of {n}")
    if cfg["poll_seconds"] < 5 or cfg["timeout_minutes"] < 1 or cfg["appear_within_minutes"] < 1:
        raise ValueError("gate.json: poll, timeout and appear limits are too small")
    return cfg


def github_get(repo):
    token = os.environ.get("GITHUB_TOKEN", "")
    if not token:
        raise SystemExit("gate_check: GITHUB_TOKEN is not set")

    def get(path):
        req = urllib.request.Request(f"{API}/repos/{repo}{path}", headers={"Authorization": f"Bearer {token}", "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"})
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.load(resp)
    return get


def pr_files(get, number):
    files, page = [], 1
    while True:
        batch = get(f"/pulls/{number}/files?per_page=100&page={page}")
        files += [f["filename"] for f in batch] + [f["previous_filename"] for f in batch if f.get("previous_filename")]
        if len(batch) < 100 or page >= 30:  # GitHub lists at most 3000 files
            break
        page += 1
    return files


def main(argv):
    if len(argv) != 4 or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", argv[1]) or not argv[2].isdigit() or not re.fullmatch(r"[0-9a-f]{40}", argv[3]):
        print(__doc__, file=sys.stderr)
        return 2
    here = os.path.dirname(os.path.abspath(__file__))
    try:
        cfg = load_config(os.path.join(here, "..", "..", ".github", "gate.json"))
    except (OSError, ValueError) as e:
        print(f"gate_check: {e}", file=sys.stderr)
        return 2
    get = github_get(argv[1])
    return Gate(cfg, get, pr_files(get, argv[2]), argv[3], repo=argv[1]).run()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
