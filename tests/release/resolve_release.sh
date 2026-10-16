#!/usr/bin/env bash
# resolve_release.sh TAG: what a release tag builds, as key=value lines for $GITHUB_OUTPUT (#3855).
# The one place for the tier logic: the build tags each job passes to go build, whether the
# GitHub release is a prerelease, the version, and the shipped program lists.
#   version       the tag without its leading v, suffix included
#   build_tags    fts5, plus the tier's tag: -forDropbox dropboxers, -beta beta, -dev dev
#                 (checked in that order, as release.yml always did); prod has none
#   prerelease    true for -dev, -beta, -rc and -forDropbox (never "Latest"), else false
#   commands      tests/release/commands.txt, space separated
#   gui_commands  tests/release/gui_commands.txt, space separated
set -euo pipefail
tag="${1:?usage: resolve_release.sh TAG}"
here="$(cd "$(dirname "$0")" && pwd)"
list() { grep -vE '^[[:space:]]*(#|$)' "$here/$1" | tr '\n' ' ' | sed 's/ *$//'; }
build_tags=fts5
case "$tag" in
  *-forDropbox*) build_tags=fts5,dropboxers ;;
  *-beta*) build_tags=fts5,beta ;;
  *-dev*) build_tags=fts5,dev ;;
esac
prerelease=false
case "$tag" in
  *-dev*|*-beta*|*-rc*|*-forDropbox*) prerelease=true ;;
esac
commands=$(list commands.txt); gui=$(list gui_commands.txt)
[ -n "$commands" ] && [ -n "$gui" ] || { echo "resolve_release.sh: an empty program list" >&2; exit 1; }
echo "version=${tag#v}"
echo "build_tags=$build_tags"
echo "prerelease=$prerelease"
echo "commands=$commands"
echo "gui_commands=$gui"
