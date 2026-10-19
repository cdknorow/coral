# Landing and release scripts

The scripts the landing agent uses to assemble a `ci/<version>` batch, gate it, land it on `main` and publish the
release. They live here, not in a scratchpad, so a restarted session cannot lose a check (#4605). Every script stops on
the first failed check.

## The flow

1. **Assemble** the batch in a worktree on current `main`, from the approved patches, in the order given:

   ```bash
   git worktree add --detach /tmp/wt-v1491 origin/main
   tools/devops/apply-patches.sh /tmp/wt-v1491 p1491/*.patch
   ```

   Patches go in with `git am --3way --keep-cr`. Without `--keep-cr`, git strips the carriage returns that raw terminal
   captures need: v1.4.87 lost six PTY fixtures that way. Each patch is scanned for personal data and binaries first; set
   `CORAL_ALLOW_BINARY=1` for a patch whose binaries are known and reviewed. A conflict on the `test:cloud` line of
   `tests/frontend/package.json` is resolved as the union of both sides; any other conflict stops for a hand resolution.

2. **Pre-push**, the checks before the gate. `tools/devops/prepush.sh /tmp/wt-v1491` runs:
   - gofmt with the module's own toolchain;
   - build, plus vet with the default tags, `dev fts5` and `GOOS=windows`;
   - the route regenerators, which must leave no diff;
   - `go test ./...` on both tag sets;
   - `test:cloud`.

3. **Gate**: push the worktree to `ci/<version>` (that starts `cdp-gate` and `gofmt`), then dispatch `windows-tests` on it:

   ```bash
   git -C /tmp/wt-v1491 push origin HEAD:refs/heads/ci/v1491
   gh workflow run windows-tests --ref ci/v1491
   ```

   The gate is green only when all eight cdp-gate jobs are green and the summary's first line says `green:`. The #4354
   end check must be quiet in every job. A flaky or failed suite's first failing line is printed under its name in the
   summary.

   After a green gate, refresh the suite durations the six parts are balanced by (#4605). The landed tree must be the
   gated tree, so the refreshed file is the first commit of the next batch (one `tests: suite_durations.json from the
   vX.Y.Z gate` commit, and only when the file changed), and that batch's gate runs with it:

   ```bash
   gh run download <cdp-gate run id> -D /tmp/gate-v1491
   (cd tests/frontend && node update_suite_durations.mjs /tmp/gate-v1491/cdp-{0,1,2,3,4,5}-a*/results.json)
   ```

   Do this after every release gate: it had not been run since v1.4.94, and by v1.4.101 the six parts took 233 to 369 s
   instead of about equal times (#4956). The parts are only as even as these numbers are fresh.

   `cdp-0` to `cdp-5` are the six parts (`cdp-6` the sequences and `cdp-7` the strict phase, which it ignores anyway).
   Only passing first attempts count, and suites missing from the run keep their old value.

4. **Notes and tag**:
   - Write the notes section in a file starting with `## vX.Y.Z — `, and add it on top of the gated head:
     `python3 tools/devops/insert-notes.py RELEASE_NOTES.md notes.md X.Y.Z`. Commit that file alone.
   - Land with `tools/devops/land-main.sh <old main> <notes commit> <gated tree>`. It checks that `main` is a
     fast-forward, that the parent's tree is the gated tree, and that the top commit is notes-only. It syncs the shared
     checkout's files with their modes, and stops if anyone else edited one of them.
   - Then `git tag vX.Y.Z <notes commit>` and push the tag. The release workflow builds, signs and publishes.

5. **Finish**: after the release workflow succeeds, run `tools/devops/finish-release.sh X.Y.Z`. It:
   - sets the release body from the tag's notes;
   - starts the runtime check;
   - runs `tools/verify-release.sh`, which takes every file from the release run's artifacts and checks each against
     GitHub's digest of the published asset, so it verifies the published files without downloading them. It needs
     cosign on PATH.

   Edit the body only after verify-release has passed, since its notes row compares the body with the tag's notes.

6. **Cask**: `tools/devops/cask.sh X.Y.Z` makes the one `cask: Coral X.Y.Z` commit on `main`. It reads the DMG's
   sha256 from the release's metadata, not by downloading anything.

## Downloads from a release by our own automation

A download of a release asset counts in the release's download numbers, so our own checks don't download them (#4936).
They read the same bytes from the release run's artifacts (`release.yml` keeps every package, plus `release-sums`
with `SHA256SUMS.txt` and the Sigstore bundles), and prove those bytes are the published ones with GitHub's digest of
each asset, which is release metadata, not a download.

Expected downloads by our automation per release: **none**.

| Step | Reads | Downloads from the release |
|---|---|---|
| `release.yml` update-tap job | `release-sums` artifact of the same run | none |
| Runtime check (`standard-runtime-verify.yml`) | `coral-linux-<arch>` artifacts of the release run | none |
| `tools/verify-release.sh` (default) | the release run's artifacts, and the previous release's run for the entry counts | none |
| `tools/devops/cask.sh` | the DMG's digest from the release metadata | none |

What still downloads, and when:

- `tools/verify-release.sh` on a release made before #4936, whose run has no `release-sums` artifact: `SHA256SUMS.txt`
  and the three Sigstore bundles (4 small files, no packages).
- `tools/verify-release.sh --published` and `tools/devops/download-release.sh`: every asset (9 files), plus, with
  `--published`, the previous release's two Linux tarballs and portable zip. Only when someone runs them on purpose, to
  check exactly what a user gets, or after the run's artifacts have expired.
- `windows-runner-dryrun.yml`: one portable zip of the version it is given, on a manual run or a `ci/` push that
  changes `tests/release/smoke_windows.ps1` or the workflow.

If a release run's artifacts have expired, the runtime check and verify-release fail with a message that says so; they
don't fall back to downloading.

Our own runs also send no usage events. Every CI workflow that builds or runs Coral, and every test runner and epoch
script, sets `CORAL_TELEMETRY_DISABLED=1`. CI's own builds carry no PostHog key anyway; only `release.yml` and
`installers/build-macos.sh` put one in.

## Rules these encode

- Test servers and test runs never use `~/.coral` or a running Coral. `CORAL_CLOUD_URL` is unset, and go test runs in
  a short `/tmp` dir, because a long TMPDIR breaks tmux socket paths.
- The landing identity is set per command (`CORAL_LAND_NAME` and `CORAL_LAND_EMAIL`, by default "Coral DevOps
  stack"). The global git identity is never used.
- Stage by explicit path, never amend or rebase `main`, and remove temp folders by exact path.
