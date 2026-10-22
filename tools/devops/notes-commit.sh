#!/usr/bin/env bash
# tools/devops/notes-commit.sh VER NOTES.md GATED_SHA: make the "release notes: vVER" commit on the current HEAD, which must be GATED_SHA, the commit the heavy
# gates ran on. The commit message carries the trailer "Gated-commit: <GATED_SHA>", which tests/release/release_provenance.py reads: the release is then judged
# on exactly that commit, by name, not on whichever gate run is newest (#6067). The identity is the role's, set in the command (never the global config).
set -euo pipefail
V=${1:?usage: notes-commit.sh VER NOTES.md GATED_SHA}; NOTES=${2:?}; GATED=${3:?}
[[ "$GATED" =~ ^[0-9a-f]{40}$ ]] || { echo "notes-commit: GATED_SHA must be the full 40-hex commit" >&2; exit 1; }
[ "$(git rev-parse HEAD)" = "$GATED" ] || { echo "notes-commit: HEAD is not $GATED: the notes go on the gated commit itself" >&2; exit 1; }
[ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "notes-commit: the working tree is not clean" >&2; exit 1; }
python3 "$(dirname "$0")/insert-notes.py" RELEASE_NOTES.md "$NOTES" "$V"
[ "$(git diff --name-only)" = RELEASE_NOTES.md ] || { echo "notes-commit: more than RELEASE_NOTES.md changed" >&2; exit 1; }
GIT_COMMITTER_NAME='DevOps Engineer' GIT_COMMITTER_EMAIL=devops-engineer@example.invalid \
  git -c user.name='DevOps Engineer' -c user.email=devops-engineer@example.invalid commit -q -a -m "release notes: v$V

Gated-commit: $GATED

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
git log -1 --format='%h %an|%ae|%cn|%ce %s'
