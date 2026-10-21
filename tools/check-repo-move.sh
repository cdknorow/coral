#!/usr/bin/env bash
# check-repo-move.sh: read-only check of the Coral repository move (#5299, #5525). Run it before the move to see the baseline, and after it to see
# what the move did to everything outside the repository that points at it: installed Corals' update notice, release downloads, git remotes,
# the Homebrew tap, the docs site and the release verification. It prints one table (PASS, FAIL, SKIP or INFO per row) and exits 1 only when a row FAILS.
#
#   tools/check-repo-move.sh                              # old = cdknorow/coral, new = subgentic/coral, the latest release
#   tools/check-repo-move.sh --tag v1.4.141 --no-verify
#   tools/check-repo-move.sh --expect after               # after the transfer: the old name must redirect to the new home
#   tools/check-repo-move.sh --self-test                  # the parsing and redirect rules, on fixtures, no network
#
# Read-only, by construction: GET and HEAD requests and git ls-remote / clone of public repositories only; no gh call that changes anything; nothing is
# pushed, tagged, dispatched or installed. It never touches ~/.coral or the installed Homebrew (a tap is cloned into a temp folder and its cask file read).
# Anonymous by default, as an installed Coral's own update check is; GitHub's anonymous limit is shared per address, so with --use-gh-token a request the
# limit refused is repeated once with the token `gh auth token` prints (GET only, host api.github.com only, passed in a 0600 file, never on a command line).
# One small download is made: SHA256SUMS.txt of the release (a few KB), through the old release URL, to prove the redirect and the digests.
#
# Options: --old OWNER/REPO  --new OWNER/REPO  --tag vX.Y.Z (default: the latest release of the old home)  --expect before|after|auto (default auto: after when the
# new repository answers)  --no-verify (skip tools/verify-release.sh, which downloads the release run's artifacts)  --use-gh-token  --keep  --self-test
set -u
set -o pipefail
export CORAL_TELEMETRY_DISABLED=1

OLD="cdknorow/coral"; NEW="subgentic/coral"; TAG=""; EXPECT="auto"; VERIFY=1; USE_TOKEN=0; KEEP=0; SELFTEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    --old) OLD="${2:?}"; shift 2 ;;
    --new) NEW="${2:?}"; shift 2 ;;
    --tag) TAG="${2:?}"; shift 2 ;;
    --expect) EXPECT="${2:?}"; shift 2 ;;
    --no-verify) VERIFY=0; shift ;;
    --use-gh-token) USE_TOKEN=1; shift ;;
    --keep) KEEP=1; shift ;;
    --self-test) SELFTEST=1; shift ;;
    -h|--help) sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "check-repo-move: unknown option $1" >&2; exit 2 ;;
  esac
done

# ---- rules shared with the product, so the check says what an installed Coral would do ----

# tagShape: internal/updatecheck's release tag shape.
tag_shape() { printf '%s' "$1" | grep -Eq '^v?[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}(-[0-9A-Za-z.]{1,40})?$'; }

# hop_ok FROM_URL TO_URL: internal/updatecheck's redirect rule: only on the same host, never to another scheme.
hop_ok() {
  local f t fh th fs ts
  f="$1"; t="$2"
  fs="${f%%://*}"; ts="${t%%://*}"
  fh="${f#*://}"; fh="${fh%%/*}"; th="${t#*://}"; th="${th%%/*}"
  [ "$fs" = "$ts" ] && [ "$fh" = "$th" ]
}

# cask_field FILE KEY: the quoted value of a cask's  key "value"  line.
cask_field() { grep -E "^[[:space:]]*$2[[:space:]]+\"" "$1" | head -1 | sed -E 's/^[^"]*"([^"]*)".*$/\1/'; }

