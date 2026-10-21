#!/usr/bin/env bash
# tools/devops/approve-release.sh against a stand-in gh (release channels, #5559): no network, no GitHub. The stand-in answers
# `gh api <path>`, `gh release view` and `gh release edit` from fixture files and records every edit.
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1 # nothing here runs Coral, but the telemetry census wants it on every script that names the repository
here="$(cd "$(dirname "$0")" && pwd)"; SCRIPT="$here/../../tools/devops/approve-release.sh"
T="$(mktemp -d /tmp/apr.XXXX)"; [ -d "$T" ] || exit 1
trap 'rm -r "$T"' EXIT
mkdir -p "$T/bin" "$T/fix"
cat > "$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
# stand-in gh: fixtures in $FIX
case "$1" in
  api)
    path="$2"; shift 2; jqf=""
    while [ $# -gt 0 ]; do case "$1" in --jq) jqf="$2"; shift 2 ;; *) shift ;; esac; done
    f="$FIX/$(echo "$path" | sed 's#[/?=]#_#g').json"
    [ -f "$f" ] || { echo "stub: no fixture for $path" >&2; exit 1; }
    if [ -n "$jqf" ]; then jq -r "$jqf" "$f"; else cat "$f"; fi ;;
  release)
    sub="$2"; tag="$3"; shift 3
    case "$sub" in
      view) jq -r .body "$FIX/body_$tag.json" ;;
      edit) args="$*"; nf=""; while [ $# -gt 0 ]; do [ "$1" = --notes-file ] && nf="$2"; shift; done
            echo "edit $tag $args" >> "$FIX/edits.log"; [ -n "$nf" ] && cp "$nf" "$FIX/notes_$tag.txt"; true ;;
    esac ;;
esac
EOF
chmod +x "$T/bin/gh"
export PATH="$T/bin:$PATH" FIX="$T/fix" GITHUB_REPOSITORY=subgentic/coral GITHUB_ACTOR=tester
fail=0
ok() { echo "PASS $1"; }
bad() { echo "FAIL $1" >&2; fail=1; }
# expect_fail NAME CMD...: the command must fail and its message must contain the text in $WANT
expect_fail() { local name="$1"; shift; local out; if out="$("$@" 2>&1)"; then bad "$name (it succeeded)"; elif grep -qF -- "$WANT" <<<"$out"; then ok "$name"; else bad "$name (message was: $out)"; fi; }

# fixtures: v1.4.150 is the published pre-release being approved; v1.4.149 is the current latest
rel() { printf '{"draft":%s,"prerelease":%s,"immutable":%s,"body":%s}\n' "$2" "$3" "$4" "$(jq -Rn --arg b "$5" '$b')" > "$FIX/repos_subgentic_coral_releases_tags_$1.json"; }
tagsha() { printf '{"object":{"sha":"%s"}}\n' "$2" > "$FIX/repos_subgentic_coral_git_ref_tags_$1.json"; }
compare() { printf '{"status":"%s"}\n' "$2" > "$FIX/repos_subgentic_coral_compare_main...$1.json"; }
latest() { printf '{"tag_name":"%s"}\n' "$1" > "$FIX/repos_subgentic_coral_releases_latest.json"; }
good() { rel v1.4.150 false true true "notes"; tagsha v1.4.150 aaa111; compare aaa111 behind; latest v1.4.149; }

good
out="$(bash "$SCRIPT" check v1.4.150 2>&1)"; rc=$?
[ $rc = 0 ] && ok "a good pre-release passes check ($out)" || bad "a good pre-release passes check: $out"

for t in v1.4 1.4.150 v1.4.150-rc1 'v1.4.150;id' 'v1.4.150 x' '' 'v1.4.150-forDropbox'; do
  WANT="not a plain vX.Y.Z" expect_fail "tag '$t' is refused before anything is read" bash "$SCRIPT" check "$t"
done
good; rel v1.4.150 true true true x;      WANT="still a draft" expect_fail "a draft is refused" bash "$SCRIPT" check v1.4.150
good; rel v1.4.150 false false true x;    WANT="not a pre-release" expect_fail "an approved release is refused" bash "$SCRIPT" check v1.4.150
good; rel v1.4.150 false true false x;    WANT="not an immutable" expect_fail "a mutable release is refused" bash "$SCRIPT" check v1.4.150
good; rel v1.4.150 false true true "$(printf 'notes\n\nRevoked: 2026-10-21T00:00:00Z by someone')"
WANT="was revoked" expect_fail "a revoked tag is refused" bash "$SCRIPT" check v1.4.150
good; compare aaa111 ahead;               WANT="not on main" expect_fail "a tag commit ahead of main is refused" bash "$SCRIPT" check v1.4.150
good; compare aaa111 diverged;            WANT="not on main" expect_fail "a diverged tag commit is refused" bash "$SCRIPT" check v1.4.150
good; compare aaa111 identical; bash "$SCRIPT" check v1.4.150 >/dev/null 2>&1 && ok "a tag at main's tip is allowed" || bad "a tag at main's tip is allowed"
good; latest v1.4.150;                    WANT="not newer than the current latest" expect_fail "the same version is refused" bash "$SCRIPT" check v1.4.150
good; latest v1.4.151;                    WANT="not newer than the current latest" expect_fail "an older version is refused (never backwards)" bash "$SCRIPT" check v1.4.150
good; latest v1.4.9; bash "$SCRIPT" check v1.4.150 >/dev/null 2>&1 && ok "versions compare as numbers (1.4.150 > 1.4.9)" || bad "versions compare as numbers"
good; latest v1.4.149-rc1;                WANT="not a plain tag" expect_fail "a suffix latest is refused" bash "$SCRIPT" check v1.4.150
good; rm "$FIX/repos_subgentic_coral_releases_tags_v1.4.150.json"; WANT="no release for" expect_fail "a missing release is refused" bash "$SCRIPT" check v1.4.150

# flip: prerelease off, latest on, one display-only line appended to the body
good; : > "$FIX/edits.log"; printf '{"body":"the notes"}\n' > "$FIX/body_v1.4.150.json"
bash "$SCRIPT" flip v1.4.150 >/dev/null 2>&1; rc=$?
[ $rc = 0 ] && grep -q 'edit v1.4.150 --repo subgentic/coral --prerelease=false --latest' "$FIX/edits.log" && ok "flip clears the pre-release flag and marks latest" || bad "flip edits: $(cat "$FIX/edits.log")"
grep -qx 'Approved: [0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}T[0-9:]*Z' "$FIX/notes_v1.4.150.txt" && [ "$(grep -v '^$' "$FIX/notes_v1.4.150.txt" | tail -1 | cut -c1-9)" = 'Approved:' ] && grep -q 'the notes' "$FIX/notes_v1.4.150.txt" && ok "flip appends the Approved line (UTC, nothing after the time, the LAST line) and keeps the notes" || bad "flip body: $(cat "$FIX/notes_v1.4.150.txt" 2>/dev/null)"

# revoke: only an approved release; the highest EARLIER approved release is marked latest again
printf '[{"tag_name":"v1.4.151","draft":false,"prerelease":false},{"tag_name":"v1.4.150","draft":false,"prerelease":false},{"tag_name":"v1.4.149","draft":false,"prerelease":false},{"tag_name":"v1.4.9","draft":false,"prerelease":false},{"tag_name":"v1.4.152","draft":false,"prerelease":true},{"tag_name":"v1.4.150-rc1","draft":false,"prerelease":false}]\n' > "$FIX/repos_subgentic_coral_releases_per_page_30.json"
# a release that is not the latest cannot be revoked: that would move latest backwards past a newer approved release
rel v1.4.150 false false true "notes"; latest v1.4.151; : > "$FIX/edits.log"
WANT="only the latest release can be revoked" expect_fail "revoke of an approved release that is not latest is refused" bash "$SCRIPT" revoke v1.4.150
[ ! -s "$FIX/edits.log" ] && ok "a refused revoke edits nothing" || bad "a refused revoke edited: $(cat "$FIX/edits.log")"
rel v1.4.150 false false true "notes"; latest v1.4.150; : > "$FIX/edits.log"
bash "$SCRIPT" revoke v1.4.150 >"$T/rv.out" 2>&1; rc=$?; [ $rc = 0 ] || cat "$T/rv.out" >&2
[ $rc = 0 ] && grep -q 'edit v1.4.150 --repo subgentic/coral --prerelease --latest=false' "$FIX/edits.log" && grep -q 'edit v1.4.149 --repo subgentic/coral --latest' "$FIX/edits.log" && ok "revoke flips back and re-marks v1.4.149 (not v1.4.9, not the rc, not a newer one)" || bad "revoke edits: $(cat "$FIX/edits.log")"
grep -qx 'Revoked: [0-9TZ:-]* by tester' "$FIX/notes_v1.4.150.txt" && ok "revoke records the Revoked line" || bad "revoke body"
rel v1.4.150 false true true "notes"; WANT="is not approved" expect_fail "revoke of a pre-release is refused" bash "$SCRIPT" revoke v1.4.150

# flip re-runs the checks itself: a release that stopped being approvable between the verify job and the flip job is not flipped
good; latest v1.4.151; : > "$FIX/edits.log"; printf '{"body":"x"}\n' > "$FIX/body_v1.4.150.json"
WANT="not newer than the current latest" expect_fail "flip refuses a tag that is no longer newer than latest" bash "$SCRIPT" flip v1.4.150
[ ! -s "$FIX/edits.log" ] && ok "a refused flip edits nothing" || bad "a refused flip edited: $(cat "$FIX/edits.log")"
good; rel v1.4.150 false false true "notes"; : > "$FIX/edits.log"
WANT="not a pre-release" expect_fail "flip refuses an already approved release" bash "$SCRIPT" flip v1.4.150

# the script never builds a command from the tag text: a hostile tag never reaches gh
good; : > "$FIX/edits.log"; bash "$SCRIPT" flip '$(touch /tmp/apr-pwned)' >/dev/null 2>&1; [ ! -e /tmp/apr-pwned ] && [ ! -s "$FIX/edits.log" ] && ok "a hostile tag runs nothing and edits nothing" || bad "a hostile tag was used"
exit "$fail"
