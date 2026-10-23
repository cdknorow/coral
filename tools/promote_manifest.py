#!/usr/bin/env python3
"""The promote manifest builder (#6274): what .github/workflows/promote.yml checks and writes. Standard library only.

    promote_manifest.py check   validates the dispatch inputs from the environment and prints nothing a log reader could misuse
    promote_manifest.py build   writes promote.json (schema 1) from the environment: the validated inputs and the facts the validate job read

Everything comes from the environment, never from arguments: a workflow input is data, and this file is the only place it is parsed. The manifest is exactly the
fields of internal/updater/verify.Manifest (an unknown field is refused by the client). Times are RFC 3339 UTC with a Z. The sequence is the promotion's UTC epoch
seconds: it only has to be monotonic (the client refuses a lower one than the last it accepted), and the workflow's concurrency group keeps two promotions from sharing a
second. This file lives on the protected promote branch with the workflow, so the ruleset that protects promote.yml protects it too.
"""
import json
import os
import re
import sys
import time

REPO = "subgentic/coral"                 # the pinned repository (verify.Repo): a manifest for another repository is refused by the client
# The client parses a manifest's version with verify.ParseVersion: plain X.Y.Z only (a suffix such as -rc1 is refused there), so only a plain version tag is promoted.
TAG = re.compile(r"v([0-9]+\.[0-9]+\.[0-9]+)")
VERSION = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+")
HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")
MAX_LIFETIME_DAYS = 59                   # the client allows MaxManifestLifetime (60 days); one under, so no comparison at the edge can refuse it
DEFAULT_LIFETIME_DAYS = 30
MAX_REVOKED = 20


class InputError(Exception):
    """An input is not acceptable. The message never repeats the input's value."""


def parse_tag(tag):
    m = TAG.fullmatch(tag or "")
    if not m:
        raise InputError("the tag is not a plain vX.Y.Z")
    return m.group(1)


def parse_lifetime(text):
    text = (text or "").strip() or str(DEFAULT_LIFETIME_DAYS)
    if not re.fullmatch(r"[0-9]{1,2}", text):
        raise InputError("lifetime_days is not a whole number of days")
    days = int(text)
    if days < 1 or days > MAX_LIFETIME_DAYS:
        raise InputError("lifetime_days is outside 1..%d" % MAX_LIFETIME_DAYS)
    return days


def parse_revoked(text):
    """A comma or space separated list of versions to revoke: each must be a version, no more than MAX_REVOKED, no duplicates."""
    items = [p for p in re.split(r"[,\s]+", (text or "").strip()) if p]
    if len(items) > MAX_REVOKED:
        raise InputError("more than %d versions to revoke" % MAX_REVOKED)
    out = []
    for item in items:
        v = item[1:] if item.startswith("v") else item
        if not VERSION.fullmatch(v):
            raise InputError("a version to revoke is not a plain version")
        if v not in out:
            out.append(v)
    return sorted(out)


def utc(ts):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(ts))


def build(env, now=None):
    """The manifest as a dict, from the environment mapping. Raises InputError for any input or fact that is not acceptable."""
    now = int(time.time() if now is None else now)
    version = parse_tag(env.get("PROMOTE_TAG", ""))
    days = parse_lifetime(env.get("PROMOTE_LIFETIME_DAYS", ""))
    revoked = parse_revoked(env.get("PROMOTE_REVOKE", ""))
    repo = env.get("GITHUB_REPOSITORY", "")
    if repo != REPO:
        raise InputError("this workflow runs in %r, not in the pinned repository" % repo[:80])
    prerelease = env.get("PROMOTE_PRERELEASE", "")
    if prerelease not in ("true", "false"):
        raise InputError("the release's prerelease flag is neither true nor false")
    source = env.get("PROMOTE_SOURCE_SHA", "")
    if not HEX40.fullmatch(source):
        raise InputError("source_sha is not 40 lowercase hex digits")
    sums = env.get("PROMOTE_SUMS_SHA256", "")
    if not HEX64.fullmatch(sums):
        raise InputError("sha256sums_sha256 is not 64 lowercase hex digits")
    return {
        "schema": 1,
        "repo": repo,
        "tag": "v" + version,
        "version": version,
        "prerelease": prerelease == "true",
        "source_sha": source,
        "sha256sums_sha256": sums,
        "sequence": now,
        "promoted_at": utc(now),
        "expires_at": utc(now + days * 86400),
        "revoked": revoked,
    }


def main(argv, env):
    if len(argv) != 2 or argv[1] not in ("check", "build"):
        print("usage: promote_manifest.py check|build (inputs come from the environment)", file=sys.stderr)
        return 2
    try:
        if argv[1] == "check":
            parse_tag(env.get("PROMOTE_TAG", ""))
            parse_lifetime(env.get("PROMOTE_LIFETIME_DAYS", ""))
            parse_revoked(env.get("PROMOTE_REVOKE", ""))
            print("inputs ok")
            return 0
        manifest = build(env)
    except InputError as e:
        print("promote: " + str(e), file=sys.stderr)
        return 1
    out = env.get("PROMOTE_OUT", "promote.json")
    with open(out, "w", encoding="utf-8", newline="\n") as f:
        json.dump(manifest, f, indent=2, sort_keys=False)
        f.write("\n")
    print("wrote %s: %s %s sequence %d, expires %s" % (out, manifest["tag"], "(prerelease)" if manifest["prerelease"] else "", manifest["sequence"], manifest["expires_at"]))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv, dict(os.environ)))
