#!/usr/bin/env python3
"""Rules every workflow in .github/workflows keeps (#5369, Security's CI pass). Run from anywhere: python3 tests/release/workflows_lint.py

1. Every `uses:` of an action, in a workflow or in a local composite (.github/actions/*/action.yml), is pinned by a full 40-hex commit (a local ./path action is exempt).
2. Every actions/checkout step sets `persist-credentials: false`: the token must not stay in .git/config for the steps after it
   (the release job's token can write contents and mint a Sigstore identity).
3. No workflow input, branch, tag, PR text or matrix value is interpolated into a run: script. It goes through env: instead.
   Allowed in a script: github.repository, github.run_id, github.sha, runner.*, matrix.* from a fixed list, steps.*.outputs of our own steps.
4. No pull_request_target and no workflow_run trigger (a fork's code would run with the repository's token or secrets).
5. The relaunch test seam's build tag (relaunchfixture, #5452) is named nowhere that builds or ships anything: not in .github/, installers/, tools/, a Makefile or the
   Homebrew cask, except the files listed in SEAM_ALLOWED (none today: a trial workflow that builds it for coral-hub is added there by a reviewed edit).
Exits 1 and prints every violation.
"""
import glob
import os
import re
import sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")
SAFE_IN_SCRIPT = re.compile(r"^(github\.(repository|run_id|run_number|sha|workspace|token)|runner\.\w+|matrix\.[\w.-]+|steps\.[\w-]+\.outputs\.[\w-]+|env\.\w+|secrets\.\w+|needs\.[\w-]+\.outputs\.[\w-]+|inputs\.arch)$")


def step_blocks(lines):
    """Yield (first line number, list of lines) for each `- ` step item."""
    i = 0
    while i < len(lines):
        m = re.match(r"^(\s*)- (?:name|uses|run|id|if|shell|with|env|working-directory):", lines[i])
        if m:
            ind = len(m.group(1))
            j = i + 1
            while j < len(lines) and (not lines[j].strip() or len(lines[j]) - len(lines[j].lstrip()) > ind):
                j += 1
            yield i + 1, lines[i:j]
            i = j
        else:
            i += 1


SEAM_TAG = "relaunchfixture"
SEAM_ALLOWED = {
    # go-tests.yml's relaunchfixture job runs the tests that only exist under the tag (#6290); it builds test binaries, never a program that ships.
    ".github/workflows/go-tests.yml",
}  # repository paths (from the root) that may name the tag: a reviewed edit adds one
SEAM_ROOTS = [".github", "installers", "tools", "Casks", "coral-go/Makefile", "Makefile"]


def seam_violations():
    """Files of the places that build or ship anything that name the relaunch seam's build tag."""
    repo = ROOT
    bad = []
    for root in SEAM_ROOTS:
        p = os.path.join(repo, root)
        files = []
        if os.path.isdir(p):
            for dp, dn, fn in os.walk(p):
                dn[:] = [d for d in dn if d not in (".git", "node_modules")]
                files += [os.path.join(dp, f) for f in fn]
        elif os.path.isfile(p):
            files = [p]
        for f in files:
            rel = os.path.relpath(f, repo).replace(os.sep, "/")
            try:
                text = open(f, encoding="utf-8", errors="ignore").read()
            except OSError:
                continue
            if SEAM_TAG in text.lower() and rel not in SEAM_ALLOWED:
                bad.append(f"{rel}: names the build tag {SEAM_TAG} (the relaunch test seam is never built or shipped by this; see SEAM_ALLOWED)")
    return bad


def main():
    bad = []
    for path in sorted(glob.glob(os.path.join(ROOT, ".github", "workflows", "*.yml"))):
        name = os.path.basename(path)
        text = open(path).read()
        lines = text.split("\n")
        if re.search(r"^\s*(pull_request_target|workflow_run)\s*:", text, re.M):
            bad.append(f"{name}: uses a pull_request_target or workflow_run trigger")
        for no, block in step_blocks(lines):
            body = "\n".join(block)
            m = re.search(r"uses:\s*(\S+)", body)
            if m and not m.group(1).startswith("./"):
                if not re.search(r"@[0-9a-f]{40}$", m.group(1)):
                    bad.append(f"{name}:{no}: action {m.group(1)} is not pinned by a full commit")
                if m.group(1).startswith("actions/checkout@") and not re.search(r"persist-credentials:\s*false", body):
                    bad.append(f"{name}:{no}: actions/checkout without persist-credentials: false")
        in_run, ind = False, 0
        for no, l in enumerate(lines, 1):
            m = re.match(r"^(\s*)(?:- )?run:\s*(.*)$", l)
            if m:
                in_run, ind = True, len(m.group(1))
                chunk = m.group(2) if m.group(2) not in ("|", ">", "|-", ">-", "|+") else ""
            elif in_run:
                if l.strip() and len(l) - len(l.lstrip()) <= ind:
                    in_run = False
                    continue
                chunk = l
            else:
                continue
            for e in re.findall(r"\$\{\{(.*?)\}\}", chunk):
                if not SAFE_IN_SCRIPT.match(e.strip()):
                    bad.append(f"{name}:{no}: `${{{{ {e.strip()} }}}}` is interpolated into a run: script (pass it through env:)")
    # A local composite action's own `uses:` lines follow rule 1 too (a composite is read from the checkout when its step runs).
    # The lint reads each action.yml as text: it cannot resolve what a THIRD-PARTY action uses inside itself (a pinned
    # azure/trusted-signing-action nests actions/cache@v4 by tag), which only the repository's SHA-pinning setting catches.
    for path in sorted(glob.glob(os.path.join(ROOT, ".github", "actions", "*", "action.yml"))):
        rel = os.path.relpath(path, ROOT)
        for no, block in step_blocks(open(path).read().split("\n")):
            m = re.search(r"uses:\s*(\S+)", "\n".join(block))
            if m and not m.group(1).startswith("./") and not re.search(r"@[0-9a-f]{40}$", m.group(1)):
                bad.append(f"{rel}:{no}: action {m.group(1)} is not pinned by a full commit")
    bad += seam_violations()
    for b in bad:
        print(b)
    print(f"workflows_lint: {len(bad)} violation(s)")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
