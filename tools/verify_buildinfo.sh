#!/usr/bin/env bash
# tools/verify_buildinfo.sh: how tools/verify-release.sh reads a shipped program's build info (sourced, never run).
# Since #6052 every release is built with -trimpath, and Go then leaves the -ldflags line (and so "config.Version=") out of the build info: only the
# programs that link internal/config still carry the stamp (strings finds it), the others (coral-board, coral-agent, the hooks) show no version at all.
# What still proves a program is this release's: the build info's vcs.revision equals the commit the release tag points at, with vcs.modified=false.
# The build info is plain text in the file, so this proves the workflow built it from the tagged commit, not that a hostile file is ours: that is the
# Sigstore bundle's and the attestation's job (verify-release.sh checks both). Every case fails closed.

# bi_parse GO_VERSION_M_TEXT -> "version|tags|goos/goarch|trimpath|vcs.revision|vcs.modified" (empty fields when the line is not there)
bi_parse() {
  local gi="$1" v t tgt trim rev mod
  v="$(grep -o 'config\.Version=[^" ]*' <<<"$gi" | head -1 | sed 's/.*=//')"
  t="$(awk '$1=="build" && $2 ~ /^-tags=/ {sub("-tags=","",$2); print $2}' <<<"$gi" | head -1)"
  tgt="$(awk '$1=="build" && $2 ~ /^GOOS=/ {sub("GOOS=","",$2); os=$2} $1=="build" && $2 ~ /^GOARCH=/ {sub("GOARCH=","",$2); ar=$2} END {print os"/"ar}' <<<"$gi")"
  trim="$(awk '$1=="build" && $2 ~ /^-trimpath=/ {sub("-trimpath=","",$2); print $2}' <<<"$gi" | head -1)"
  rev="$(awk '$1=="build" && $2 ~ /^vcs\.revision=/ {sub("vcs.revision=","",$2); print $2}' <<<"$gi" | head -1)"
  mod="$(awk '$1=="build" && $2 ~ /^vcs\.modified=/ {sub("vcs.modified=","",$2); print $2}' <<<"$gi" | head -1)"
  echo "$v|$t|$tgt|$trim|$rev|$mod"
}

# bi_verdict WANT_VERSION TAG_COMMIT VERSION TRIMPATH REVISION MODIFIED GOOS/GOARCH -> "ok|how" or "fail|why", exit 0 / 1.
# TAG_COMMIT is the 40-hex commit the release tag is fetched to point at, or "-" when there is no tag to compare with (--local): then a revision is not
# compared, and a program with no version stamp fails.
bi_verdict() {
  local want="$1" tagc="$2" v="$3" trim="$4" rev="$5" mod="$6" tgt="${7:-}"
  if [ -n "$rev" ] && [ "$tagc" != "-" ]; then
    if [[ ! "$rev" =~ ^[0-9a-f]{40}$ ]]; then echo "fail|vcs.revision '$rev' is not a full 40-hex commit"; return 1; fi
    if [ "$rev" != "$tagc" ]; then echo "fail|vcs.revision ${rev:0:9} is not the tag's commit ${tagc:0:9}"; return 1; fi
  fi
  if [ -n "$v" ]; then
    if [ "$v" = "$want" ]; then echo "ok|version $v stamped"; return 0; fi
    echo "fail|version '$v'"; return 1
  fi
  # no config.Version: only a -trimpath build from the tagged commit, unmodified, passes
  if [ "$trim" != true ]; then echo "fail|version 'none stamped' and the build is not -trimpath=true"; return 1; fi
  if [ "$tagc" = "-" ]; then echo "fail|version 'none stamped' and there is no tag commit to compare vcs.revision with"; return 1; fi
  if [ -z "$rev" ]; then echo "fail|version 'none stamped' and no vcs.revision in the build info"; return 1; fi
  # a Windows program is built after tools/gen_windows_resources.sh rewrites the tracked rsrc_windows_amd64.syso files with the release version (by design:
  # "release builds regenerate the files before compiling and do not commit them"), so its tree is modified; the revision above still has to be the tag's
  if [ "$mod" != false ] && ! { [ "$mod" = true ] && [ "${tgt%%/*}" = windows ]; }; then echo "fail|vcs.modified is '${mod:-absent}', not false"; return 1; fi
  echo "ok|-trimpath build of the tagged commit ${rev:0:9}"; return 0
}