if [ "$SELFTEST" = 1 ]; then
  fails=0
  t() { if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; fails=$((fails+1)); fi; }
  t "tag shape: v1.4.141"            'tag_shape v1.4.141'
  t "tag shape: 1.4.141-rc1"         'tag_shape 1.4.141-rc1'
  t "tag shape refuses a sentence"   '! tag_shape "v1.4.141 and more"'
  t "tag shape refuses a path"       '! tag_shape "../v1.4.141"'
  t "same host https hop is ok"      'hop_ok https://api.github.com/repos/a/b/releases/latest https://api.github.com/repositories/123/releases/latest'
  t "another host is refused"        '! hop_ok https://api.github.com/repos/a/b https://evil.example/repos/a/b'
  t "https to http is refused"       '! hop_ok https://api.github.com/x http://api.github.com/x'
  t "a lookalike host is refused"    '! hop_ok https://api.github.com/x https://api.github.com.evil.example/x'
  tmp="$(mktemp -d /tmp/crm-self.XXXX)"
  printf 'cask "coral" do\n  version "1.4.141"\n  sha256 "%064d"\n  url "https://github.com/cdknorow/coral/releases/download/v#{version}/Coral.v#{version}.dmg"\nend\n' 7 > "$tmp/c.rb"
  t "cask version"                   '[ "$(cask_field "$tmp/c.rb" version)" = 1.4.141 ]'
  t "cask url"                       'cask_field "$tmp/c.rb" url | grep -q "^https://github.com/cdknorow/coral/releases/download/"'
  sha="$(cask_field "$tmp/c.rb" sha256)"
  t "cask sha256 is 64 digits"       '[ "${#sha}" = 64 ]'
  case "$tmp" in /tmp/crm-self.*) rm -rf "$tmp" ;; esac
  [ "$fails" = 0 ] && echo "self-test: all rules hold" || echo "self-test: $fails failed"
  exit $((fails > 0))
fi

for need in curl jq git shasum; do command -v "$need" >/dev/null 2>&1 || { echo "check-repo-move: $need is required" >&2; exit 2; }; done
here="$(cd "$(dirname "$0")" && pwd)"
[ -f "$here/release-homes.sh" ] && . "$here/release-homes.sh"
TMP="$(mktemp -d /tmp/check-repo-move.XXXX)" || exit 2
cleanup() { [ "$KEEP" = 1 ] && { echo "kept: $TMP"; return; }; case "$TMP" in /tmp/check-repo-move.*) rm -rf "$TMP" ;; esac; }
trap cleanup EXIT
UA="coral-repo-move-check"
OLD_OWNER="${OLD%%/*}"; OLD_NAME="${OLD##*/}"; NEW_OWNER="${NEW%%/*}"; NEW_NAME="${NEW##*/}"
export GIT_TERMINAL_PROMPT=0

ROWS=(); FAILS=0
row() { ROWS+=("$1|$2|$3"); [ "$1" = FAIL ] && FAILS=$((FAILS+1)); return 0; }

# ---- requests: anonymous curl; one retry with the gh token when the shared anonymous limit refused (opt-in) ----
TOKEN_FILE=""
token_cfg() {
  [ "$USE_TOKEN" = 1 ] || return 1
  if [ -z "$TOKEN_FILE" ]; then
    command -v gh >/dev/null 2>&1 || return 1
    local t; t="$(gh auth token 2>/dev/null)" || return 1
    [ -n "$t" ] || return 1
    TOKEN_FILE="$TMP/.tok"; (umask 077; printf 'header = "Authorization: Bearer %s"\n' "$t" > "$TOKEN_FILE")
  fi
  return 0
}
# api_head URL: the status line, Location, rate-limit remaining of one GET, redirects NOT followed. Sets A_CODE, A_LOC, A_BODY (file).
api_get() {
  local url="$1" hdr="$TMP/h.$$" body="$TMP/b.$$" i
  A_BODY="$body"; A_LOC=""; A_CODE=""
  for i in 1 2; do
    if [ "$i" = 2 ]; then token_cfg || break; A_TOKEN=1; fi
    if [ "$i" = 1 ]; then
      curl -q -sS --max-time 25 -A "$UA" -H 'Accept: application/vnd.github+json' -D "$hdr" -o "$body" "$url" 2>/dev/null
    else
      case "$url" in https://api.github.com/*) ;; *) break ;; esac
      curl -q -sS --max-time 25 -A "$UA" -H 'Accept: application/vnd.github+json' -K "$TOKEN_FILE" -D "$hdr" -o "$body" "$url" 2>/dev/null
    fi
    A_CODE="$(awk 'NR==1{print $2}' "$hdr" | tr -d '\r')"
    A_LOC="$(grep -i '^location:' "$hdr" | head -1 | sed 's/^[^:]*: *//' | tr -d '\r')"
    local rem; rem="$(grep -i '^x-ratelimit-remaining:' "$hdr" | head -1 | sed 's/^[^:]*: *//' | tr -d '\r')"
    if [ "$A_CODE" = 403 ] && [ "$rem" = 0 ]; then A_LIMITED=1; continue; fi
    A_LIMITED=0; return 0
  done
  [ "${A_LIMITED:-0}" = 1 ] && A_CODE="limited"
  return 0
}
A_LIMITED=0; A_TOKEN=0

