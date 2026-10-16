# The epoch test machine

Heavy test runs (the full `go test`, the CDP browser suites, stress, `-race`, fuzz soaks, the latency guard) run on the
shared Linux server `epoch`, never on a laptop. Run them one at a time at `nice -n 10`, and say on the board what you
start and when it ends. None of these tools is on epoch's default `PATH`; put them there yourself:

```bash
export PATH="$HOME/node-install/node/bin:$HOME/go-install/go/bin:$PATH"
export CORAL_CHROME_BIN=/sd2/$USER/coralgo-tools/chrome-linux64/chrome
unset CORAL_CLOUD_URL CORAL_DATA_DIR CORAL_URL TMUX   # a test server must never reach the real Hub or a real data dir
```

## Where a run keeps its data: /sd2, never /

epoch's root disk (`/`, which holds `/tmp` and `$HOME`) is small and nearly full, while `/sd2` has hundreds of GB free. Every
run puts its temp data, seeded data dirs, Chrome profiles and worktrees under `/sd2/$USER/<your-role>-tmp` (for example
`/sd2/$USER/qa-tmp`; DevOps' scripts use `/sd2/$USER/coralgo-tmp`), never under `/tmp` or `$HOME`, and deletes them on exit:

```bash
export TMPDIR=/sd2/$USER/<your-role>-tmp; mkdir -p "$TMPDIR"
work=$(mktemp -d "$TMPDIR/run.XXXXXX"); trap 'rm -rf -- "$work"' EXIT   # Go tests, Chrome and mktemp all follow TMPDIR
```

Everyone on the team runs as the same epoch user, so the owner column of `ls` can't tell whose a folder is. Name yours
after your role, and remove only your own, by exact path.

| Tool | Where | Version | How it was checked |
|---|---|---|---|
| Go | `$HOME/go-install/go/bin/go` | go1.24.1, which switches itself to `coral-go/go.mod`'s toolchain (go1.26.9) on first use (`GOTOOLCHAIN`) | the official go.dev tarball; the switched toolchain is checked against the Go checksum database |
| Node.js | `$HOME/node-install/node/bin/node` (`node` -> `node-v22.23.3-linux-x64`) | v22.23.3 (LTS) | `SHASUMS256.txt` from nodejs.org, its signature checked with the releaser's key from [nodejs/release-keys](https://github.com/nodejs/release-keys); the tarball's sha256 matches it. No sudo. |
| Chrome (headless) | `/sd2/$USER/coralgo-tools/chrome-linux64/chrome` | Chrome for Testing 155.0.8059.39 | Google's Chrome for Testing build for linux64 |
| Frontend test dependencies | `/sd2/$USER/coralgo-cache/frontend/node_modules` | `tests/frontend/package-lock.json` | `npm ci --ignore-scripts`; link it as `tests/frontend/node_modules` |

`/sd2/$USER/coralgo-tools/node` holds the same Node build (byte-identical `node` binary), which the CDP runs use.

## Reinstalling Node

```bash
V=v22.23.3; D=$HOME/node-install; mkdir -p "$D/dl" && cd "$D/dl"
curl -fsSLO "https://nodejs.org/dist/$V/node-$V-linux-x64.tar.xz" \
     -fsSLO "https://nodejs.org/dist/$V/SHASUMS256.txt" -fsSLO "https://nodejs.org/dist/$V/SHASUMS256.txt.sig"
# the signing key: keyid from `gpg --list-packets SHASUMS256.txt.sig`, the full fingerprint from
# https://raw.githubusercontent.com/nodejs/release-keys/main/keys.list, the key from .../keys/<fingerprint>.asc
export GNUPGHOME="$D/dl/gnupg"; mkdir -p -m 700 "$GNUPGHOME"; gpg --import signer.asc
gpg --verify SHASUMS256.txt.sig SHASUMS256.txt                      # must say "Good signature"
grep " node-$V-linux-x64.tar.xz\$" SHASUMS256.txt | sha256sum -c -  # must say OK
cd "$D" && tar -xJf "dl/node-$V-linux-x64.tar.xz" && ln -sfn "node-$V-linux-x64" node
```

## The stress runners that need Node and Chrome

- `tests/stress/run_agent_tasks.sh`: its assignment editor check runs `run_assignment_browser.sh`.
- `tests/stress/run_kill_all.sh`.

Give each its own port (`CORAL_TEST_PORT`, never 8420); they build Coral and start their own isolated server and tmux socket.
