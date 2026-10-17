#!/usr/bin/env python3
"""audit-frontend-deps.py: known-vulnerability check of what the dashboard ships and tests with (#4093).

  1. The vendored libraries in coral-go/pkg/frontend/static/vendor, from the versions recorded in its
     README table (tests/frontend/vendor_manifest.mjs keeps that table equal to the files), looked up
     in OSV (api.osv.dev). These ship to every user: a HIGH or CRITICAL advisory, or one whose
     severity OSV does not state, FAILS the check (exit 1); lower ones are listed.
  2. npm audit of the lock that builds the CodeMirror bundle (tests/vendor-build/codemirror), runtime
     packages only (--omit=dev: the bundle's contents). A high or critical one fails; a lower one is listed.
  3. npm audit of tests/frontend's test tooling. Dev only, never shipped: listed, never failing.

Needs python3 (standard library only) and npm; network access to api.osv.dev and the npm registry.
Usage: tools/audit-frontend-deps.py [--extra name@version ...]   (--extra adds a package to the vendored
set, to prove the check fails on a vulnerable one: e.g. --extra marked@4.0.0)
Exit: 0 clean, 1 a failing finding, 2 the check could not run (never a silent pass).
"""
import json
import os
import re
import subprocess
import sys
import urllib.request

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
README = os.path.join(ROOT, "coral-go", "pkg", "frontend", "static", "vendor", "README.md")
FAIL_LEVELS = {"HIGH", "CRITICAL"}
OSV = "https://api.osv.dev/v1"


def die(msg):
    print("audit-frontend-deps: " + msg, file=sys.stderr)
    sys.exit(2)


def vendored():
    """(package, version) for every library recorded in the vendor README's two tables."""
    text = open(README, encoding="utf-8").read()
    found = []
    # | `file` | [`package`](url) | version | ...
    for m in re.finditer(r"^\| `[^`]+` \| \[`([^`]+)`\]\([^)]*\) \| ([0-9][^ |]*) \|", text, re.M):
        found.append((m.group(1), m.group(2)))
    # | `package` | version |   (the CodeMirror bundle's packages)
    for m in re.finditer(r"^\| `(@?codemirror[^`]*)` \| ([0-9][^ |]*) \|", text, re.M):
        found.append((m.group(1), m.group(2)))
    seen, out = set(), []
    for p in found:
        if p not in seen:
            seen.add(p)
            out.append(p)
    return out


def post(url, body):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=60))


def get(url):
    return json.load(urllib.request.urlopen(url, timeout=60))


def severity(vuln):
    """HIGH, CRITICAL, MODERATE, LOW, or UNKNOWN from an OSV record."""
    s = str((vuln.get("database_specific") or {}).get("severity", "")).upper()
    if s in ("CRITICAL", "HIGH", "MODERATE", "MEDIUM", "LOW"):
        return "MODERATE" if s == "MEDIUM" else s
    return "UNKNOWN"


def osv_check(pkgs):
    """Returns (failing, listed): lines to print."""
    res = post(OSV + "/querybatch", {"queries": [{"package": {"ecosystem": "npm", "name": n}, "version": v} for n, v in pkgs]})
    results = res.get("results", [])
    if len(results) != len(pkgs):
        die("OSV answered for %d of %d packages" % (len(results), len(pkgs)))
    failing, listed = [], []
    for (name, ver), r in zip(pkgs, results):
        for v in r.get("vulns", []):
            full = get(OSV + "/vulns/" + v["id"])
            sev = severity(full)
            line = "%s@%s: %s (%s) %s" % (name, ver, v["id"], sev, (full.get("summary") or "")[:100])
            (failing if sev in FAIL_LEVELS or sev == "UNKNOWN" else listed).append(line)
    return failing, listed


def npm_audit(rel, omit_dev):
    """Returns (failing, listed) for one npm lock, from the lock alone."""
    cwd = os.path.join(ROOT, rel)
    cmd = ["npm", "audit", "--package-lock-only", "--json"] + (["--omit=dev"] if omit_dev else [])
    p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True)
    try:
        data = json.loads(p.stdout)
    except ValueError:
        die("npm audit gave no JSON in %s: %s" % (rel, (p.stderr or p.stdout)[:200]))
    if "vulnerabilities" not in data:
        die("npm audit failed in %s: %s" % (rel, json.dumps(data)[:200]))
    failing, listed = [], []
    for name, v in sorted(data["vulnerabilities"].items()):
        sev = str(v.get("severity", "")).upper()
        via = ", ".join(x if isinstance(x, str) else str(x.get("title", "")) for x in v.get("via", []))[:120]
        line = "%s (%s) in %s: %s, range %s, fix %s" % (name, sev, rel, via, v.get("range"), (v.get("fixAvailable") or {}).get("version") if isinstance(v.get("fixAvailable"), dict) else v.get("fixAvailable"))
        (failing if sev in FAIL_LEVELS else listed).append(line)
    return failing, listed


def main(argv):
    extra = []
    args = list(argv)
    while args:
        a = args.pop(0)
        if a == "--extra" and args and "@" in args[0][1:]:
            n, _, v = args.pop(0).rpartition("@")
            extra.append((n, v))
        else:
            die("usage: audit-frontend-deps.py [--extra name@version ...]")
    pkgs = vendored()
    if len(pkgs) < 20:
        die("found only %d vendored packages in the README (expected the table's 32): the table format changed?" % len(pkgs))
    pkgs += extra
    print("vendored libraries: %d (name@version looked up in OSV)" % len(pkgs))
    failing, listed = osv_check(pkgs)
    f2, l2 = npm_audit("tests/vendor-build/codemirror", True)
    failing += f2
    listed += l2
    f3, l3 = npm_audit("tests/frontend", False)
    listed += f3 + l3  # dev tooling: never failing
    if listed:
        print("\nListed (not failing; dev-only or below high):")
        for l in listed:
            print("  " + l)
    if failing:
        print("\nFAILING (shipped, high or critical, or severity unknown):")
        for l in failing:
            print("  " + l)
        return 1
    print("\nOK: no high or critical advisory in the shipped front-end libraries")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