latest_json="$TMP/latest.json"

# git, anonymous: an empty environment (no credential helper, no ssh agent, no user config), https only
git_ls() { env -i PATH="$PATH" HOME="$TMP" GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git ls-remote "https://github.com/$1.git" "${@:2}" 2>&1; }
git_tags() { env -i PATH="$PATH" HOME="$TMP" GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git ls-remote --tags --refs "https://github.com/$1.git" 2>&1; }

# ---- the rows ----

# 1. the old name and the new repository
api_get "https://api.github.com/repos/$OLD"; old_code="$A_CODE"; old_loc="$A_LOC"; old_full=""
[ "$old_code" = 200 ] && old_full="$(jq -r '.full_name // empty' "$A_BODY")"
api_get "https://api.github.com/repos/$NEW"; new_code="$A_CODE"; new_full=""
[ "$new_code" = 200 ] && new_full="$(jq -r '.full_name // empty' "$A_BODY")"
if [ "$EXPECT" = auto ]; then [ "$new_code" = 200 ] && EXPECT=after || EXPECT=before; fi
HOME_REPO="$OLD"; [ "$EXPECT" = after ] && HOME_REPO="$NEW"
HOME_OWNER="${HOME_REPO%%/*}"
if [ "$old_code" = limited ] || [ "$new_code" = limited ]; then
  row SKIP "api: the repository names" "GitHub's anonymous limit is used up for this address (rerun with --use-gh-token, or after the reset)"
elif [ "$EXPECT" = before ]; then
  [ "$old_code" = 200 ] && [ "$old_full" = "$OLD" ] && row PASS "api: the old repository answers" "$OLD is $old_full (200)" || row FAIL "api: the old repository answers" "GET /repos/$OLD gave $old_code $old_full"
  [ "$new_code" = 404 ] && row PASS "api: the new repository does not exist yet" "$NEW answers 404, as expected before the move" || row INFO "api: the new repository" "$NEW answers $new_code ($new_full): it exists already; use --expect after"
else
  if [ "$old_code" = 301 ] || [ "$old_code" = 308 ]; then row PASS "api: the old name redirects" "GET /repos/$OLD gives $old_code to ${A_LOC:-$old_loc}"
  elif [ "$old_code" = 200 ] && [ "$old_full" = "$NEW" ]; then row PASS "api: the old name answers as the new repository" "full_name is $old_full"
  else row FAIL "api: the old name redirects to the new home" "GET /repos/$OLD gave $old_code ($old_full), expected a redirect to $NEW"; fi
  [ "$new_code" = 200 ] && [ "$new_full" = "$NEW" ] && row PASS "api: the new repository" "$NEW is public and answers 200" || row FAIL "api: the new repository" "GET /repos/$NEW gave $new_code $new_full"
fi

