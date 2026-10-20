# tests/release

Checks the release workflow and the release manager run on packages.

## smoke_macos.sh: the post-release smoke of the macOS build

Run it after `tools/verify-release.sh vX.Y.Z` has passed (#5256, QA's #5253 as a script):

```bash
tests/release/smoke_macos.sh v1.4.123            # downloads the DMG and SHA256SUMS.txt with gh
tests/release/smoke_macos.sh --dmg Coral.v1.4.123.dmg --sums SHA256SUMS.txt [--keep]
```

It runs the RELEASE binaries from the DMG's app, never the installed app and never `~/.coral`: a fresh `HOME`
and data folder under `/tmp/coral-macsmoke.*`, random ports, `CORAL_CLOUD_URL=off`, and a stand-in CLI
(`smoke_macos/standin_cli.py`) instead of a real agent. The tray's and the server's HTTP(S) goes through
`smoke_macos/refusing_proxy.py`, which refuses and logs, so nothing leaves the machine. Every process is stopped by
the PID the script recorded and the folder is removed by its own path (`--keep` leaves it).

| Item | What it checks |
|---|---|
| 1 dmg | the DMG's sha256 is the one in `SHA256SUMS.txt`; the app is codesign-valid and Notarized Developer ID |
| 2 loopback | the tray starts the server, which listens on 127.0.0.1 only |
| 3 analytics | a control run is seen reaching the analytics host; with `telemetry_enabled=false` saved, the tray and the server never do |
| 4 comments | an artifact comment is saved with its quote and the owner as author, and is read back |
| 5 multiselect | the stand-in CLI's multi-select question: toggles read back from its terminal, a stale label gets 409, Submit reaches the CLI, which got exactly the ticked boxes |
| 6 settings | an operator-shaped set written by older versions (`True`, legacy keys) is read: privacy, launch defaults, the legacy key removed, values kept |

The output is one table; the exit status is 0 only when every item passes, and a failing item is named on stderr.
A check that cannot run (no app because item 1 failed, the tray did not start) is a FAIL, not a skip.
Item 3 fails if its own control sees nothing, so a proxy that cannot see the analytics host cannot pass it.

Not covered here: how the dashboard draws the card and the comments panel (a browser is needed). That is
`tests/frontend` (the CDP suites). This smoke covers the released binaries and the server's side of both.
Needs macOS, `curl`, `sqlite3`, `lsof`, `python3`, `hdiutil`, `codesign`, `spctl`, and `gh` for a TAG.

## smoke_linux.sh: the release smoke of a Linux tarball

release.yml runs it on each architecture's own native runner right after the tarball is made, so the file that is published is the file that is tested
(`linux-runner-dryrun.yml` runs it on the dry-run tarball); by hand: `tests/release/smoke_linux.sh dist/coral-linux-amd64-1.4.130.tar.gz --version 1.4.130 [--keep]`.
Like the macOS smoke it uses a fresh HOME and data folder under `/tmp/coral-linsmoke.*`, a random port, `CORAL_CLOUD_URL=off`, the stand-in CLI
(`smoke_macos/standin_cli.py`, shared), the PIDs it recorded for every stop, one table and a non-zero exit naming the failing item.

| Item | What it checks |
|---|---|
| 1 package | the tarball unpacks inside its folder (no absolute or `..` names); every program of `commands.txt` is there, executable, and an ELF for this machine (amd64 or arm64) |
| 2 version | `coral --version` prints the version given with `--version` |
| 3 serve | `/api/health` and the dashboard page answer, the live list answers 200 with the key and 401 without, and the port listens on loopback only |
| 4 launch | a launch with no agent type starts the stand-in under the pty backend; its hooks make it `waiting: question`; the question is answered through the API (toggles read back from its terminal, Submit); it received exactly the boxes ticked |
| 5 stop | SIGTERM stops the server within 20 s (exit status 0 or 143), its port is closed, and the stand-in, which exits when its terminal closes as a real CLI does, is gone within 5 s |

Needs Linux, bash, curl, python3 and tar. The stand-in ends when its terminal goes: an agent that never reads its terminal can outlive a stopped server
(its parent shell is killed, it is orphaned and ignores the SIGHUP the server's own `signal.Ignore` handed down), which is not a failure of this check.

**A smoke failure on a tag is fixed forward in the next version** (#5306): the failing item names what to fix, the tag is never re-used or re-tagged (the `v*` tags cannot be moved), and the step is never re-run by hand to get past it.

## smoke_windows.ps1: the release smoke of the Windows build

`tests/release/smoke_windows.ps1 -Zip Coral-<version>-x64-portable.zip [-Msi Coral-<version>-x64.msi] -Version <version> [-StandIn <exe>]` runs the shipped programs from the portable ZIP in an isolated data folder under strict local auth with `--cloud-url off`: `/api/health` is 200 and a request with no credential is 401, `coral api-key` and a keyed request, `coral-board` joins, posts and reads as a person, `coral setup-code` prints a code, the ZIP holds every program of `commands.txt` and `gui_commands.txt` (each a 64-bit Windows executable) and `coral.exe --version` prints the version, with `-StandIn` a launch starts the stand-in under the pty backend and its process runs, and with `-Msi` the MSI is installed silently into a scratch folder, holds the programs, runs, and uninstalls. The server is stopped with Ctrl+C first, its port closes and no agent process is left. The release workflow runs it on the Windows build and `windows-runner-dryrun.yml` runs it by hand.

**Run it only on an ephemeral CI runner.** The MSI step changes the machine's PATH and writes the EULA marker (it removes both again), so it is not for a developer's own computer; the ZIP-only checks have no such effect.

As with the macOS and Linux smokes, a failure on a tag is fixed forward in the next version (#5306): the tag is never re-used and the step is never re-run by hand to get past it.
