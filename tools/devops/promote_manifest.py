#!/usr/bin/env python3
"""promote_manifest.py: build promote.json (schema 1) for .github/workflows/promote.yml (#326).

It is run by promote.yml's build job, and by the updater's tests against its own verifier
(coral-go/internal/updater/testrig/promote_script_test.go).
Nothing is read from the command line. The inputs are environment variables, each matched here before use:
  MODE           promote | refresh | revoke | backfill
  TAG            the release tag, a plain vX.Y.Z (subgentic/coral)
  SOURCE_SHA     the tag's commit, 40 lowercase hex: the verify job resolves the tag (an annotated one peeled) and the
                 release's verified sums bundle must carry the same commit
  SUMS_FILE      the path of the verified SHA256SUMS.txt; its sha256 goes into the manifest
  PREV_MANIFEST  the path of the highest promote-<n>'s promote.json, its signature and n == its sequence already verified;
                 empty when there is no promotion yet
  REVOKE         versions to revoke, space separated plain X.Y.Z (mode revoke only)
  NOW            optional, an RFC 3339 UTC time (the tests); default: the clock
  LIFETIME_DAYS  optional, default 30 (the updater allows at most 60)
It writes the manifest to stdout: one JSON object, the schema's fields only, compact, with sorted keys.
It writes nothing else there; errors go to stderr with exit 1.

The rules mirror the updater (coral-go/internal/updater/verify: ParseManifest, CheckManifest, CheckManifestOf):
  - every field is present, tag == "v" + version, the hex fields are lowercase, and sequence >= 1;
  - the times are RFC 3339 UTC with a Z, expires_at is after promoted_at, and the lifetime is at most 60 days;
  - revoked holds plain versions (at most 512), and the promoted version is never in it;
  - sequence == the previous manifest's + 1, exactly: the updater finds promote-<sequence> releases by probing the names
    upward, so the sequence has no gaps (coral-go/internal/updater/stage/promotion.go).
And the promotion rules of the workflow:
  - promote:  the version is strictly newer than the previous manifest's;
  - refresh:  the same tag as the previous manifest (a new expiry, a new sequence);
  - revoke:   REVOKE names the previous manifest's version. The tag is the release approve-release put back as latest,
              so it may be older;
  - backfill: a manifest for an older approved version, published only as promote-v<version> for the Windows rollback
              lookup (which doesn't read the sequence), never as a promote-<n>: its sequence is the previous one, so it
              takes no number from the promotions.
"""
import datetime as dt
import hashlib
import json
import os
import re
import sys

PLAIN = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")
TAG = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+$")
HEX40 = re.compile(r"^[0-9a-f]{40}$")
REPO = "subgentic/coral"  # the manifest's repo field: verify.Repo, the release home
MAX_LIFETIME_DAYS = 60
MAX_REVOKED = 512
FIELDS = ["schema", "repo", "tag", "version", "prerelease", "source_sha", "sha256sums_sha256", "sequence", "promoted_at", "expires_at", "revoked"]


def die(msg):
    print("promote_manifest: " + msg, file=sys.stderr)
    sys.exit(1)


def vkey(v):
    return tuple(int(x) for x in v.split("."))


def utc(s):
    if not s.endswith("Z"):
        die(f"{s!r} is not an RFC 3339 UTC time")
    return dt.datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc)


def fmt(t):
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


def load_prev(path):
    if not path:
        return None
    with open(path, "rb") as f:
        data = f.read()
    m = json.loads(data)
    if not isinstance(m, dict) or sorted(m) != sorted(FIELDS):
        die("the previous manifest does not have exactly the schema's fields")
    if m["schema"] != 1 or m["repo"] != REPO or not PLAIN.match(m["version"]) or not isinstance(m["sequence"], int) or m["sequence"] < 1:
        die("the previous manifest is not a schema 1 manifest for " + REPO)
    if not all(isinstance(r, str) and PLAIN.match(r) for r in m["revoked"]):
        die("the previous manifest's revoked list is not plain versions")
    return m


def main():
    env = os.environ.get
    mode = env("MODE", "")
    if mode not in ("promote", "refresh", "revoke", "backfill"):
        die("MODE is not promote, refresh, revoke or backfill")
    tag = env("TAG", "")
    if not TAG.match(tag):
        die("TAG is not a plain vX.Y.Z")
    version = tag[1:]
    sha = env("SOURCE_SHA", "")
    if not HEX40.match(sha):
        die("SOURCE_SHA is not 40 lowercase hex digits")
    sums = env("SUMS_FILE", "")
    with open(sums, "rb") as f:
        sums_sha = hashlib.sha256(f.read()).hexdigest()
    revoke = env("REVOKE", "").split()
    if mode != "revoke" and revoke:
        die("REVOKE is only for mode revoke")
    if mode == "revoke" and not revoke:
        die("mode revoke needs REVOKE")
    for r in revoke:
        if not PLAIN.match(r):
            die(f"REVOKE {r!r} is not a plain X.Y.Z")
    lifetime = int(env("LIFETIME_DAYS") or "30")
    if not 1 <= lifetime <= MAX_LIFETIME_DAYS:
        die(f"LIFETIME_DAYS must be 1..{MAX_LIFETIME_DAYS}")
    now = utc(env("NOW")) if env("NOW") else dt.datetime.now(dt.timezone.utc).replace(microsecond=0)

    prev = load_prev(env("PREV_MANIFEST", ""))
    revoked = sorted(set((prev["revoked"] if prev else []) + revoke), key=vkey)
    if mode == "promote" and prev and vkey(version) <= vkey(prev["version"]):
        die(f"{version} is not newer than the promoted {prev['version']} (promote only moves forward; use refresh or revoke)")
    if mode == "refresh" and (not prev or prev["tag"] != tag):
        die("refresh renews the promoted tag only")
    if mode == "revoke" and (not prev or prev["version"] not in revoke):
        die("revoke must name the version that is promoted now")
    if mode == "backfill" and prev and vkey(version) >= vkey(prev["version"]):
        die("backfill is for an older version than the promoted one")
    if version in revoked:
        die(f"{version} is revoked and can never be promoted")
    if len(revoked) > MAX_REVOKED:
        die("too many revoked versions")
    if mode == "backfill":
        seq = prev["sequence"] if prev else 1
    else:
        seq = (prev["sequence"] + 1) if prev else 1

    m = {
        "schema": 1, "repo": REPO, "tag": tag, "version": version, "prerelease": False, "source_sha": sha,
        "sha256sums_sha256": sums_sha, "sequence": seq, "promoted_at": fmt(now),
        "expires_at": fmt(now + dt.timedelta(days=lifetime)), "revoked": revoked,
    }
    sys.stdout.write(json.dumps(m, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    main()