# 2. the update notice of an INSTALLED Coral: internal/updatecheck asks this one URL, follows redirects only on the same host, at most 5
FEED="https://api.github.com/repos/cdknorow/coral/releases/latest"
hop="$FEED"; hops=0; feed_status="ok"; feed_why=""
while :; do
  api_get "$hop"
  if [ "$A_CODE" = limited ]; then feed_status="limited"; break; fi
  case "$A_CODE" in
    301|302|307|308)
      hops=$((hops+1))
      if [ "$hops" -ge 5 ]; then feed_status="bad"; feed_why="more than 5 redirects"; break; fi
      next="$A_LOC"; case "$next" in /*) next="https://api.github.com$next" ;; esac
      if ! hop_ok "$FEED" "$next"; then feed_status="bad"; feed_why="redirects to another host or scheme: $next (an installed Coral refuses it)"; break; fi
      hop="$next" ;;
    200) break ;;
    *) feed_status="bad"; feed_why="answered $A_CODE"; break ;;
  esac
done
if [ "$feed_status" = limited ]; then row SKIP "update notice: the release feed" "GitHub's anonymous limit is used up for this address (rerun with --use-gh-token)"
elif [ "$feed_status" = bad ]; then row FAIL "update notice: the release feed an installed Coral reads" "$FEED: $feed_why"
else
  cp "$A_BODY" "$latest_json"
  ftag="$(jq -r '.tag_name // empty' "$latest_json")"
  via=""; [ "$A_TOKEN" = 1 ] && via=" (asked with the gh token: the anonymous limit is used up; an installed Coral asks anonymously)"
  if tag_shape "$ftag"; then row PASS "update notice: the release feed an installed Coral reads" "$hops redirect(s), all on api.github.com, then 200 with tag $ftag$via"
  else row FAIL "update notice: the release feed an installed Coral reads" "200 but tag_name '$ftag' is not a release tag"; fi
  [ -z "$TAG" ] && TAG="$ftag"
fi
[ -z "$TAG" ] && TAG="$(git_tags "$HOME_REPO" | sed 's|.*refs/tags/||' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1)"
if [ -z "$TAG" ]; then row FAIL "release: which tag" "no tag given and none found"; TAG="v0.0.0"; fi

# 3. release downloads through the OLD URL: the redirect chain, then the digests
SUMS_URL="https://github.com/$OLD/releases/download/$TAG/SHA256SUMS.txt"
chain=""; url="$SUMS_URL"; code=""; i=0
while [ "$i" -lt 6 ]; do
  out="$(curl -q -sS -I --max-time 25 -A "$UA" -o /dev/null -w '%{http_code} %{redirect_url}' "$url" 2>/dev/null)"
  code="${out%% *}"; next="${out#* }"
  chain="$chain $code"
  case "$code" in 301|302|303|307|308) url="$next"; i=$((i+1)) ;; *) break ;; esac
done
if [ "$code" = 200 ]; then
  if [ "$EXPECT" = after ] && ! printf '%s' "$chain" | grep -Eq '30[1278]'; then row FAIL "release download: the old URL redirects" "$SUMS_URL answered 200 with no redirect, but after the move the old name must redirect"
  else row PASS "release download: the old URL reaches the file" "$SUMS_URL ->$chain (final host $(printf '%s' "$url" | sed -E 's|^[a-z]+://([^/]*).*|\1|'))"; fi
else
  row FAIL "release download: the old URL reaches the file" "$SUMS_URL ->$chain, stopped at $url"
fi
sums="$TMP/SHA256SUMS.txt"
if curl -q -sS -L --max-redirs 5 --max-time 40 -A "$UA" -o "$sums" "$SUMS_URL" 2>/dev/null && [ -s "$sums" ]; then
  api_get "https://api.github.com/repos/$HOME_REPO/releases/tags/$TAG"
  if [ "$A_CODE" = limited ]; then row SKIP "release download: the digests" "GitHub's anonymous limit is used up (rerun with --use-gh-token)"
  elif [ "$A_CODE" != 200 ]; then row FAIL "release download: the digests" "GET /repos/$HOME_REPO/releases/tags/$TAG gave $A_CODE"
  else
    cp "$A_BODY" "$TMP/rel.json"
    n=0; bad=0; missing=0; detail=""
    while read -r sum name; do
      [ -n "$name" ] || continue; name="${name#\*}"; n=$((n+1))
      want="$(jq -r --arg n "$name" '.assets[] | select(.name==$n) | .digest // empty' "$TMP/rel.json" | head -1)"
      if [ -z "$want" ]; then missing=$((missing+1)); detail="$detail $name(not an asset)"; continue; fi
      [ "${want#sha256:}" = "$sum" ] || { bad=$((bad+1)); detail="$detail $name(differs)"; }
    done < "$sums"
    if [ "$n" = 0 ]; then row FAIL "release download: SHA256SUMS.txt" "the file lists nothing"
    elif [ "$bad" = 0 ] && [ "$missing" = 0 ]; then row PASS "release download: SHA256SUMS.txt equals the published digests" "$n files listed, each equal to the release's own sha256 for that asset"
    else row FAIL "release download: SHA256SUMS.txt equals the published digests" "$n listed, $bad differ, $missing not assets:$detail"; fi
    row INFO "release download: the release" "$TAG at $HOME_REPO has $(jq '.assets | length' "$TMP/rel.json") assets, prerelease=$(jq -r '.prerelease' "$TMP/rel.json"), draft=$(jq -r '.draft' "$TMP/rel.json")"
  fi
else
  row FAIL "release download: SHA256SUMS.txt" "could not download $SUMS_URL"
fi

# 4. git: the same history under both names
old_main="$(git_ls "$OLD" refs/heads/main | awk 'NR==1{print $1}')"
old_tag="$(git_ls "$OLD" "refs/tags/$TAG" "refs/tags/$TAG^{}" | awk 'END{print $1}')"
if [ -n "$old_main" ] && [ "${#old_main}" = 40 ]; then row PASS "git: ls-remote of the old name" "$OLD main is ${old_main:0:12}, $TAG is ${old_tag:0:12}"; else row FAIL "git: ls-remote of the old name" "no main at $OLD: $old_main"; fi
if [ "$EXPECT" = after ]; then
  new_main="$(git_ls "$NEW" refs/heads/main | awk 'NR==1{print $1}')"
  new_tag="$(git_ls "$NEW" "refs/tags/$TAG" "refs/tags/$TAG^{}" | awk 'END{print $1}')"
  [ "$old_main" = "$new_main" ] && [ "$old_tag" = "$new_tag" ] && row PASS "git: the old and the new name show the same history" "main ${new_main:0:12}, $TAG ${new_tag:0:12}" || row FAIL "git: the old and the new name show the same history" "old main ${old_main:0:12} vs new ${new_main:0:12}; $TAG ${old_tag:0:12} vs ${new_tag:0:12}"
  on="$(git_tags "$OLD" | grep -c 'refs/tags/')"; nn="$(git_tags "$NEW" | grep -c 'refs/tags/')"
  [ "$on" = "$nn" ] && [ "$nn" -gt 0 ] && row PASS "git: every tag moved" "$nn tags under both names" || row FAIL "git: every tag moved" "$on tags at the old name, $nn at the new"
else
  nout="$(git_ls "$NEW" HEAD | head -1)"
  case "$nout" in *"not found"*|*"Repository not found"*|*"fatal"*|*"could not read"*|"") row PASS "git: the new name does not exist yet" "git ls-remote $NEW: ${nout:-no answer} (git cannot tell a missing repository from a private one)" ;; *) row INFO "git: the new name answers already" "$nout" ;; esac
fi

# 5. the Homebrew tap: the cask file in the tap's repository, cloned into a temp folder (Homebrew itself is not touched: a tap lives inside its prefix)
# The tap does NOT move with the repository (decided, #5299): it stays at the old owner and its cask points at the release home. A tap at the new owner is reported, never required.
ntap="$NEW_OWNER/homebrew-coral"
nout2="$(git_ls "$ntap" HEAD | head -1)"
if printf '%s' "$nout2" | grep -Eq '^[0-9a-f]{40}'; then row INFO "tap: $ntap exists" "answers (${nout2:0:12}): the tap is not moving, so nothing reads it"
else row INFO "tap: $ntap does not exist" "as decided: the tap stays at ${OLD_OWNER}/homebrew-coral"; fi
for which in old; do
  trepo="$OLD_OWNER/homebrew-coral"
  d="$TMP/tap-$which"
  if env -i PATH="$PATH" HOME="$TMP" GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git clone -q --depth 1 "https://github.com/$trepo.git" "$d" >"$TMP/clone.log" 2>&1 && [ -f "$d/Casks/coral.rb" ]; then
    cv="$(cask_field "$d/Casks/coral.rb" version)"; cs="$(cask_field "$d/Casks/coral.rb" sha256)"; cu="$(cask_field "$d/Casks/coral.rb" url)"
    curepo="$(printf '%s' "$cu" | sed -E 's|^https://github.com/([^/]+/[^/]+)/.*|\1|')"
    dmg_sum="$(grep -E ' \*?Coral\.v?[0-9][^ ]*\.dmg$| \*?Coral\.dmg$' "$sums" 2>/dev/null | head -1 | awk '{print $1}')"
    if ruby -c "$d/Casks/coral.rb" >/dev/null 2>&1; then syn=ok; else syn=bad; fi
    detail="version $cv, url repo $curepo, ruby syntax $syn"
    ok=1
    [ "$syn" = ok ] || ok=0
    [ "$cv" = "${TAG#v}" ] || { ok=0; detail="$detail; version is not $TAG"; }
    [ -n "$dmg_sum" ] && [ "$dmg_sum" != "$cs" ] && { ok=0; detail="$detail; sha256 differs from SHA256SUMS.txt's DMG"; }
    [ "$curepo" = "$HOME_REPO" ] || { ok=0; detail="$detail; the url points at $curepo, the home is $HOME_REPO"; [ "$EXPECT" = after ] && [ "$curepo" = "$OLD" ] && detail="$detail (the old home still works through GitHub's redirect, see the release download row; the cask gets the new url when the first release from the new home publishes it)"; }
    [ "$ok" = 1 ] && row PASS "tap: $trepo's cask" "$detail, sha256 equals the DMG's in SHA256SUMS.txt" || row FAIL "tap: $trepo's cask" "$detail"
  else
    if [ "$which" = old ]; then row FAIL "tap: $trepo" "could not clone it: $(head -c 160 "$TMP/clone.log")"; else row FAIL "tap: $trepo" "could not clone it after the move: $(head -c 160 "$TMP/clone.log")"; fi
  fi
done
row SKIP "tap: brew tap in an isolated Homebrew" "Homebrew cannot be relocated: a tap lives inside its prefix, so tapping would write to the installed one; the cask file, its syntax, version, sha256 and url were checked from a clone instead"

# 6. the docs site (Pages)
site_old="$(git show "HEAD:mkdocs.yml" 2>/dev/null | grep -E '^site_url:' | head -1 | awk '{print $2}')"
[ -z "$site_old" ] && site_old="$(curl -q -sS -L --max-time 25 -A "$UA" "https://raw.githubusercontent.com/$OLD/main/mkdocs.yml" 2>/dev/null | grep -E '^site_url:' | head -1 | awk '{print $2}')"
oc="$(curl -q -sS -L --max-redirs 3 -o /dev/null --max-time 25 -A "$UA" -w '%{http_code}' "https://${OLD_OWNER}.github.io/$OLD_NAME/" 2>/dev/null)"
if [ "$EXPECT" = before ]; then
  [ "$oc" = 200 ] && row PASS "pages: the docs site" "https://$OLD_OWNER.github.io/$OLD_NAME/ answers 200; mkdocs.yml site_url is $site_old" || row FAIL "pages: the docs site" "https://$OLD_OWNER.github.io/$OLD_NAME/ answers $oc"
else
  nc="$(curl -q -sS -L --max-redirs 3 -o /dev/null --max-time 25 -A "$UA" -w '%{http_code}' "https://${NEW_OWNER}.github.io/$NEW_NAME/" 2>/dev/null)"
  [ "$nc" = 200 ] && row PASS "pages: the docs site at the new home" "https://$NEW_OWNER.github.io/$NEW_NAME/ answers 200" || row FAIL "pages: the docs site at the new home" "https://$NEW_OWNER.github.io/$NEW_NAME/ answers $nc (the docs workflow has to run at the new home)"
  row INFO "pages: the old site" "https://$OLD_OWNER.github.io/$OLD_NAME/ answers $oc after the move (GitHub does not redirect Pages across owners)"
  case "$site_old" in "https://$NEW_OWNER.github.io/"*) row PASS "pages: mkdocs.yml names the new site" "$site_old" ;; *) row FAIL "pages: mkdocs.yml names the new site" "site_url is $site_old" ;; esac
fi

# 7. what in this checkout still names the old home (the module path is kept on purpose and is not counted)
root="$(cd "$here/.." && pwd)"
if git -C "$root" rev-parse --git-dir >/dev/null 2>&1; then
  names="$(git -C "$root" grep -nI "$OLD" -- . ':!coral-go/go.mod' ':!coral-go/go.sum' ':!*.go' ':!licenses' 2>/dev/null | grep -v "github.com/$OLD/[a-z]*/internal\|-X github.com/$OLD/internal" || true)"
  gos="$(git -C "$root" grep -nI "https\?://[^ \"']*$OLD" -- '*.go' 2>/dev/null | grep -v '_test.go' || true)"
  nfiles="$(printf '%s' "$names" | grep -c . | tr -d ' ')"; gfiles="$(printf '%s' "$gos" | grep -c . | tr -d ' ')"
  row INFO "tree: places that still name $OLD" "$nfiles lines outside Go sources ($(printf '%s' "$names" | cut -d: -f1 | sort -u | grep -c . | tr -d ' ') files), $gfiles URLs in Go sources: $(printf '%s\n' "$gos" | cut -d: -f1 | sort -u | tr '\n' ' ')"
