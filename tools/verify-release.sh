#!/usr/bin/env bash
# verify-release.sh TAG: check a published Coral release the way a careful user or the release
# manager would, and print one summary table (#4183).
#
#   tools/verify-release.sh v1.4.71
#   tools/verify-release.sh v1.4.71 --prev v1.4.70 --keep
#
# What it does (each result is a row in the table: PASS, FAIL or SKIP with the reason):
#   1. release     the release exists, is published, and is a prerelease exactly when the tag's tier
#                  says so (tests/release/resolve_release.sh); every expected asset is there. With --stage agentic a plain
#                  vX.Y.Z tag is expected to be a PRERELEASE (published, not yet approved: release channels, #5551)
#   2. notes       the release body starts with this version's whole section of RELEASE_NOTES.md at the tag
#   3. sha256      every asset is taken from the release run's artifacts (release.yml at the tag) into a fresh
#                  folder, so our own check doesn't count as a download (#4936; --published downloads them from
#                  the release instead); each file's sha256 equals the published asset's digest (release
#                  metadata, not a download), so these are the published files byte for byte; SHA256SUMS.txt
#                  matches each package, and lists every package (so nothing goes unchecked)
#   4. attest      gh attestation verify for each package and for SHA256SUMS.txt, bound to release.yml at this tag
#   3b. cask       the tap's Casks/coral.rb (written by the Approve release workflow; not touched at stage agentic) is at this version with the DMG's sha256 from
#                  SHA256SUMS.txt; the repo's own cask is checked only once it is at this version. Authenticode is a Windows
#                  signature and is not checked on macOS (the DMG gets spctl, stapler and codesign).
#   5. cosign      cosign verify-blob of every .sigstore.json bundle, when cosign is installed (a
#                  plain SKIP, not a pass, when it is not; with --require-cosign a missing cosign, or a release with
#                  no bundle at all, is a FAIL: what tools/devops/finish-release.sh uses, #5112)
#   6. dmg         hdiutil verify of the DMG (macOS hosts only); spctl, stapler on the DMG and app, codesign,
#                  no entitlements, the licenses folder; its coral prints the version
#   7. contents    the tarball and zip entry counts against the previous release of the same kind, and
#                  every shipped program (tests/release/commands.txt) present
#   8. build       each program's version, build tags and target, from the binary's own build info,
#                  against the tag (go version -m, else strings)
# Exit status 0 only when no row is a FAIL. A SKIP does not fail the run but is printed in the table:
# read it. Everything goes into a new folder under $TMPDIR (removed at the end unless --keep).
#
# Needs: gh (logged in), jq, tar, unzip, shasum or sha256sum. Optional: cosign, go, hdiutil.
# Options: --stage agentic|approved (default approved: a plain tag is a normal release; agentic: published as a pre-release, not yet approved, cask untouched),
#          --repo OWNER/NAME (default: the current home in tools/release-homes.txt; a release is checked against the home that
# built it, also from that table, unless --repo is given), --prev TAG (default: the release before this one
# of the same kind), --no-prev, --keep, --out DIR (use an empty folder you name; implies --keep),
# --local DIR (check assets already in DIR instead of downloading: for a re-check, or to see a tampered copy fail),
# --require-cosign (the cosign row FAILS instead of SKIPping when cosign is missing or there is no bundle to check),
# --published (download the assets from the published release, as a user would: each counts as a download; use it
# when the release run's artifacts have expired or you can't read the repo's Actions).
set -u
set -o pipefail
# The release's own programs are run below: our checks send no usage events (#4936).
export CORAL_TELEMETRY_DISABLED=1

usage() { sed -n '2,/^set -u/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 2; }
. "$(dirname "$0")/release-homes.sh"
. "$(dirname "$0")/release-notes-edits.sh"
TAG=""; REPO="$(release_home_current)"; REPO_GIVEN=0; PREV=""; NOPREV=0; KEEP=0; OUT=""; LOCAL=""; PUBLISHED=0; REQUIRE_COSIGN=0; STAGE=approved
while [ $# -gt 0 ]; do
  case "$1" in
    --repo) REPO="${2:?}"; REPO_GIVEN=1; shift 2 ;;
    --published) PUBLISHED=1; shift ;;
    --require-cosign) REQUIRE_COSIGN=1; shift ;;
    --stage) STAGE="${2:?}"; shift 2 ;;
    --prev) PREV="${2:?}"; shift 2 ;;
    --no-prev) NOPREV=1; shift ;;
    --keep) KEEP=1; shift ;;
    --out) OUT="${2:?}"; KEEP=1; shift 2 ;;
    --local) LOCAL="${2:?}"; shift 2 ;;
    -h|--help) usage ;;
    -*) echo "unknown option $1" >&2; usage ;;
    *) [ -z "$TAG" ] && TAG="$1" || usage; shift ;;
  esac
