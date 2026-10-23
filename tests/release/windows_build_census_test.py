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
    for m in re.finditer(r'go build\s+-trimpath\s+(?:-overlay\s+\S+\s+)?-tags\s+("[^"]*"|\S+)\s+-ldflags\s+"([^"]*)"\s+-o\s+"[^"]*"\s+"\./cmd/([^/"]+)/?"', step):
        tags, ldflags, cmd = m.group(1).strip('"'), m.group(2), m.group(3)
        out[cmd] = (tags, "-H windowsgui" in ldflags)
    return out


OWNER_ONLY = r'icacls \$overlayDir /inheritance:r /grant:r "\*\$\(\[Security\.Principal\.WindowsIdentity\]::GetCurrent\(\)\.User\.Value\):\(OI\)\(CI\)F"'


def overlay_errors(name, step, pattern=r'go build\s[^\n]*'):
    """What every Windows build script or step must do about the strip-static overlay (#6208): a flag on each go build line, an owner-only folder given to the generator, a finally that deletes it."""
    errs = []
    lines = re.findall(pattern, step)
    if not lines:
        errs.append(f"{name}: the census sees no go build line")
    for line in lines:
        if not re.search(r"-overlay\s+\$overlay\b", line):
            errs.append(f"{name} builds without the strip-static overlay: {line[:80]}")
    if not re.search(OWNER_ONLY, step):
        errs.append(f"{name}: the overlay folder is not made owner-only (icacls /inheritance:r with this user's SID, (OI)(CI)F)")
    if "go run ./tools/strip-static -out $overlayDir" not in step:
        errs.append(f"{name}: strip-static is not given the owner-only folder (-out $overlayDir)")
    if not re.search(r"finally \{ go run \./tools/strip-static -clean \$overlay \}", step):
        errs.append(f"{name}: the overlay folder is not deleted in a finally block (go run ./tools/strip-static -clean $overlay)")
    return errs


def check(wt, rel, resolve, commands, gui, local=None):
    errs = []
    want_tags = re.search(r"^build_tags=(\S+)", resolve, re.M)
    if not want_tags:
        return ["resolve_release.sh: no default build_tags= line"]
    want_tags = want_tags.group(1)

    wt_step = block(wt, "- name: Build the package", "windows-tests: Build the package")
    rel_step = block(rel, "- name: Build Windows binaries (amd64)", "release.yml: Build Windows binaries")

    # The dashboard ships minified (#6208): every Windows build line reads the strip-static overlay, which lives in a folder only the build's user can touch, and the
    # step deletes that folder by exact path even when a build fails. Before this the census accepted a build line with or without -overlay, so a build that lost it still passed.
    for name, step in (("windows-tests", wt_step), ("release.yml", rel_step)):
        errs += overlay_errors(name, step, r'go build\s[^\n]*"\./cmd/[^/"]+/?"')
    if local is not None:  # tools/build_windows.ps1, the local and portable build a person makes themselves
        errs += overlay_errors("tools/build_windows.ps1", local, r'(?m)^\s*go build\s[^\n]*')  # a line of code, not a comment that says "go build"

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