fi

# 7b. attestations: a transferred repository keeps its releases' provenance with the OWNER that built them; the per-repository lookup stops finding it
if command -v gh >/dev/null 2>&1 && [ -s "$sums" ] && command -v release_home_for_tag >/dev/null 2>&1; then
  idrepo="$(release_home_for_tag "$TAG")"; [ -n "$idrepo" ] || idrepo="$OLD"
  signer="$idrepo/.github/workflows/release.yml"
  if gh attestation verify "$sums" --owner "${idrepo%%/*}" --signer-workflow "$signer" --source-ref "refs/tags/$TAG" >"$TMP/att1.log" 2>&1; then
    row PASS "attestation: SHA256SUMS.txt by owner ${idrepo%%/*}" "verified against $signer at refs/tags/$TAG"
  else row FAIL "attestation: SHA256SUMS.txt by owner ${idrepo%%/*}" "$(tail -1 "$TMP/att1.log" | cut -c1-140)"; fi
  if gh attestation verify "$sums" --repo "$idrepo" --signer-workflow "$signer" --source-ref "refs/tags/$TAG" >"$TMP/att2.log" 2>&1; then
    row INFO "attestation: the same by repository $idrepo" "found (the per-repository lookup works)"
  else row INFO "attestation: the same by repository $idrepo" "not found: $(tail -1 "$TMP/att2.log" | cut -c1-110) (tools/verify-release.sh looks up by owner for this reason)"; fi
