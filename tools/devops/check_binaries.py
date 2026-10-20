#!/usr/bin/env python3
"""tools/devops/check_binaries.py BASE TIP | --tree REF: refuse a commit that adds a program or a big file (#5444).

A built program carries the build machine's paths and account name in its build info, so one committed by mistake is a leak in a
public repository, and it stays in the history. In every commit of BASE..TIP (each one on its own, so a program added and removed again is still found), every file added or changed must be at most 5 MB and must
not start with an ELF, Mach-O or PE (MZ) header, unless the path is listed in tools/devops/binary-allowlist.txt (a file that is
already in the tree on purpose: a test fixture, an icon). --tree REF lists what REF holds that would be refused (how the allowlist
is made). Exits 1 and names every offender; the check has no override, a new allowed path is an edit to the allowlist, reviewed.
"""
import os
import subprocess
import sys

MAX = 5 * 1024 * 1024
MAGICS = {b"\x7fELF": "ELF", b"\xcf\xfa\xed\xfe": "Mach-O", b"\xce\xfa\xed\xfe": "Mach-O", b"\xfe\xed\xfa\xcf": "Mach-O",
          b"\xfe\xed\xfa\xce": "Mach-O", b"\xca\xfe\xba\xbe": "Mach-O universal"}
HERE = os.path.dirname(os.path.abspath(__file__))


def git(*args, stdin=None):
    return subprocess.run(["git", *args], input=stdin, capture_output=True, check=True).stdout


def kind(head):
    if head[:4] in MAGICS:
        return MAGICS[head[:4]]
    if head[:2] == b"MZ":
        return "PE"
    return ""


def allowed():
    p = os.environ.get("CORAL_BINARY_ALLOWLIST") or os.path.join(HERE, "binary-allowlist.txt")
    if not os.path.exists(p):
        return set()
    return {l.strip() for l in open(p) if l.strip() and not l.startswith("#")}


def inspect(entries):
    """entries: (path, blob sha). Returns [(path, why)] for the offenders."""
    if not entries:
        return []
    batch = "".join(sha + "\n" for _, sha in entries).encode()
    out = git("cat-file", "--batch", stdin=batch)
    bad, i = [], 0
    for path, _ in entries:
        nl = out.index(b"\n", i)
        _, _, size = out[i:nl].decode().split()
        size = int(size)
        body = out[nl + 1:nl + 1 + size]
        i = nl + 1 + size + 1
        why = []
        if size > MAX:
            why.append("%.1f MB" % (size / 1048576))
        k = kind(body[:4])
        if k:
            why.append(k + " program")
        if why:
            bad.append((path, ", ".join(why)))
    return bad


def main(argv):
    ok = allowed()
    if len(argv) == 3 and argv[1] == "--tree":
        raw = git("ls-tree", "-r", "-z", argv[2]).decode().split("\0")
        entries = []
        for r in raw:
            if r:
                meta, path = r.split("\t", 1)
                entries.append((path, meta.split()[2]))
    elif len(argv) == 3:
        # every commit of the range on its own: a program added in one commit and removed in the next is still pushed with the history
        entries, seen = [], set()
        for c in git("rev-list", argv[1] + ".." + argv[2]).decode().split():
            raw = git("diff-tree", "-r", "-m", "--no-commit-id", "--no-renames", "-z", c).decode().split("\0")
            for j in range(0, len(raw) - 1, 2):
                f = raw[j].split()
                if f[4][0] in "AM" and f[3] != "0" * 40 and not f[1].startswith(("160000", "120000")) and (raw[j + 1], f[3]) not in seen:
                    seen.add((raw[j + 1], f[3]))
                    entries.append((raw[j + 1], f[3]))
    else:
        print(__doc__, file=sys.stderr)
        return 2
    # the raw diff gives abbreviated ids; resolve them whole (cat-file takes either)
    bad = [(p, w) for p, w in inspect(entries) if p not in ok]
    for p, w in bad:
        print("check_binaries: refused %s (%s)" % (p, w), file=sys.stderr)
    if bad:
        print("check_binaries: %d file(s) refused: a built program carries this machine's paths and account name; a big file bloats the repository forever (#5444)" % len(bad), file=sys.stderr)
        return 1
    print("check_binaries: %d changed file(s), none over 5 MB, no ELF, Mach-O or PE" % len(entries))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
