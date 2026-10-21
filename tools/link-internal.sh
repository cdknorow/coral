#!/usr/bin/env bash
# tools/link-internal.sh: link the private notes repository's agent runbooks into this checkout (#5541).
#
# The release skill (.claude/skills/release.md) and the slash commands (.claude/commands/) live in the private repository subgentic/coral-internal, which is cloned
# next to this one as ../coral-internal. Claude Code reads them from this checkout's .claude/, so this script links them there. The links are untracked (.gitignore lists them).
#
#   tools/link-internal.sh            # link .claude/skills and .claude/commands to ../coral-internal
#   tools/link-internal.sh --check    # only say what is and is not linked
#   CORAL_INTERNAL=/path/to/coral-internal tools/link-internal.sh
set -u
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
internal="${CORAL_INTERNAL:-$root/../coral-internal}"
check=0; [ "${1:-}" = "--check" ] && check=1
if [ ! -d "$internal/.claude" ]; then
  echo "link-internal: no $internal/.claude. Clone git@github.com:subgentic/coral-internal.git next to this checkout (or set CORAL_INTERNAL)." >&2
  exit 1
fi
internal="$(cd "$internal" && pwd)"
rc=0
for d in skills commands; do
  link="$root/.claude/$d"; target="$internal/.claude/$d"
  [ -d "$target" ] || { echo "link-internal: $target is missing" >&2; rc=1; continue; }
  if [ -L "$link" ] && [ "$(readlink "$link")" = "$target" ]; then echo "linked   .claude/$d -> $target"; continue; fi
  if [ "$check" = 1 ]; then echo "not linked: .claude/$d"; rc=1; continue; fi
  if [ -e "$link" ] || [ -L "$link" ]; then echo "link-internal: .claude/$d exists and is not this link; move it away first" >&2; rc=1; continue; fi
  mkdir -p "$root/.claude" && ln -s "$target" "$link" && echo "linked   .claude/$d -> $target"
done
exit $rc
