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
   - downloads every asset, retrying each until it matches GitHub's digest;
   - runs `tools/verify-release.sh --local` on them. It needs cosign on PATH.

   Edit the body only after verify-release has passed, since its notes row compares the body with the tag's notes.

6. **Cask**: `tools/devops/cask.sh X.Y.Z` makes the one `cask: Coral X.Y.Z` commit on `main`.

## Rules these encode

- Test servers and test runs never use `~/.coral` or a running Coral. `CORAL_CLOUD_URL` is unset, and go test runs in
  a short `/tmp` dir, because a long TMPDIR breaks tmux socket paths.
- The landing identity is set per command (`CORAL_LAND_NAME` and `CORAL_LAND_EMAIL`, by default "Coral DevOps
  stack"). The global git identity is never used.
- Stage by explicit path, never amend or rebase `main`, and remove temp folders by exact path.
