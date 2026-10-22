#!/usr/bin/env python3
"""Census (#6052): every go build of a release or an installer carries -trimpath, so a stack trace in a shipped program holds no absolute path of the
machine that built it. The files that build what ships are read, line continuations joined; each `go build` command in them must have -trimpath, except the
stand-in agent the smoke test builds for itself (it ships nothing). Each file has a floor: a rewrite that moves a build out of sight would otherwise make the
census find nothing and pass."""
import os
import re
import sys
import unittest

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..")
# Not listed on purpose: encryption-verify.yml builds the opt-in sqlcipher variant for verification only and uploads nothing that ships; a step there that
# ships a program needs -trimpath and an entry below.
# file -> the least number of go build commands it holds today (a floor per file, not a total)
FILES = {
    ".github/workflows/release.yml": 8,
    ".github/workflows/release-cache-warm.yml": 5,   # warms the Go build cache the release reuses: -trimpath changes the compile action ids, so it must match
    ".github/workflows/windows-tests.yml": 3,   # the CI package build, kept in step with release.yml (windows_build_census_test.py)
    "installers/build-linux.sh": 1,
    "installers/build-macos.sh": 6,
    "installers/build-windows.sh": 5,
    "tools/build_windows.ps1": 7,
    "coral-go/Makefile": 2,
}
BUILD = re.compile(r"\bgo build\b(?=\s+(?:-|\$|\.|\\))")


def commands(text):
    """(line number, the command text) of each go build, a `\\` continuation joined; comments and strings of messages skipped."""
    lines, out, i = text.split("\n"), [], 0
    while i < len(lines):
        line = lines[i]
        if line.strip().startswith("#") or not BUILD.search(line) or re.search(r"(?:throw|echo|Write-Host)\b.*go build", line):
            i += 1
            continue
        start, cmd = i + 1, line
        while cmd.rstrip().endswith("\\") and i + 1 < len(lines):
            i += 1
            cmd = cmd.rstrip()[:-1] + " " + lines[i].strip()
        out.append((start, cmd))
        i += 1
    return out


class T(unittest.TestCase):
    def test_every_release_and_installer_go_build_has_trimpath(self):
        missing = []
        for f, floor in FILES.items():
            cmds = [(n, c) for n, c in commands(open(os.path.join(ROOT, f)).read()) if "mock-agent" not in c]
            self.assertGreaterEqual(len(cmds), floor, f"{f}: found {len(cmds)} go build commands, the floor is {floor}: the census can no longer see them")
            missing += [f"{f}:{n}" for n, c in cmds if "-trimpath" not in c]
        self.assertEqual(missing, [], "go build without -trimpath (a shipped binary would carry the builder's absolute paths): " + ", ".join(missing))

    def test_the_census_itself_sees_a_missing_flag_and_a_continuation(self):
        self.assertEqual([n for n, c in commands("x\n  go build -tags a -o b ./cmd/x\n") if "-trimpath" not in c], [2])
        self.assertEqual(len(commands("CGO=0 go build -trimpath \\\n  -tags a \\\n  -o b ./c\n")), 1)
        self.assertEqual([n for n, c in commands("go build \\\n  -tags a ./c\n") if "-trimpath" not in c], [1])
        self.assertEqual(commands("# go build -o x ./c\nthrow \"go build ./cmd/x failed\"\n"), [])


if __name__ == "__main__":
    unittest.main()
