Publish a new release of Coral. The user provides the version number as $ARGUMENTS (e.g. "1.4.61").

Coral is released by pushing a `v$ARGUMENTS` tag; the release workflow builds, signs and publishes every package. Follow `.claude/skills/release.md` (`/release $ARGUMENTS`):

1. Check that main's CI is green on the commit to tag
2. Add the `## v$ARGUMENTS` section to `RELEASE_NOTES.md` and commit it
3. Ask for confirmation, then tag and push the tag
4. Wait for the release workflow, then set the release body from the notes
5. Verify the published packages (signatures, attestations, the notarized DMG, the Homebrew tap, the runtime check) and bump `Casks/coral.rb`
6. Post the release URL and the results