fi

# 8. the release verification with the OLD identity: a release is checked against the home that built it (tools/release-homes.txt)
if [ "$VERIFY" = 1 ]; then
  if [ -x "$here/verify-release.sh" ]; then
    vlog="$TMP/verify.log"
    vhome="$OLD"; command -v release_home_for_tag >/dev/null 2>&1 && [ -n "$(release_home_for_tag "$TAG")" ] && vhome="$(release_home_for_tag "$TAG")" # the home that built THIS tag
    "$here/verify-release.sh" "$TAG" --repo "$vhome" > "$vlog" 2>&1; vrc=$?
    vsum="$(grep -E 'passed|FAIL' "$vlog" | tail -2 | tr '\n' ' ')"
    [ "$vrc" = 0 ] && row PASS "verify-release: $TAG with its own identity ($vhome)" "exit 0; ${vsum:-see the table}" || row FAIL "verify-release: $TAG with its own identity ($vhome)" "exit $vrc; $(grep -E '^(FAIL|\|.*FAIL)' "$vlog" | head -3 | tr '\n' ' ')"
  else
    row SKIP "verify-release" "tools/verify-release.sh not found next to this script"
  fi
else
  row SKIP "verify-release: $TAG with the old identity ($OLD)" "--no-verify"
fi

# ---- the table ----
echo
printf 'check-repo-move: old %s, new %s, expect %s, release %s\n' "$OLD" "$NEW" "$EXPECT" "$TAG"
printf '%-5s | %-62s | %s\n' STATUS CHECK DETAIL
for r in "${ROWS[@]}"; do
  s="${r%%|*}"; rest="${r#*|}"; c="${rest%%|*}"; dtl="${rest#*|}"
  printf '%-5s | %-62s | %s\n' "$s" "$c" "$dtl"
done
echo
npass=0; nskip=0; ninfo=0
for r in "${ROWS[@]}"; do case "${r%%|*}" in PASS) npass=$((npass+1)) ;; SKIP) nskip=$((nskip+1)) ;; INFO) ninfo=$((ninfo+1)) ;; esac; done
echo "$npass passed, $FAILS failed, $nskip skipped, $ninfo info"
[ "$FAILS" = 0 ]
