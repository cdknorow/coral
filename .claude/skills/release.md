---
name: release
description: Tag a Coral release (vX.Y.Z) after a green main, then verify what the release workflow published.
user_invocable: true
arguments: version number (e.g. "1.4.61")
---

# Release Skill

Coral is released by pushing a version tag: `.github/workflows/release.yml` builds Linux (amd64 and arm64), Windows (MSI and portable ZIP) and macOS (a signed, notarized DMG), creates the GitHub Release with `SHA256SUMS.txt` and Sigstore bundles, attests provenance, and updates the Homebrew tap. Tag names and tiers are in CLAUDE.md ("Tagged Release (CI)"). The version is `$ARGUMENTS`.

**Stop and ask for confirmation before any public step**: pushing a commit to main, pushing a tag, editing the release.

## 1. Check main
- Main's CI (gofmt, cross-build, windows-tests) is green on the commit you will tag.
- The changes since the last tag have their reviews and one relevant green test run.

## 2. Release notes
Add a `## v$ARGUMENTS — <title>` section at the top of `RELEASE_NOTES.md` (below `# Release Notes`), in the style of the earlier sections (Security, Fixes, Other changes). Commit it on main as `release notes: v$ARGUMENTS`.

## 3. Tag
```bash
git tag -a v$ARGUMENTS -m "v$ARGUMENTS" <notes-commit>
git push origin refs/tags/v$ARGUMENTS
```
`tools/release.sh` does the same for the next patch version (`--dry-run` to preview).

## 4. Wait for the workflow, then set the release body
Wait until the run is `completed` (poll `gh run view <id> --json status,conclusion`; `gh run watch` can exit early), and act only on `success`. Then set the body from the notes section:
```bash
awk -v h="## v$ARGUMENTS " 'index($0,h)==1{f=1;next} f&&/^## v[0-9]/{exit} f' RELEASE_NOTES.md | awk 'NF{p=1} p' \
  | gh release edit v$ARGUMENTS --notes-file -
```

## 5. Verify what was published
The release home is per tag (tools/release-homes.txt: up to v1.4.141 `cdknorow/coral`, v1.4.142 and later `subgentic/coral`): `. tools/release-homes.sh; home=$(release_home_for_tag v$ARGUMENTS)`. Download the assets into an empty folder (`gh release download v$ARGUMENTS -R "$home"`), then:
- `cosign verify-blob` of `SHA256SUMS.txt` and both Linux tarballs against their `.sigstore.json` bundles (identity `https://github.com/$home/.github/workflows/release.yml@refs/tags/v…`, issuer `https://token.actions.githubusercontent.com`), and `shasum -a 256 -c SHA256SUMS.txt`.
- `gh attestation verify <file> --repo "$home"` for every package and `SHA256SUMS.txt`.
- The DMG: `spctl -a -vv -t exec` on the app (Notarized Developer ID), `xcrun stapler validate`, `coral --version` prints the version, no entitlements.
- The tap (`$RELEASE_TAP_REPO` of tools/release-homes.sh, today `cdknorow/homebrew-coral`; `Casks/coral.rb`) has the new version and the DMG's sha256; bump the in-repo `Casks/coral.rb` to match.
- Run the "Published Standard Linux runtime verification" workflow with `-f tag=v$ARGUMENTS`.

## 6. Report
Post the release URL, the tag and the results of step 5.
