#!/usr/bin/env python3
"""The release provenance record (#5550, Security's rule 3 of the 05:20 decision): the first step of release.yml writes down who pushed the tag,
the tagged commit, whether it is the tip of main (or at least on it), and whether the heavy gates (go-tests, go-race, cdp-gate: .github/gate.json
"always") were green on that exact code, and FAILS the release when they were not.

"That exact code": the heavy gates run on the commit that is pushed to a ci/ branch (the gated commit); the commit that is tagged is the same
tree with the release notes on top (a notes-only commit, tools/devops/land-main.sh). So the runs are looked up for the tagged commit first, and,
when it has none and it changes RELEASE_NOTES.md only, for its single parent. A tagged commit that changes anything else and has no green gates
fails.

By name (#6067): a notes commit whose message carries a trailer "Gated-commit: <40 hex>" (which must be its single parent) is judged on exactly that
commit: each gate's newest automatic push run there, runs on any other commit ignored. Without the trailer the rule above applies.

Usage (release.yml):  release_provenance.py REPO TAG_COMMIT_SHA ACTOR   (GITHUB_TOKEN in the environment, read-only: contents and actions)
Prints a markdown record (also fit for $GITHUB_STEP_SUMMARY). Exits 0 (gates green), 1 (not green), 2 (usage or a broken configuration).
"""
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gate_check as g  # noqa: E402

NOTES = "RELEASE_NOTES.md"
GATED_TRAILER = re.compile(r"^Gated-commit: ([0-9a-f]{40})\s*$", re.MULTILINE)


def provenance(cfg, get, repo, sha, actor, out=print):
    commit = get(f"/commits/{sha}")
    files = [f["filename"] for f in commit.get("files", [])]
    parents = [p["sha"] for p in commit.get("parents", [])]
    cmp = get(f"/compare/main...{sha}")["status"]  # "identical": the tip of main; "behind": an ancestor of main
    need = list(cfg["always"])

    def runs_for(s):
        return g.Gate(cfg, get, [], s, repo=repo).latest_runs()

    def runs_for_named(s):
        """The runs of a commit the notes commit NAMED (#6067): each gate's newest automatic push run on exactly that commit; only a gate with no push run there
        falls back to a dispatched one. A manual repeat or a stray dispatch of a heavy gate (a flake repeat, a cdp-gate on main) is a newer run of the same
        workflow, and must not become the one the release reads."""
        push_cfg = dict(cfg, events=["push"], always_events=["push"])
        best_push = g.Gate(push_cfg, get, [], s, repo=repo).latest_runs()
        if all(n in best_push for n in need):
            return best_push
        best_any = runs_for(s)
        return {n: best_push.get(n) or best_any[n] for n in need if n in best_push or n in best_any}

    message = (commit.get("commit") or {}).get("message", "")
    named = GATED_TRAILER.findall(message)
    lines, ok = [], True
    if named:
        # "Gated-commit: <sha>" (a trailer of the notes commit, written by whoever lands it): the release is judged on THAT commit, by name; runs on any other
        # commit (the tagged commit's own included) are ignored. It must be the single parent of a notes-only commit, so it cannot name some other code.
        if len(set(named)) != 1 or files != [NOTES] or len(parents) != 1 or named[0] != parents[0]:
            lines.append(f"| trailer | refused | Gated-commit names {', '.join(sorted(set(named)))[:100]} but the tagged commit is "
                         + ("not a notes-only commit with one parent" if files != [NOTES] or len(parents) != 1 else f"on parent {parents[0][:9]}") + " |")
            ok = False
            gated, best = sha, {}
        else:
            gated, best = named[0], runs_for_named(named[0])
    else:
        gated, best = sha, runs_for(sha)
        if not any(n in best for n in need) and files == [NOTES] and len(parents) == 1:
            gated, best = parents[0], runs_for(parents[0])
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
    out(f"- gated commit: {gated}" + ("" if gated == sha else " (named by the notes commit's Gated-commit trailer)" if named and gated == (named[0] if named else None) else " (the tagged commit adds the release notes only)"))
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