done
[ -n "$TAG" ] || usage

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/.." && pwd)"
RESOLVE="$root/tests/release/resolve_release.sh"
. "$here/verify_buildinfo.sh"
for need in gh jq tar unzip; do
  command -v "$need" >/dev/null 2>&1 || { echo "verify-release: $need is required" >&2; exit 2; }
done
if command -v sha256sum >/dev/null 2>&1; then SHA() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then SHA() { shasum -a 256 "$1" | awk '{print $1}'; }
else echo "verify-release: sha256sum or shasum is required" >&2; exit 2; fi
[ -f "$RESOLVE" ] || { echo "verify-release: $RESOLVE not found (run from a Coral checkout)" >&2; exit 2; }

# ---- results table ----
ROWS=(); FAILS=0
row() { # status check subject detail
  ROWS+=("$1|$2|$3|$4")
  [ "$1" = FAIL ] && FAILS=$((FAILS + 1))
  printf '  %-4s %-9s %-34s %s\n' "$1" "$2" "$3" "$4" >&2
}
pass() { row PASS "$1" "$2" "${3:-}"; }
fail() { row FAIL "$1" "$2" "${3:-}"; }
skip() { row SKIP "$1" "$2" "${3:-}"; }

# ---- what the tag should be ----
res="$(bash "$RESOLVE" "$TAG")" || { echo "verify-release: $TAG is not a release tag" >&2; exit 2; }
want_ver="$(sed -n 's/^version=//p' <<<"$res")"
want_tags="$(sed -n 's/^build_tags=//p' <<<"$res")"
want_pre="$(sed -n 's/^prerelease=//p' <<<"$res")"
case "$STAGE" in approved) ;; agentic) [ "$want_pre" = false ] && want_pre=true ;; *) echo "verify-release: --stage is agentic or approved" >&2; exit 2 ;; esac
commands="$(sed -n 's/^commands=//p' <<<"$res")"
echo "verify-release $TAG ($REPO): version $want_ver, build tags $want_tags, prerelease $want_pre" >&2

# ---- fresh folder ----
if [ -n "$OUT" ]; then
  mkdir -p "$OUT" && [ -z "$(ls -A "$OUT")" ] || { echo "verify-release: --out must be an empty folder" >&2; exit 2; }
  WORK="$OUT"
else
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/verify-release.XXXXXX")" || exit 2
fi
cleanup() {
  # detach the DMG first: a folder with a mounted image in it can't be removed
  if [ -n "${MNT:-}" ] && [ -d "${MNT:-}" ]; then hdiutil detach "$MNT" -quiet >/dev/null 2>&1 || hdiutil detach "$MNT" -force -quiet >/dev/null 2>&1; fi
  case "$WORK" in */verify-release.*) [ "$KEEP" = 1 ] || rm -rf "$WORK" ;; esac
}
trap cleanup EXIT
DL="$WORK/assets"; mkdir -p "$DL"

# run_artifacts TAG DIR NAME...: move the named artifacts of TAG's release run (release.yml, pushed by the tag)
# into DIR, trying each up to three times; sets LAST_RUN. Fails when there is no such run or an artifact is not
# there (artifacts expire), with the reason in $WORK/run.err.
run_artifacts() {
  local tag="$1" dir="$2" run a got t; shift 2
  run="$(gh run list --repo "$REPO" --workflow release.yml --branch "$tag" --event push --status success --limit 1 \
    --json databaseId -q '.[0].databaseId' 2>/dev/null)"
  [ -n "$run" ] || { echo "no successful release.yml run for $tag" >"$WORK/run.err"; return 1; }
  for a in "$@"; do
    got=0
    for _ in 1 2 3; do
      t="$(mktemp -d "$WORK/art.XXXXXX")" || return 1
      if gh run download "$run" --repo "$REPO" -n "$a" -D "$t" 2>"$WORK/run.err"; then got=1; break; fi
    done
    [ "$got" = 1 ] || return 1
    find "$t" -type f -exec mv {} "$dir/" \;
  done
  LAST_RUN="$run"
}

# ---- 1. the release ----
meta="$(gh release view "$TAG" --repo "$REPO" --json tagName,isDraft,isPrerelease,body,assets,publishedAt 2>&1)" || {
  fail release "$TAG" "gh release view failed: $(head -c 200 <<<"$meta")"; meta=""; }