# smoke_windows.ps1 refuses a 32-bit MSI, and the releases before v1.4.157 have one (#6009's W1 made it x64). The "Windows runner dry run" workflow smoke-tests a
# PUBLISHED release, so a version pinned in it goes stale in the other direction too (it was v1.4.153: the 158 gate's red). It names no version of its own: with
# none given it resolves the latest published release, and it must only download a package, never build an MSI.
def check_dryrun(dry):
    """Errors in .github/workflows/windows-runner-dryrun.yml's choice of release."""
    errs = []
    if re.search(r'default:\s*"\d+\.\d+\.\d+"', dry):
        errs.append("windows-runner-dryrun.yml pins a default release for the dispatch input: it goes stale (use an empty default: the latest release)")
    if re.search(r"\|\|\s*'\d+\.\d+\.\d+'", dry):
        errs.append("windows-runner-dryrun.yml pins a fallback release for a push: it goes stale (the step resolves the latest release)")
    if "--exclude-drafts" not in dry:
        errs.append("windows-runner-dryrun.yml lists releases without --exclude-drafts")
    if "'^[0-9]+\\.[0-9]+\\.[0-9]+(-[A-Za-z0-9.]+)?$'" not in dry or "-notmatch" not in dry:
        errs.append("windows-runner-dryrun.yml does not check the version is a plain release version before it reaches gh")
    if "gh release list" not in dry or "--limit 30" not in dry:
        errs.append("windows-runner-dryrun.yml does not resolve the latest published release (gh release list --limit 30, filtered to product tags)")
    if 'select(.tagName | test("^v[0-9]"))' not in dry:
        errs.append("windows-runner-dryrun.yml does not skip promote-<tag>-<sequence> releases when it picks the newest release (#6394)")
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

    local = (ROOT / "tools/build_windows.ps1").read_text()

    errs = check(wt, rel, resolve, commands, gui, local) + check_dryrun(dry)
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
        ("windows-tests builds a program without the overlay", lambda w, r, g: (w.replace(" -overlay $overlay", "", 1), r, g)),
        ("release.yml builds a program without the overlay", lambda w, r, g: (w, r.replace(" -overlay $overlay", "", 1), g)),
        ("windows-tests keeps the overlay folder's inherited rights", lambda w, r, g: (w.replace("/inheritance:r", "/inheritance:e"), r, g)),
        ("release.yml keeps the overlay folder's inherited rights", lambda w, r, g: (w, r.replace("/inheritance:r", "/inheritance:e"), g)),
        ("windows-tests does not give strip-static its folder", lambda w, r, g: (w.replace("-out $overlayDir", ""), r, g)),
        ("release.yml leaves the overlay folder behind", lambda w, r, g: (w, r.replace("-clean $overlay", "-check $overlay"), g)),
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
    local_mutations = [
        ("build_windows.ps1 builds a program without the overlay", lambda s: s.replace(" -overlay $overlay", "", 1)),
        ("build_windows.ps1 keeps the overlay folder's inherited rights", lambda s: s.replace("/inheritance:r", "/inheritance:e")),
        ("build_windows.ps1 does not give strip-static its folder", lambda s: s.replace("-out $overlayDir", "")),
        ("build_windows.ps1 leaves the overlay folder behind", lambda s: s.replace("-clean $overlay", "-check $overlay")),
    ]
    for name, mut in local_mutations:
        m = mut(local)
        if m == local:
            errs.append(f"mutation {name!r} changed nothing: the census test is out of date with the script")
            print("FAIL: mutation changed nothing:", name)
        elif not check(wt, rel, resolve, commands, gui, m):
            errs.append(f"the census did not notice: {name}")
            print("FAIL: the census did not notice:", name)
    dry_mutations = [
        ("the dry run pins its input's default release", dry.replace('default: ""', 'default: "1.4.153"', 1)),
        ("the dry run pins a push fallback release", dry.replace("${{ github.event.inputs.version }}", "${{ github.event.inputs.version || '1.4.153' }}", 1)),
        ("the dry run stops resolving the latest release", dry.replace("gh release list", "gh release view", 1)),
        ("the dry run lists drafts too", dry.replace("--exclude-drafts ", "", 1)),
        ("the dry run would pick a promote release", dry.replace('select(.tagName | test("^v[0-9]"))', 'select(true)', 1)),
        ("the dry run stops validating the version", dry.replace("-notmatch", "-match", 1)),
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
    print(f"ok: windows-tests' package build and release.yml's Windows build agree ({len(gui)} GUI programs, {len(commands)} commands, tags {tags}); {len(mutations) + len(local_mutations) + len(dry_mutations)} drifts detected")


if __name__ == "__main__":
    main()
