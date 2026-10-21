#!/usr/bin/env bash
# tools/devops/update-tap.sh against a local bare repository standing in for the Homebrew tap (release channels, #5559): no network.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1 # nothing here runs Coral, but the telemetry census wants it on every script that names the repository
here="$(cd "$(dirname "$0")" && pwd)"; SCRIPT="$here/../../tools/devops/update-tap.sh"; CASK="$here/../../Casks/coral.rb"
T="$(mktemp -d /tmp/utp.XXXX)"; [ -d "$T" ] || exit 1
trap 'rm -rf "$T"' EXIT
fail=0
ok() { echo "PASS $1"; }
bad() { echo "FAIL $1" >&2; fail=1; }
ID=(-c user.name=t -c user.email=t@example.invalid)
S1=$(printf '1%.0s' $(seq 64)); S2=$(printf '2%.0s' $(seq 64)); S3=$(printf '3%.0s' $(seq 64))
export GITHUB_REPOSITORY=subgentic/coral

mktap() {  # mktap VERSION: a fresh bare tap whose cask is at VERSION and still names the OLD owner
  rm -rf "$T/bare.git" "$T/seed"; git init -q --bare -b main "$T/bare.git"; git init -q -b main "$T/seed"
  mkdir -p "$T/seed/Casks"
  sed -E "s/^  version \"[^\"]*\"$/  version \"$1\"/; s#github.com/subgentic/coral#github.com/cdknorow/coral#g" "$CASK" > "$T/seed/Casks/coral.rb"
  (cd "$T/seed" && git add -A && git "${ID[@]}" commit -qm "coral $1" && git push -q "$T/bare.git" HEAD:main)
}
cask() { git --git-dir="$T/bare.git" show main:Casks/coral.rb; }
commits() { git --git-dir="$T/bare.git" rev-list --count main; }
run() { TAP_URL="file://$T/bare.git" bash "$SCRIPT" "$@" 2>&1; }

mktap 1.4.149
out="$(run 1.4.150 "$S1")"; rc=$?
[ $rc = 0 ] && cask | grep -qx '  version "1.4.150"' && cask | grep -qx "  sha256 \"$S1\"" && ok "approve: version and sha256 are set" || bad "approve: $out"
cask | grep -qx '  url "https://github.com/subgentic/coral/releases/download/v#{version}/Coral.v#{version}.dmg"' && cask | grep -qx '  homepage "https://github.com/subgentic/coral"' && ok "approve: url and homepage follow the repository that built the release" || bad "approve: url/homepage"
[ "$(commits)" = 2 ] && [ "$(git --git-dir="$T/bare.git" log -1 --format=%an main)" = coral-release ] && ok "approve: exactly one new commit by the release identity" || bad "approve: commits=$(commits)"
before="$(commits)"; out="$(run 1.4.150 "$S1")"; rc=$?
[ $rc = 0 ] && [ "$(commits)" = "$before" ] && ok "approve again: nothing to do, no commit" || bad "approve again: $out"
out="$(run 1.4.149 "$S2")"; rc=$?
[ $rc = 0 ] && [ "$(commits)" = "$before" ] && cask | grep -qx '  version "1.4.150"' && ok "approve of an older version never downgrades the cask" || bad "downgrade guard: $out"
out="$(run 1.4.9 "$S2")"; [ "$(commits)" = "$before" ] && ok "1.4.9 is older than 1.4.150 (numeric compare)" || bad "numeric compare: $out"

# revoke: the cask goes back by a NEW commit, never by a force-push
out="$(run 1.4.149 "$S3" --revoke)"; rc=$?
[ $rc = 0 ] && cask | grep -qx '  version "1.4.149"' && cask | grep -qx "  sha256 \"$S3\"" && [ "$(commits)" = $((before+1)) ] && ok "revoke: a new commit puts the previous version back" || bad "revoke: $out"
git --git-dir="$T/bare.git" log -1 --format=%s main | grep -q 'revoke' && ok "revoke: the commit message says so" || bad "revoke message"
out="$(run 1.4.150 "$S1" --revoke)"; rc=$?
[ $rc != 0 ] && grep -q 'not older' <<<"$out" && ok "revoke towards a NEWER version is refused" || bad "revoke newer: rc=$rc $out"

# bad input never reaches git
UP=$(printf 'A%.0s' $(seq 64))
for args in "v1.4.150 $S1" "1.4 $S1" "1.4.150-rc1 $S1" "1.4.150 abc" "1.4.150 $UP"; do
  # shellcheck disable=SC2086
  out="$(run $args)"; rc=$?; [ $rc != 0 ] && ok "refused: $args" || bad "accepted: $args"
done
out="$(run 1.4.150 "$S1" --force)"; [ $? != 0 ] && ok "an unknown flag is refused" || bad "unknown flag accepted"
exit "$fail"
