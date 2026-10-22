#!/usr/bin/env python3
"""windows-tests' package build is release.yml's Windows build (#5886, DevOps's note on #5804).

The package job in .github/workflows/windows-tests.yml builds the Windows programs the way release.yml's "Build Windows
binaries" step does, so a break in the shipped build shows on every Windows run, not at a tag. A copy drifts, so this
census holds the two together:

  - the build tags are the release's own default (resolve_release.sh: build_tags=fts5);
  - the GUI programs (cgo, -H windowsgui) are exactly gui_commands.txt in both builds, each with the same tags (coral-app
    adds "webview"), and windows-tests reads that list from the file rather than spelling it;
  - windows-tests reads the other programs from commands.txt, and both builds compile them with cgo off;
  - the same GOOS/GOARCH, strip flags and Version flag, and the same bundle check after the build.

It runs in windows-tests (its package job) and in release.yml's resolve job. The checks are also run against edited
copies of the two files, each of which must fail (a census that cannot fail is no census).
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def program_list(path):
    return [l.strip() for l in path.read_text().splitlines() if l.strip() and not l.lstrip().startswith("#")]


def block(text, start, what):
    """The step that begins at the line holding `start`, up to the next step."""
    i = text.find(start)
    if i < 0:
        raise SystemExit(f"FAIL: no step {what!r} ({start!r}) found")
    j = text.find("\n      - name:", i + 1)
    return text[i:j if j > 0 else len(text)]


def go_builds(step):
    """{program: (tags, windowsgui)} of the step's `go build` lines, with release.yml's $webviewTags replaced by its definition."""
    d = re.search(r'\$webviewTags = ("[^"]*")', step)  # release.yml names coral-app's tags once and uses the name
    if d:
        step = re.sub(r"\$webviewTags\b", lambda _: d.group(1), step)
    out = {}
    for m in re.finditer(r'go build\s+-trimpath\s+-tags\s+("[^"]*"|\S+)\s+-ldflags\s+"([^"]*)"\s+-o\s+"[^"]*"\s+"\./cmd/([^/"]+)/?"', step):
        tags, ldflags, cmd = m.group(1).strip('"'), m.group(2), m.group(3)
        out[cmd] = (tags, "-H windowsgui" in ldflags)
    return out


def check(wt, rel, resolve, commands, gui):
    errs = []
    want_tags = re.search(r"^build_tags=(\S+)", resolve, re.M)
    if not want_tags:
        return ["resolve_release.sh: no default build_tags= line"]
    want_tags = want_tags.group(1)

    wt_step = block(wt, "- name: Build the package", "windows-tests: Build the package")
    rel_step = block(rel, "- name: Build Windows binaries (amd64)", "release.yml: Build Windows binaries")

    m = re.search(r'\$buildTags = "([^"]*)"', wt_step)
    if not m or m.group(1) != want_tags:
        errs.append(f"windows-tests builds with tags {m.group(1) if m else None!r}, the release's default is {want_tags!r}")
    if "gui_commands.txt" not in wt_step or not re.search(r"\$gui = Get-Content [^\n]*gui_commands\.txt", wt_step):
        errs.append("windows-tests must read its GUI programs from tests/release/gui_commands.txt, not spell them")
    if not re.search(r"\$commands = Get-Content [^\n]*[\\/]commands\.txt", wt_step):
        errs.append("windows-tests must read its programs from tests/release/commands.txt")

    wt_b, rel_b = go_builds(wt_step), go_builds(rel_step)
    wt_gui = {c: v for c, v in wt_b.items() if v[1]}
    rel_gui = {c: v for c, v in rel_b.items() if v[1]}
    for name, got in (("windows-tests", wt_gui), ("release.yml", rel_gui)):
        if sorted(got) != sorted(gui):
            errs.append(f"{name} builds the GUI programs {sorted(got)} with -H windowsgui; gui_commands.txt lists {sorted(gui)}")
        for cmd, (tags, _) in got.items():
            want = "webview,$buildTags" if cmd == "coral-app" else "$buildTags"
            if tags != want:
                errs.append(f"{name} builds {cmd} with tags {tags!r}, expected {want!r}")
    if wt_gui != rel_gui:
        errs.append(f"the GUI builds differ: windows-tests {wt_gui}, release.yml {rel_gui}")

    for name, step in (("windows-tests", wt_step), ("release.yml", rel_step)):
        for needle, why in (
            ('$env:GOOS = "windows"', "GOOS"),
            ('$env:GOARCH = "amd64"', "GOARCH"),
            ('$env:CGO_ENABLED = "0"', "cgo off for the command-line programs"),
            ('$env:CGO_ENABLED = "1"', "cgo on for the GUI programs"),
            ("-s -w", "the strip flags"),
            ("internal/config.Version=", "the Version flag"),
            ("verify_standard_bundle.ps1", "the bundle check"),
        ):
            if needle not in step:
                errs.append(f"{name}'s build lacks {why} ({needle!r})")
        # the command-line programs are compiled before CGO is turned on, in a loop, never the GUI ones with cgo off
        i0, i1 = step.find('$env:CGO_ENABLED = "0"'), step.find('$env:CGO_ENABLED = "1"')
        if i0 >= 0 and i1 >= 0 and not i0 < i1:
            errs.append(f"{name} turns cgo on before it builds the command-line programs with it off")
    # Every place the MSI is built says -arch x64: without it the package is a 32-bit one and installs into Program Files (x86) (#6004's W1).
    for name, text in (("release.yml", rel), ("windows-tests.yml", wt)):
        lines = [l for l in text.splitlines() if re.search(r"\bwix build\b", l) and "coral.wxs" in l]
        if not lines:
            errs.append(f"{name} has no 'wix build ... coral.wxs' line: the census cannot see the MSI build")
        for l in lines:
            if not re.search(r"-arch\s+x64\b", l):
                errs.append(f"{name} builds the MSI without -arch x64: {l.strip()[:80]}")
    # every command is built by the loop in both (neither names one by hand)
    named = [c for c in commands if f"./cmd/{c}/" in wt_step and c not in gui]
    if named:
        errs.append(f"windows-tests names programs by hand instead of reading commands.txt: {named}")
    return errs


# The first release whose MSI is an x64 package (-arch x64 everywhere, #6009's W1; v1.4.157). smoke_windows.ps1 refuses a 32-bit MSI, so the
# "Windows runner dry run" workflow, which smoke-tests a PUBLISHED release, must not default to an older one (its MSI is 32-bit and the
# smoke test rightly fails it: the 158 gate's red). Move FIRST_X64_RELEASE only when the MSI build itself changes.
FIRST_X64_RELEASE = (1, 4, 157)


def check_dryrun(dry):
    """Errors in .github/workflows/windows-runner-dryrun.yml's default release (the dispatch input's and the push fallback's)."""
    errs = []
    inputs = re.findall(r'default:\s*"(\d+)\.(\d+)\.(\d+)"', dry)
    fallbacks = re.findall(r"\|\|\s*'(\d+)\.(\d+)\.(\d+)'", dry)
    if len(inputs) != 1 or len(fallbacks) != 1:
        return ["windows-runner-dryrun.yml: its default release is not found in both places (the input's default and VERSION's fallback)"]
    a, b = tuple(map(int, inputs[0])), tuple(map(int, fallbacks[0]))
    if a != b:
        errs.append(f"windows-runner-dryrun.yml: the input's default {'.'.join(map(str, a))} and VERSION's fallback {'.'.join(map(str, b))} differ")
    for v in (a, b):
        if v < FIRST_X64_RELEASE:
            errs.append(f"windows-runner-dryrun.yml defaults to v{'.'.join(map(str, v))}, whose MSI is a 32-bit package: smoke_windows.ps1 fails it "
                        f"(the first release with an x64 MSI is v{'.'.join(map(str, FIRST_X64_RELEASE))})")
    if re.search(r"\bwix build\b", dry):
        errs.append("windows-runner-dryrun.yml builds an MSI: it must only download a published one (or be added to the -arch x64 census above)")
    return errs


