#!/usr/bin/env python3
"""tools/devops/check_binaries.py BASE TIP | --tree REF: refuse a commit that adds a program or a big file (#5444).

A built program carries the build machine's paths and account name in its build info, so one committed by mistake is a leak in a
public repository, and it stays in the history. In every commit of BASE..TIP (each one on its own, so a program added and removed again is still found), every file added or changed must be at most 5 MB and must
not start with an ELF, Mach-O or PE (MZ) header, unless the path is listed in tools/devops/binary-allowlist.txt (a file that is
already in the tree on purpose, an icon: the allowlist lifts the SIZE limit only, a program is refused under any path). --tree REF lists what REF holds that would be refused (how the allowlist
is made). Exits 1 and names every offender; the check has no override, a new allowed path is an edit to the allowlist, reviewed.
"""
import os
import subprocess
import sys

MAX = 5 * 1024 * 1024
MAGICS = {b"\x7fELF": "ELF", b"\xcf\xfa\xed\xfe": "Mach-O", b"\xce\xfa\xed\xfe": "Mach-O", b"\xfe\xed\xfa\xcf": "Mach-O",
          b"\xfe\xed\xfa\xce": "Mach-O", b"\xca\xfe\xba\xbe": "Mach-O universal", b"\xca\xfe\xba\xbf": "Mach-O universal",
          b"\xbe\xba\xfe\xca": "Mach-O universal", b"\xbf\xba\xfe\xca": "Mach-O universal"}
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
    """entries: (path, blob sha). Returns [(path, why, size_only)] for the offenders. A blob over the limit is not read at all (its size is enough);
    a smaller one is read once for its first bytes."""
    if not entries:
        return []
    sizes = git("cat-file", "--batch-check", stdin="".join(sha + "\n" for _, sha in entries).encode()).decode().splitlines()
    small = [(p, sha) for (p, sha), line in zip(entries, sizes) if int(line.split()[2]) <= MAX]
    heads = {}
    if small:
        out = git("cat-file", "--batch", stdin="".join(sha + "\n" for _, sha in small).encode())
        i = 0
        for _, sha in small:
            nl = out.index(b"\n", i)
            size = int(out[i:nl].decode().split()[2])
            heads[sha] = out[nl + 1:nl + 1 + min(size, 4)]
            i = nl + 1 + size + 1
    bad = []
    for (path, sha), line in zip(entries, sizes):
        size = int(line.split()[2])
        big = size > MAX
        k = kind(heads.get(sha, b""))
        why = []
        if big:
            why.append("%.1f MB" % (size / 1048576))
        if k:
            why.append(k + " program")
        if why:
            bad.append((path, ", ".join(why), big and not k))
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
    elif len(argv) == 3 or (len(argv) == 2 and argv[1] == "--commits"):
        # every commit of the range on its own: a program added in one commit and removed in the next is still pushed with the history.
        # "--commits" reads the commit ids from stdin (a branch with no base on the remote: the hook lists what no remote ref holds).
        entries, seen = [], set()
        commits = sys.stdin.read().split() if len(argv) == 2 else git("rev-list", argv[1] + ".." + argv[2]).decode().split()
        for c in commits:
            raw = git("diff-tree", "-r", "-m", "--root", "--no-commit-id", "--no-renames", "-z", c).decode().split("\0")
            for j in range(0, len(raw) - 1, 2):
                f = raw[j].split()
                if f[4][0] in "AM" and f[3] != "0" * 40 and not f[1].startswith(("160000", "120000")) and (raw[j + 1], f[3]) not in seen:
                    seen.add((raw[j + 1], f[3]))
                    entries.append((raw[j + 1], f[3]))
    else:
        print(__doc__, file=sys.stderr)
        return 2
    # the raw diff gives abbreviated ids; resolve them whole (cat-file takes either)
    # The allowlist lets a path through only for its SIZE (the three icons are big images): a program is refused whatever its path, so a program
    # swapped in under an allowlisted name is not let through (#5444 review).
    bad = [(p, w) for p, w, size_only in inspect(entries) if not (p in ok and size_only)]
    for p, w in bad:
        print("check_binaries: refused %s (%s)" % (p, w), file=sys.stderr)
    if bad:
        print("check_binaries: %d file(s) refused: a built program carries this machine's paths and account name; a big file bloats the repository forever (#5444)" % len(bad), file=sys.stderr)
        return 1
    print("check_binaries: %d changed file(s), none over 5 MB, no ELF, Mach-O or PE" % len(entries))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