if [ -n "$meta" ]; then
  [ "$(jq -r .isDraft <<<"$meta")" = false ] && pass release "published" "" || fail release "published" "still a draft"
  got_pre="$(jq -r .isPrerelease <<<"$meta")"
  [ "$got_pre" = "$want_pre" ] && pass release "prerelease flag" "$got_pre, as the tag's tier says" || fail release "prerelease flag" "is $got_pre, the tag says $want_pre"
  names="$(jq -r '.assets[].name' <<<"$meta")"
  has() { grep -qxF "$1" <<<"$names"; }
  need=("coral-linux-amd64-$want_ver.tar.gz" "coral-linux-arm64-$want_ver.tar.gz"
        "coral-linux-amd64-$want_ver.tar.gz.sigstore.json" "coral-linux-arm64-$want_ver.tar.gz.sigstore.json"
        "Coral-$want_ver-x64.msi" "Coral-$want_ver-x64-portable.zip" "SHA256SUMS.txt" "SHA256SUMS.txt.sigstore.json")
  # the stable-name copies (#5333): the same bytes without the version in the name, and the Linux ones' bundles; releases from 1.4.130
  # on carry them, an older one is verified without (re-checking an old tag still passes)
  if release_has_stable_names "$want_ver"; then
    need+=("coral-linux-amd64.tar.gz" "coral-linux-arm64.tar.gz" "coral-linux-amd64.tar.gz.sigstore.json" "coral-linux-arm64.tar.gz.sigstore.json"
           "Coral.dmg" "Coral-x64.msi" "Coral-x64-portable.zip")
  fi
  missing=""
  for n in "${need[@]}"; do has "$n" || missing="$missing $n"; done
  dmg="$(grep -E '^Coral\.v.*\.dmg$' <<<"$names" | head -1)"
  [ -n "$dmg" ] || missing="$missing (a versioned .dmg)"
  [ "$(grep -cE '^Coral\.v.*\.dmg$' <<<"$names")" -le 1 ] || missing="$missing (more than one versioned .dmg)"
  [ -z "$missing" ] && pass release "assets" "$(wc -l <<<"$names" | tr -d ' ') present, none missing" || fail release "assets" "missing:$missing"
  zeros="$(jq -r '.assets[] | select(.size == 0) | .name' <<<"$meta")"
  [ -z "$zeros" ] || fail release "empty assets" "$zeros"
fi

# ---- 2. the notes ----
if [ -n "$meta" ]; then
  body="$(jq -r .body <<<"$meta")"
  # A release from 1.4.134 on starts with the Download section (#5467: finish-release.sh puts it above the notes, "### Download" up to its "---" line):
  # it is checked on its own below, and the notes are compared without it.
  dlsec=""
  if [ "$(head -n 1 <<<"$body")" = "### Download" ]; then
    dlsec="$(awk 'NR==1{next} /^---$/{exit} {print}' <<<"$body")"
    body="$(awk 'st==0 && NR==1 && $0=="### Download"{st=1; next} st==1{ if ($0=="---") st=2; next } st==2{ st=3; if ($0=="") next } {print}' <<<"$body")"
  fi
  notes="$(gh api "repos/$REPO/contents/RELEASE_NOTES.md?ref=$TAG" -q .content 2>/dev/null | base64 -d 2>/dev/null)"
  if [ -z "$notes" ]; then
    fail notes "RELEASE_NOTES.md at $TAG" "could not be read"
  else
    # this version's section: the lines after its "## vX.Y.Z" heading, up to the next "## "
    section="$(awk -v v="## v$want_ver" 'index($0, v) == 1 && substr($0, length(v)+1, 1) !~ /[0-9.]/ {on=1; next} on && /^## / {exit} on {print}' <<<"$notes" | sed '/./,$!d')"
    section="$(notes_drop_edited "$TAG" <<<"$section")"   # lines removed from the published body on purpose (tools/release-notes-edits.txt)
    if [ -z "$section" ]; then
      fail notes "section for v$want_ver" "RELEASE_NOTES.md at the tag has none"
    else
      # the whole section, not its first lines: an edit further down is the one that gets missed
      nlines="$(wc -l <<<"$section" | tr -d ' ')"
      if [ "$(head -n "$nlines" <<<"$body")" = "$section" ]; then
        pass notes "body starts with the notes" "all $nlines lines of the section match"
      else
        firstdiff="$(diff <(printf '%s\n' "$section") <(printf '%s\n' "$body" | head -n "$nlines") | sed -n '2p' | cut -c1-70)"
        fail notes "body starts with the notes" "differs from RELEASE_NOTES.md at the tag; first difference (notes line): ${firstdiff:-body is shorter}"
      fi
    fi
  fi
  # The Download section (#5467), from 1.4.134 on: the five stable-name files of this tag, named by the release home. A Homebrew line is neither required nor refused (releases before 144 carry one; later ones do not advertise the tap).
  if [ "$(printf '%s\n%s\n' 1.4.134 "${want_ver%%-*}" | sort -V | head -1)" = 1.4.134 ]; then
    if [ -z "$dlsec" ]; then
      fail notes "Download section" "the body does not start with '### Download'"
    else
      dlhome="$(release_home_for_tag "$TAG")"; dlbase="https://github.com/$dlhome/releases/download/$TAG"; dlmiss=""
      for f in Coral.dmg Coral-x64.msi Coral-x64-portable.zip coral-linux-amd64.tar.gz coral-linux-arm64.tar.gz; do
        grep -qF "($dlbase/$f)" <<<"$dlsec" || dlmiss="$dlmiss $f"
      done
      [ -z "$dlmiss" ] && pass notes "Download section" "the five stable files of $TAG" || fail notes "Download section" "missing:$dlmiss"
    fi
  fi