def main():
    wt = (ROOT / ".github/workflows/windows-tests.yml").read_text()
    rel = (ROOT / ".github/workflows/release.yml").read_text()
    resolve = (ROOT / "tests/release/resolve_release.sh").read_text()
    commands = program_list(ROOT / "tests/release/commands.txt")
    gui = program_list(ROOT / "tests/release/gui_commands.txt")

    dry = (ROOT / ".github/workflows/windows-runner-dryrun.yml").read_text()

    errs = check(wt, rel, resolve, commands, gui) + check_dryrun(dry)
    for e in errs:
        print("FAIL:", e)

    # Each edit below is a drift the census must see.
    mutations = [
        ("windows-tests tags", lambda w, r, g: (w.replace('$buildTags = "fts5"', '$buildTags = "fts5,dev"'), r, g)),
        ("windows-tests spells the GUI list", lambda w, r, g: (re.sub(r"\$gui = Get-Content [^\n]*", '$gui = @("coral-tray", "coral-app")', w), r, g)),
        ("a third GUI program", lambda w, r, g: (w, r, g + ["coral-extra"])),
        ("release.yml drops coral-app's webview tag", lambda w, r, g: (w, r.replace("$webviewTags = \"webview,$buildTags\"", "$webviewTags = \"$buildTags\""), g)),
        ("windows-tests drops the webview tag", lambda w, r, g: (w.replace('-tags "webview,$buildTags"', "-tags $buildTags"), r, g)),
        ("windows-tests drops -H windowsgui from the tray", lambda w, r, g: (w.replace('-ldflags "-H windowsgui $ldflags" -o "..\\dist\\windows\\bin\\coral-tray.exe"', '-ldflags "$ldflags" -o "..\\dist\\windows\\bin\\coral-tray.exe"'), r, g)),
        ("release.yml drops the bundle check", lambda w, r, g: (w, r.replace("verify_standard_bundle.ps1", "x.ps1"), g)),
        ("release.yml builds the MSI as a 32-bit package", lambda w, r, g: (w, r.replace("-arch x64 ", ""), g)),
        ("windows-tests builds the MSI as a 32-bit package", lambda w, r, g: (w.replace("-arch x64 ", ""), r, g)),
        ("windows-tests names no commands.txt", lambda w, r, g: (w.replace("commands.txt", "other.txt"), r, g)),
    ]
    for name, mut in mutations:
        w, r, g = mut(wt, rel, list(gui))
        if w == wt and r == rel and g == gui:
            errs.append(f"mutation {name!r} changed nothing: the census test is out of date with the workflows")
            print("FAIL: mutation changed nothing:", name)
            continue
        if not check(w, r, resolve, commands, g):
            errs.append(f"the census did not notice: {name}")
            print("FAIL: the census did not notice:", name)
    dry_mutations = [
        ("the dry run's input defaults to a 32-bit release", dry.replace('default: "1.4.157"', 'default: "1.4.153"', 1)),
        ("the dry run's push fallback is a 32-bit release", dry.replace("|| '1.4.157'", "|| '1.4.153'", 1)),
        ("the dry run's two defaults differ", dry.replace("|| '1.4.157'", "|| '1.4.158'", 1)),
        ("the dry run builds an MSI", dry + "\n          wix build tools\\coral.wxs -o x.msi\n"),
    ]
    for name, text in dry_mutations:
        if text == dry:
            errs.append(f"mutation {name!r} changed nothing: the census test is out of date with the workflow")
            print("FAIL: mutation changed nothing:", name)
        elif not check_dryrun(text):
            errs.append(f"the census did not notice: {name}")
            print("FAIL: the census did not notice:", name)
    if errs:
        sys.exit(1)
    tags = re.search(r"^build_tags=(\S+)", resolve, re.M).group(1)
    print(f"ok: windows-tests' package build and release.yml's Windows build agree ({len(gui)} GUI programs, {len(commands)} commands, tags {tags}); {len(mutations) + len(dry_mutations)} drifts detected")


if __name__ == "__main__":
    main()
