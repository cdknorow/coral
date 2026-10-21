#!/usr/bin/env python3
"""The release provenance record (#5550, Security's rule 3 of the 05:20 decision): the first step of release.yml writes down who pushed the tag,
the tagged commit, whether it is the tip of main (or at least on it), and whether the heavy gates (go-tests, go-race, cdp-gate: .github/gate.json
"always") were green on that exact code, and FAILS the release when they were not.

"That exact code": the heavy gates run on the commit that is pushed to a ci/ branch (the gated commit); the commit that is tagged is the same
tree with the release notes on top (a notes-only commit, tools/devops/land-main.sh). So the runs are looked up for the tagged commit first, and,
when it has none and it changes RELEASE_NOTES.md only, for its single parent. A tagged commit that changes anything else and has no green gates
fails.

Usage (release.yml):  release_provenance.py REPO TAG_COMMIT_SHA ACTOR   (GITHUB_TOKEN in the environment, read-only: contents and actions)
Prints a markdown record (also fit for $GITHUB_STEP_SUMMARY). Exits 0 (gates green), 1 (not green), 2 (usage or a broken configuration).
"""
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gate_check as g  # noqa: E402

NOTES = "RELEASE_NOTES.md"


def provenance(cfg, get, repo, sha, actor, out=print):
    commit = get(f"/commits/{sha}")
    files = [f["filename"] for f in commit.get("files", [])]
    parents = [p["sha"] for p in commit.get("parents", [])]
    cmp = get(f"/compare/main...{sha}")["status"]  # "identical": the tip of main; "behind": an ancestor of main
    need = list(cfg["always"])

    def runs_for(s):
        return g.Gate(cfg, get, [], s, repo=repo).latest_runs()

    gated, best = sha, runs_for(sha)
    if not any(n in best for n in need) and files == [NOTES] and len(parents) == 1:
        gated, best = parents[0], runs_for(parents[0])
    lines, ok = [], True
    for n in need:
        r = best.get(n)
        if r is None:
            lines.append(f"| {n} | none | no run for {gated[:9]} |")
            ok = False
        elif r["status"] == "completed" and r["conclusion"] == "success":
            lines.append(f"| {n} | success | run {r['id']} |")
        else:
            lines.append(f"| {n} | {r.get('conclusion') or r['status']} | run {r['id']} |")
            ok = False
    where = {"identical": "the tip of main", "behind": "on main (main has moved on)"}.get(cmp, f"NOT on main (compare says {cmp})")
    out("### Release provenance")
    out(f"- tag pushed by: {actor}")
    out(f"- tagged commit: {sha}")
    out(f"- position: {where}")
    out(f"- gated commit: {gated}" + ("" if gated == sha else " (the tagged commit adds the release notes only)"))
    out("")
    out("| gate | result | evidence |")
    out("|---|---|---|")
    for l in lines:
        out(l)
    out("")
    out("provenance: GATES GREEN" if ok else "provenance: NOT green: a release is not built from code whose heavy gates did not pass")
    return 0 if ok else 1


def main(argv):
    if len(argv) != 4 or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", argv[1]) or not re.fullmatch(r"[0-9a-f]{40}", argv[2]) \
            or not re.fullmatch(r"[A-Za-z0-9_.\[\]-]{1,64}", argv[3]):
        print(__doc__, file=sys.stderr)
        return 2
    here = os.path.dirname(os.path.abspath(__file__))
    try:
        cfg = g.load_config(os.path.join(here, "..", "..", ".github", "gate.json"))
    except (OSError, ValueError) as e:
        print(f"release_provenance: {e}", file=sys.stderr)
        return 2
    return provenance(cfg, g.github_get(argv[1]), argv[1], argv[2], argv[3])


if __name__ == "__main__":
    sys.exit(main(sys.argv))