fi

# ---- download ----
if [ -n "$meta" ]; then
  if [ -n "$LOCAL" ]; then
    DL="$(cd "$LOCAL" && pwd)"
    pass download "local folder" "$DL ($(ls "$DL" | wc -l | tr -d ' ') files, not downloaded)"
  elif [ "$PUBLISHED" = 1 ]; then
    echo "  downloading every asset to $DL ..." >&2
    if gh release download "$TAG" --repo "$REPO" --dir "$DL" 2>"$WORK/dl.err"; then
      pass download "all assets" "$(ls "$DL" | wc -l | tr -d ' ') files, downloaded from the release (--published)"
    else
      fail download "all assets" "$(head -c 200 "$WORK/dl.err")"
    fi
  else
    echo "  taking every asset from the release run's artifacts into $DL ..." >&2
    if run_artifacts "$TAG" "$DL" coral-linux-amd64 coral-linux-arm64 coral-windows-msi coral-windows-portable coral-macos-dmg; then
      # the stable-name copies (#5333) are made in the release job and kept as their own run artifact, from 1.4.130 on
      if release_has_stable_names "$want_ver" && ! run_artifacts "$TAG" "$DL" release-stable; then
        fail download "stable-name copies" "$(head -c 160 "$WORK/run.err") (the release-stable run artifact)"
      fi
      if run_artifacts "$TAG" "$DL" release-sums; then
        pass download "release run $LAST_RUN" "$(ls "$DL" | wc -l | tr -d ' ') files from its artifacts, none from the release"
      # a run from before #4936 has no release-sums artifact: only the small sums and bundles come from the release
      elif gh release download "$TAG" --repo "$REPO" --dir "$DL" -p 'SHA256SUMS.txt' -p '*.sigstore.json' 2>"$WORK/dl.err"; then
        pass download "release run $LAST_RUN" "packages from its artifacts; SHA256SUMS.txt and the bundles from the release (no release-sums artifact)"
      else
        fail download "SHA256SUMS.txt and the bundles" "$(head -c 200 "$WORK/dl.err")"
      fi
    else
      fail download "release run artifacts" "$(head -c 160 "$WORK/run.err") (expired? --published downloads from the release)"
    fi
  fi
fi
cd "$DL" 2>/dev/null || { echo "verify-release: cannot enter $DL" >&2; exit 2; }
packages=()
for f in *; do
  case "$f" in *.sigstore.json|SHA256SUMS.txt|'*') ;; *) [ -f "$f" ] && packages+=("$f") ;; esac
done

# ---- the published digests: the files checked here are the published ones, byte for byte ----
if [ -n "$meta" ]; then
  while IFS=$'\t' read -r name digest; do
    [ -n "$name" ] || continue
    if [ ! -f "$name" ]; then fail published "$name" "a published asset, missing from the files checked"; continue; fi
    if [ -z "$digest" ]; then skip published "$name" "GitHub gives no digest for it"; continue; fi
    got="$(SHA "$name")"
    [ "$got" = "${digest#sha256:}" ] && pass published "$name" "same bytes as the published asset" \
      || fail published "$name" "got ${got:0:12}..., the published asset is ${digest:7:12}..."
  done < <(jq -r '.assets[] | [.name, (.digest // "")] | @tsv' <<<"$meta")
fi

# ---- 3. SHA256SUMS ----
if [ -f SHA256SUMS.txt ]; then
  listed=""
  while read -r sum file; do
    [ -n "${file:-}" ] || continue
    listed="$listed $file"
    if [ ! -f "$file" ]; then fail sha256 "$file" "listed in SHA256SUMS.txt, not in the release"; continue; fi
    got="$(SHA "$file")"
    [ "$got" = "$sum" ] && pass sha256 "$file" "${sum:0:12}..." || fail sha256 "$file" "got ${got:0:12}..., SHA256SUMS.txt says ${sum:0:12}..."
  done < SHA256SUMS.txt
  for p in "${packages[@]+"${packages[@]}"}"; do
    case " $listed " in *" $p "*) ;; *) fail sha256 "$p" "not listed in SHA256SUMS.txt: nothing checked it" ;; esac
  done
else
  fail sha256 "SHA256SUMS.txt" "missing"
fi

# ---- 3c. the stable-name copies (#5333): the same bytes as the versioned file, listed under both names ----
if [ -f SHA256SUMS.txt ] && ! release_has_stable_names "$want_ver"; then
  skip alias "stable-name copies" "$want_ver is before 1.4.130: it has none"
