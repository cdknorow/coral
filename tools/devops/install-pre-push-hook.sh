#!/usr/bin/env bash
# tools/devops/install-pre-push-hook.sh: install tools/devops/pre-push-hook.sh as the shared pre-push hook (#5444). Run once per clone;
# every worktree of the clone uses it. It refuses to replace a hook it did not install.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
hooks="$(git rev-parse --git-common-dir)/hooks"; mkdir -p "$hooks"
dst="$hooks/pre-push"
if [ -e "$dst" ] && ! grep -q '#5444' "$dst"; then echo "install-pre-push-hook: $dst exists and is not ours; not replaced" >&2; exit 1; fi
cp "$here/pre-push-hook.sh" "$dst"; chmod +x "$dst"
# the checker and its allowlist go beside the hook: the hook never reads them from a working tree
mkdir -p "$hooks/coral-5444"; cp "$here/check_binaries.py" "$here/binary-allowlist.txt" "$hooks/coral-5444/"
echo "installed $dst (checker in $hooks/coral-5444)"
