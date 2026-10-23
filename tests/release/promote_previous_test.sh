#!/usr/bin/env bash
# tools/devops/promote.sh previous against a stand-in gh and cosign (#143, Security): no network, no GitHub, no Sigstore. It holds the rule that
# a promote-<n> is junk only when what it holds was read and is wrong, and that a name that can't be read or checked stops the run.
#   gh: `gh api ...matching-refs...` lists $FIX/refs; `gh api -i repos/.../releases/tags/promote-N` answers $FIX/N/status (200 by default, 404
#   when there is no $FIX/N, "none" for no answer at all; $FIX/N/flaky=K: no answer for the first K tries) with $FIX/N/assets as the release;
#   `gh release download promote-N` copies $FIX/N/promote.json*.
#   cosign: verify-blob accepts a bundle whose content is "valid"; with $FIX/cosign_down it refuses everything (Sigstore out of reach).
set -uo pipefail
export CORAL_TELEMETRY_DISABLED=1
here="$(cd "$(dirname "$0")" && pwd)"; SCRIPT="$here/../../tools/devops/promote.sh"
T="$(mktemp -d /tmp/promote-prev.XXXX)"; [ -d "$T" ] || exit 1
trap 'rm -r "$T"' EXIT
mkdir -p "$T/bin"
cat > "$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  api)
    if [ "$2" = -i ]; then
      n="${3##*/promote-}"; d="$FIX/$n"
      [ -d "$d" ] || { printf 'HTTP/2.0 404 Not Found\n\n{"message":"Not Found"}\n'; exit 1; }
      if [ -f "$d/flaky" ]; then k="$(cat "$d/flaky")"; if [ "$k" -gt 0 ]; then echo $(( k - 1 )) > "$d/flaky"; exit 1; fi; fi
      st="$(cat "$d/status" 2>/dev/null || echo 200)"
      [ "$st" = none ] && exit 1
      printf 'HTTP/2.0 %s X\nContent-Type: application/json\n\n' "$st"
      if [ "$st" = 200 ]; then cat "$d/assets"; fi
      [ "$st" = 200 ]
    else
      sed 's#^#refs/tags/promote-#' "$FIX/refs"
    fi ;;
  release)
    [ "$2" = download ] || exit 1
    n="${3#promote-}"; dir=""; shift 3
    while [ $# -gt 0 ]; do [ "$1" = --dir ] && dir="$2"; shift; done
    cp "$FIX/$n/promote.json" "$FIX/$n/promote.json.sigstore.json" "$dir/" 2>/dev/null ;;
esac
EOF
cat > "$T/bin/cosign" <<'EOF'
#!/usr/bin/env bash
[ -f "$FIX/cosign_down" ] && exit 1
b=""; while [ $# -gt 0 ]; do [ "$1" = --bundle ] && b="$2"; shift; done
[ "$(cat "$b")" = valid ]
EOF
cat > "$T/bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$T/bin/gh" "$T/bin/cosign" "$T/bin/sleep"
export PATH="$T/bin:$PATH" GITHUB_REPOSITORY=subgentic/coral TAG=v1.4.158 PROMOTE_RETRY_SLEEP=0 GH_TOKEN=x
fail=0
ok() { echo "PASS $1"; }
bad() { echo "FAIL $1" >&2; fail=1; }

# world: a fresh fixture and work folder (the sums canary the "sums" step left: valid)
world() {
  export FIX="$T/fix"; rm -rf "$FIX" "$T/run"; mkdir -p "$FIX" "$T/run/work"; : > "$FIX/refs"
  echo sums > "$T/run/work/SHA256SUMS.txt"; echo valid > "$T/run/work/SHA256SUMS.txt.sigstore.json"; printf '%040d\n' 0 | tr 0 a > "$T/run/work/source_sha"
}
# real N: a promotion of sequence N, signed; junk N KIND: unsigned, another sequence, no promote.json, or a tag without a release
real() { mkdir -p "$FIX/$1"; echo "$1" >> "$FIX/refs"; printf '{"assets":[{"name":"promote.json"},{"name":"promote.json.sigstore.json"}]}' > "$FIX/$1/assets"
  printf '{"sequence":%s,"tag":"v1.4.%s","expires_at":"x","revoked":[]}' "$1" "$1" > "$FIX/$1/promote.json"; echo valid > "$FIX/$1/promote.json.sigstore.json"; }
junk() {
  case "$2" in
    unsigned) real "$1"; echo forged > "$FIX/$1/promote.json.sigstore.json" ;;
    sequence) real "$1"; printf '{"sequence":1,"tag":"v1.4.1","expires_at":"x","revoked":[]}' > "$FIX/$1/promote.json" ;;
    noasset) real "$1"; printf '{"assets":[{"name":"README"}]}' > "$FIX/$1/assets" ;;
    norelease) echo "$1" >> "$FIX/refs" ;;
  esac
}
prev() { (cd "$T/run" && bash "$SCRIPT" previous 2>&1); }
seqof() { cat "$T/run/work/previous/sequence"; }

world; real 1; real 2; junk 3 unsigned; junk 4 sequence; junk 5 noasset; junk 6 norelease
if out="$(prev)" && [ "$(seqof)" = 2 ] && [ "$(cat "$T/run/work/previous/junk_above")" = 4 ]; then ok "four kinds of junk are passed over and the real promote-2 is found"; else bad "junk passed over: $out"; fi

world; real 1; junk 2 unsigned; mkdir -p "$FIX/3"; echo 3 >> "$FIX/refs"; echo none > "$FIX/3/status"
if out="$(prev)"; then bad "a name GitHub never answered for was skipped: $out"; elif grep -q "could not be read or checked after 3 tries" <<<"$out"; then ok "a name that can't be read stops the run (never skipped)"; else bad "unreadable name: $out"; fi

world; real 1; real 2; echo 500 > "$FIX/2/status"
if out="$(prev)"; then bad "a 5xx was skipped: $out"; elif grep -q "promote-2 could not be read" <<<"$out"; then ok "a server error stops the run after 3 tries"; else bad "5xx: $out"; fi

world; real 1; real 2; echo 2 > "$FIX/2/flaky"
if out="$(prev)" && [ "$(seqof)" = 2 ]; then ok "two failed tries then an answer: promote-2 is found"; else bad "flaky: $out"; fi

world; real 1; real 2; touch "$FIX/cosign_down"
if out="$(prev)"; then bad "cosign unable to check was taken as junk: $out"; elif grep -q "cosign could not check anything" <<<"$out"; then ok "cosign unable to check stops the run, not a skip"; else bad "cosign down: $out"; fi

world; junk 1 unsigned; junk 2 norelease; junk 3 noasset
if out="$(prev)" && [ "$(seqof)" = 0 ] && [ ! -f "$T/run/work/previous/promote.json" ] && grep -q "no promotion yet" <<<"$out"; then ok "every name junk (fewer than 128): no promotion yet"; else bad "all junk: $out"; fi

world; for n in $(seq 1 130); do junk "$n" unsigned; done
if out="$(prev)"; then bad "128 junk names in a row were accepted: $out"; elif grep -q "128 promote-<n> names in a row are junk" <<<"$out"; then ok "128 junk names in a row stop the run"; else bad "128 junk: $out"; fi

world
if out="$(prev)" && [ "$(seqof)" = 0 ] && grep -q "no promotion yet" <<<"$out"; then ok "no promote-* tag at all: no promotion yet"; else bad "none: $out"; fi

[ "$fail" = 0 ] && echo "promote previous: all cases passed"
exit "$fail"