elif [ -f SHA256SUMS.txt ]; then
  for pair in "coral-linux-amd64-$want_ver.tar.gz:coral-linux-amd64.tar.gz" "coral-linux-arm64-$want_ver.tar.gz:coral-linux-arm64.tar.gz" \
              "Coral.v$want_ver.dmg:Coral.dmg" "Coral-$want_ver-x64.msi:Coral-x64.msi" "Coral-$want_ver-x64-portable.zip:Coral-x64-portable.zip"; do
    ver="${pair%%:*}"; stable="${pair#*:}"
    if [ ! -f "$ver" ] || [ ! -f "$stable" ]; then fail alias "$stable" "the pair $ver and $stable is not both in the release"; continue; fi
    a="$(SHA "$ver")"; b="$(SHA "$stable")"
    la="$(awk -v n="$ver" '$2==n {print $1}' SHA256SUMS.txt)"; lb="$(awk -v n="$stable" '$2==n {print $1}' SHA256SUMS.txt)"
    if [ "$a" != "$b" ]; then fail alias "$stable" "is not the same bytes as $ver (${b:0:12}... against ${a:0:12}...)"
    elif [ "$la" != "$a" ] || [ "$lb" != "$b" ]; then fail alias "$stable" "SHA256SUMS.txt lists $ver as ${la:0:12}... and $stable as ${lb:0:12}..., the files are ${a:0:12}..."
    else pass alias "$stable" "the same bytes as $ver, listed under both names (${a:0:12}...)"; fi
  done
fi

# ---- 3b. the Homebrew cask: the sha256 users install with is the one SHA256SUMS.txt lists (#4997, Security's INFO) ----
# The Approve release workflow's tap job writes the tap's Casks/coral.rb at the approval, so it is checked at --stage approved; the repo's own
# Casks/coral.rb is made after verification (tools/devops/cask.sh), so it is checked only when it is already at this version.
# Authenticode is not checked on macOS (it is a Windows signature): the DMG is checked below with spctl, stapler and codesign.
if [ -f SHA256SUMS.txt ]; then
  dmg_sum="$(awk '$2 ~ /^Coral\.v.*\.dmg$/ {print $1; exit}' SHA256SUMS.txt)"
  for spec in "tap:$(release_tap_repo "$REPO")" "repo:$REPO"; do
    which_="${spec%%:*}"; cask_repo="${spec#*:}"
    cask="$(gh api "repos/$cask_repo/contents/Casks/coral.rb" -q .content 2>/dev/null | base64 -d 2>/dev/null || true)"
    if [ -z "$cask" ]; then skip cask "$which_ Casks/coral.rb" "could not be read ($cask_repo)"; continue; fi
    cask_ver="$(awk -F'"' '/^  version /{print $2; exit}' <<<"$cask")"; cask_sum="$(awk -F'"' '/^  sha256 /{print $2; exit}' <<<"$cask")"
    if [ "$cask_ver" != "$want_ver" ]; then
      if [ "$STAGE" = agentic ]; then skip cask "$which_ Casks/coral.rb" "at $cask_ver: the cask follows the approval, not the publication (--stage agentic)"
      elif [ "$which_" = repo ]; then skip cask "$which_ Casks/coral.rb" "at $cask_ver: it is made after verification (tools/devops/cask.sh)"
      elif [[ "$cask_ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && [ "$(printf '%s\n%s\n' "$cask_ver" "$want_ver" | sort -V | tail -1)" = "$cask_ver" ]; then
        # a LATER approved release moved the cask on (the weekly re-verify checks the newest three): only a cask that is OLDER than this approved release is a failure
        skip cask "$which_ Casks/coral.rb" "at $cask_ver: a newer release than $want_ver was approved since"
      else fail cask "$which_ Casks/coral.rb" "at version ${cask_ver:-none}, expected $want_ver"; fi
    elif [ -n "$dmg_sum" ] && [ "$cask_sum" = "$dmg_sum" ]; then pass cask "$which_ Casks/coral.rb" "sha256 equals the DMG's line in SHA256SUMS.txt (${cask_sum:0:12}...)"
    else fail cask "$which_ Casks/coral.rb" "sha256 ${cask_sum:0:12}... is not the DMG's line in SHA256SUMS.txt (${dmg_sum:0:12}...)"; fi
  done
fi

# ---- 4. GitHub attestations ----
# The certificate and the attestation name the repository that built the release, which a later move does not change: a tag is
# checked against its own home (tools/release-homes.txt), the files are fetched from the current one.
ID_REPO="$REPO"; [ "$REPO_GIVEN" = 1 ] || ID_REPO="$(release_home_for_tag "$TAG")"
[ -n "$ID_REPO" ] || ID_REPO="$REPO"
SIGNER="$ID_REPO/.github/workflows/release.yml"
REPO_RE="$(sed 's/\./\\./g' <<<"$ID_REPO")"; TAG_RE="$(sed 's/[.]/\\./g' <<<"$TAG")"
for p in "${packages[@]+"${packages[@]}"}" SHA256SUMS.txt; do
  [ -f "$p" ] || continue
  # By OWNER, not --repo: after a repository transfer GitHub's per-repository lookup answers 404 for the attestations made before it (they stay with the owner
  # that built the release, #5525), while the owner lookup still finds them; --signer-workflow still pins the exact repository and workflow, and --source-ref the tag.
  if out="$(gh attestation verify "$p" --owner "${ID_REPO%%/*}" --signer-workflow "$SIGNER" --source-ref "refs/tags/$TAG" 2>&1)"; then pass attest "$p" "release.yml at refs/tags/$TAG"
  else fail attest "$p" "$(tail -1 <<<"$out" | cut -c1-120)"; fi
done

# ---- 5. cosign ----
if command -v cosign >/dev/null 2>&1; then
  bundles=0
  for b in *.sigstore.json; do
    [ -f "$b" ] || continue
    bundles=$((bundles + 1))
    blob="${b%.sigstore.json}"
    if out="$(cosign verify-blob "$blob" --bundle "$b" --new-bundle-format \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com \
        --certificate-identity-regexp "^https://github\\.com/$REPO_RE/\\.github/workflows/release\\.yml@refs/tags/$TAG_RE\$" 2>&1)"; then
      pass cosign "$b" "Verified OK"
    else fail cosign "$b" "$(tail -1 <<<"$out" | cut -c1-120)"; fi
  done
  if [ "$bundles" = 0 ] && [ "$REQUIRE_COSIGN" = 1 ]; then
    fail cosign "all .sigstore.json bundles" "there is no .sigstore.json bundle to check (--require-cosign)"
  fi
elif [ "$REQUIRE_COSIGN" = 1 ]; then
  fail cosign "all .sigstore.json bundles" "cosign is required (--require-cosign) and is not installed: tools/devops/install-cosign.sh"
else
  skip cosign "all .sigstore.json bundles" "cosign is not installed: bundles NOT checked (the gh attestation rows above do cover the packages)"
fi

# ---- 6. the DMG ----
dmgfile="$(ls Coral.v*.dmg 2>/dev/null | head -1)"   # the versioned file; Coral.dmg is a byte-identical copy (checked in 3c)
if [ -n "$dmgfile" ]; then
  if command -v hdiutil >/dev/null 2>&1; then
    if out="$(hdiutil verify "$dmgfile" 2>&1)"; then pass dmg "$dmgfile" "hdiutil verify: $(tail -1 <<<"$out" | cut -c1-60)"
    else fail dmg "$dmgfile" "$(tail -1 <<<"$out" | cut -c1-120)"; fi
    MNT="$WORK/mnt"; mkdir -p "$MNT"
    if hdiutil attach "$dmgfile" -readonly -nobrowse -noverify -mountpoint "$MNT" -quiet >/dev/null 2>&1; then
      app="$(ls -d "$MNT"/*.app 2>/dev/null | head -1)"
      if [ -n "$app" ] && [ -x "$app/Contents/MacOS/coral" ]; then
        v="$("$app/Contents/MacOS/coral" --version 2>&1 | head -1)"
        [ "$v" = "coral $want_ver" ] && pass dmg "app's coral --version" "$v" || fail dmg "app's coral --version" "printed '$v', want 'coral $want_ver'"
        DMGBIN="$app/Contents/MacOS"
        # Gatekeeper, the notarization tickets, the signature and the entitlements, as a user's Mac sees them
        if out="$(spctl -a -t open --context context:primary-signature -v "$dmgfile" 2>&1)"; then pass dmg "spctl on the DMG" "$(tail -1 <<<"$out" | cut -c1-70)"
        else fail dmg "spctl on the DMG" "$(tail -1 <<<"$out" | cut -c1-100)"; fi
        if command -v xcrun >/dev/null 2>&1; then
          for target in "$dmgfile" "$app"; do
            if out="$(xcrun stapler validate "$target" 2>&1)"; then pass dmg "stapler: $(basename "$target")" "ticket stapled and valid"
            else fail dmg "stapler: $(basename "$target")" "$(tail -1 <<<"$out" | cut -c1-100)"; fi
          done
        else skip dmg "stapler" "xcrun is not available"; fi
        if out="$(codesign --verify --deep --strict "$app" 2>&1)"; then pass dmg "codesign --verify --deep" "valid"
        else fail dmg "codesign --verify --deep" "$(tail -1 <<<"$out" | cut -c1-100)"; fi
        ents="$(codesign -d --entitlements :- "$app" 2>/dev/null | grep -c '<key>')"
        [ "${ents:-0}" = 0 ] && pass dmg "app entitlements" "none declared" || fail dmg "app entitlements" "$ents entitlement keys declared (none expected)"
        lic="$(find "$app" -maxdepth 4 -type d -name licenses | head -1)"
        [ -n "$lic" ] && pass dmg "licenses folder in the app" "present" || fail dmg "licenses folder in the app" "none under the app"
      else
        fail dmg "app" "no Contents/MacOS/coral in the mounted image"
      fi
    else
      skip dmg "mount and run the app" "hdiutil attach failed on this host"
      MNT=""
    fi
  else
    skip dmg "$dmgfile" "hdiutil is not available: not a macOS host, DMG NOT checked"
  fi
fi

# ---- build info of a program ----
buildinfo() { # file -> "version|tags|goos/goarch|trimpath|vcs.revision|vcs.modified" (empty fields when unknown)
  local f="$1" gi
  if command -v go >/dev/null 2>&1 && gi="$(go version -m "$f" 2>/dev/null)" && [ -n "$gi" ]; then
    bi_parse "$gi"
  else
    echo "$(strings -a "$f" 2>/dev/null | grep -o 'config\.Version=[^" ]*' | head -1 | sed 's/.*=//')|$(strings -a "$f" 2>/dev/null | grep -o -- '-tags=[^ ]*' | head -1 | sed 's/-tags=//')|||||"
  fi
}
# the commit the release tag points at, fetched from the repo (peeled: an annotated tag gives its commit, never the tag object); "-" with --local, "" when it cannot be read
tag_commit() {
  [ -n "$LOCAL" ] && { echo "-"; return; }
  gh api "repos/$REPO/commits/$TAG" --jq .sha 2>/dev/null | grep -E '^[0-9a-f]{40}$' || true
}
TAG_COMMIT="$(tag_commit)"
check_programs() { # subject folder ext want-target
  local subject="$1" dir="$2" ext="$3" wtgt="$4" n=0 bad="" c f info v t tg trim rev mod verdict
  for c in $commands; do
    f="$dir/$c$ext"
    [ -f "$f" ] || { bad="$bad $c(missing)"; continue; }
    n=$((n + 1))
    # A Windows program's build info says vcs.modified=true on purpose (#6184): release.yml runs tools/gen_windows_resources.sh, which rewrites the four tracked
    # rsrc_windows_amd64.syso files with the release version before compiling. bi_verdict lets that through for GOOS=windows only, with vcs.revision still
    # required to be the tag's commit; on Windows the revision therefore does not prove the tracked files unchanged, and the Authenticode signature when the
    # build is signed (the signing secrets are optional), and always the attestation and Sigstore checks above, are what bind those files to us. The build info is not made to say "clean" (skip-worktree would): it must not lie.
    info="$(buildinfo "$f")"; IFS='|' read -r v t tg trim rev mod <<<"$info"
    if [ -z "$TAG_COMMIT" ]; then bad="$bad $c(the tag's commit could not be read from $REPO)"
    elif ! verdict="$(bi_verdict "$want_ver" "$TAG_COMMIT" "$v" "$trim" "$rev" "$mod" "$tg")"; then bad="$bad $c(${verdict#fail|})"; fi
    if [ "$t" != "$want_tags" ]; then bad="$bad $c(tags '${t:-none}')"; fi
    if [ -n "$wtgt" ] && [ -n "$tg" ] && [ "$tg" != "$wtgt" ]; then bad="$bad $c(target $tg)"; fi
  done
  [ -z "$bad" ] && pass build "$subject" "$n programs: version $want_ver, tags $want_tags${wtgt:+, $wtgt}" || fail build "$subject" "$bad"
}

# ---- 7 and 8. the tarballs and the zip ----
count_tar() { tar tzf "$1" 2>/dev/null | wc -l | tr -d ' '; }
count_zip() { unzip -Z1 "$1" 2>/dev/null | wc -l | tr -d ' '; }

PREVDIR="$WORK/prev"
if [ "$NOPREV" = 0 ] && [ -n "$meta" ]; then
  if [ -z "$PREV" ]; then
    PREV="$(gh release list --repo "$REPO" --limit 40 --exclude-drafts --json tagName,isPrerelease,publishedAt 2>/dev/null |
      jq -r --arg t "$TAG" --argjson pre "$want_pre" '
        (map(select(.tagName == $t)) | first | .publishedAt) as $at
        | map(select(.tagName != $t and (.tagName | startswith("promote-") | not) and .isPrerelease == $pre and .publishedAt < $at))
        | sort_by(.publishedAt) | last | .tagName // empty')"   # a promote-* release (#326) is a promotion, never a product release
  fi
  if [ -n "$PREV" ]; then
    mkdir -p "$PREVDIR"
    pv="${PREV#v}"
    if [ "$PUBLISHED" = 1 ]; then
      gh release download "$PREV" --repo "$REPO" --dir "$PREVDIR" --pattern "coral-linux-*-$pv.tar.gz" --pattern "Coral-$pv-x64-portable.zip" 2>/dev/null \
        || skip contents "previous release $PREV" "could not be downloaded; entry counts not compared"
    else
      run_artifacts "$PREV" "$PREVDIR" coral-linux-amd64 coral-linux-arm64 coral-windows-portable \
        || skip contents "previous release $PREV" "its release run's artifacts are gone (expired?); entry counts not compared (--published downloads them)"
    fi
  else
    skip contents "previous release" "none found of the same kind; entry counts not compared (use --prev)"
  fi
fi

# The growth guard is 25% of the previous release. 1.4.153 ships coral-update, whose libraries add about 175 third-party licence entries to the Linux
# tarballs and about 110 to the Windows ZIP (+51% and +47%, read and explained in the release notes): its limit is 155%. The same entries are in
# 1.4.154 to 1.4.156: at --stage approved the previous release is the last APPROVED one (1.4.147, before coral-update), so each of them still
# shows that jump until 1.4.153 or a later release is approved. Remove this case once an approved release carries coral-update (the next
# release then compares with it and the guard is 25% again).
growth_limit=125
case "$want_ver" in 1.4.153|1.4.154|1.4.155|1.4.156) growth_limit=155 ;; esac
compare_count() { # file count countfn
  local f="$1" n="$2" fn="$3" pf pn
  pf="$PREVDIR/$(sed "s/$want_ver/${PREV#v}/" <<<"$f")"
  if [ -n "$PREV" ] && [ -f "$pf" ]; then
    pn="$($fn "$pf")"
    if [ "$n" -lt "$pn" ]; then fail contents "$f" "$n entries, $PREV had $pn: entries went missing"
    elif [ $((n * 100)) -gt $((pn * growth_limit)) ]; then fail contents "$f" "$n entries, $PREV had $pn: grew by more than $((growth_limit - 100))%"
    else pass contents "$f" "$n entries ($PREV: $pn, $(printf '%+d' $((n - pn))))"; fi
  else
    [ "$NOPREV" = 1 ] && skip contents "$f" "$n entries, not compared (--no-prev)" || skip contents "$f" "$n entries, no previous release to compare"
  fi
}

for arch in amd64 arm64; do
  tgz="coral-linux-$arch-$want_ver.tar.gz"
  [ -f "$tgz" ] || continue
  n="$(count_tar "$tgz")"
  compare_count "$tgz" "$n" count_tar
  d="$WORK/x-$arch"; mkdir -p "$d"
  if tar xzf "$tgz" -C "$d" 2>/dev/null; then
    top="$(ls "$d")"; [ "$(ls "$d" | wc -l | tr -d ' ')" = 1 ] && [ -d "$d/$top" ] && d="$d/$top"
    check_programs "$tgz" "$d" "" "linux/$arch"
    [ -d "$d/licenses" ] && pass contents "$tgz licenses" "licenses folder present" || fail contents "$tgz licenses" "no licenses folder"
  else fail build "$tgz" "does not extract"; fi
done
zipf="Coral-$want_ver-x64-portable.zip"
if [ -f "$zipf" ]; then
  n="$(count_zip "$zipf")"
  compare_count "$zipf" "$n" count_zip
  d="$WORK/x-win"; mkdir -p "$d"
  if unzip -q "$zipf" -d "$d" 2>/dev/null; then
    wd="$d"; exe="$(find "$d" -name coral.exe -type f | head -1)"; [ -n "$exe" ] && wd="$(dirname "$exe")"
    check_programs "$zipf" "$wd" ".exe" "windows/amd64"
    [ -d "$wd/licenses" ] && pass contents "$zipf licenses" "licenses folder present" || fail contents "$zipf licenses" "no licenses folder"
  else fail build "$zipf" "does not extract"; fi
fi
if [ -n "${DMGBIN:-}" ]; then
  # the app's programs: the commands the DMG carries (the others ship as the CLI links), by name
  c="$DMGBIN/coral"; info="$(buildinfo "$c")"; v="${info%%|*}"; t="${info#*|}"; t="${t%%|*}"
  [ "$t" = "$want_tags" ] && [ "${v:-$want_ver}" = "$want_ver" ] && pass build "DMG coral" "version ${v:-$want_ver}, tags $t" || fail build "DMG coral" "version '$v', tags '$t' (want $want_ver, $want_tags)"
fi

# ---- summary ----
echo >&2
echo "=== verify-release $TAG: summary ==="
printf '%-5s %-9s %-34s %s\n' STATUS CHECK SUBJECT DETAIL
for r in "${ROWS[@]+"${ROWS[@]}"}"; do
  IFS='|' read -r s c sub det <<<"$r"
  printf '%-5s %-9s %-34s %s\n' "$s" "$c" "$sub" "$det"
done
np=0; ns=0
for r in "${ROWS[@]+"${ROWS[@]}"}"; do case "$r" in PASS*) np=$((np+1)) ;; SKIP*) ns=$((ns+1)) ;; esac; done
echo "--- $np passed, $FAILS failed, $ns skipped ---"
[ "$KEEP" = 1 ] && echo "files kept in $WORK"
[ "$FAILS" = 0 ]
