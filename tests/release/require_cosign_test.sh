#!/usr/bin/env bash
# require_cosign_test.sh (#5112): the release checks need the PINNED cosign. A missing or unpinned cosign stops after-release.sh and
# finish-release.sh (lib.sh require_cosign); install-cosign.sh installs only a binary that matches tools/devops/cosign.sha256; and verify-release.sh
# --require-cosign turns the cosign row's SKIP into a FAIL (a missing cosign, or no bundle to check). Runs with fake cosign, gh and jq-free paths:
# nothing here touches the network or the real tool.
set -u
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/../.." && pwd)"
fail=0
bad() { echo "FAIL $*"; fail=1; }
T="$(mktemp -d "${TMPDIR:-/tmp}/require-cosign-test.XXXXXX")" || exit 2
trap 'rm -rf -- "$T"' EXIT
SYS=/usr/bin:/bin:/usr/sbin:/sbin
mkdir -p "$T/none" "$T/fake" "$T/ghbin" "$T/assets"
plat="$(. "$root/tools/devops/lib.sh" && cosign_platform)" || { echo "SKIP require_cosign: no pinned platform on this machine"; exit 0; }
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }

# A fake cosign that always verifies, and the pin file that names it.
printf '#!/bin/sh\nexit 0\n' > "$T/fake/cosign"; chmod +x "$T/fake/cosign"
printf '# test pins\n%s  cosign-%s\n' "$(sha "$T/fake/cosign")" "$plat" > "$T/pins.good"
printf '# test pins\n%s  cosign-%s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$plat" > "$T/pins.bad"

req() { # PATHDIRS PINS: run lib.sh's require_cosign with that PATH and pin file, print its output, return its status
  (cd "$root" && PATH="$1:$SYS" COSIGN_PINS_FILE="$2" bash -c '. tools/devops/lib.sh && require_cosign' 2>&1)
}

out="$(req "$T/none" "$T/pins.good")"; rc=$?
[ "$rc" != 0 ] && grep -q "not on PATH" <<<"$out" && grep -q install-cosign.sh <<<"$out" || bad "no cosign on PATH must stop, naming install-cosign.sh (rc=$rc: $out)"
out="$(req "$T/fake" "$T/pins.bad")"; rc=$?
[ "$rc" != 0 ] && grep -q "not the pinned" <<<"$out" || bad "a cosign that is not the pinned binary must stop (rc=$rc: $out)"
out="$(req "$T/fake" "$T/pins.good")"; rc=$?
[ "$rc" = 0 ] && grep -q "matches the pin" <<<"$out" || bad "the pinned cosign must pass (rc=$rc: $out)"
printf '# no line for this platform\n' > "$T/pins.none"
out="$(req "$T/fake" "$T/pins.none")"; rc=$?
[ "$rc" != 0 ] && grep -q "no pin" <<<"$out" || bad "a platform with no pin must stop, not pass (rc=$rc: $out)"

# The checked-in pins: every platform cosign is built for has a 64-hex line.
for p in darwin-arm64 darwin-amd64 linux-amd64 linux-arm64; do
  line="$(awk -v n="cosign-$p" '$1 !~ /^#/ && $2==n {print $1}' "$root/tools/devops/cosign.sha256")"
  [[ "$line" =~ ^[0-9a-f]{64}$ ]] || bad "tools/devops/cosign.sha256 has no valid line for cosign-$p"
done

# install-cosign.sh: a fake gh "downloads" a file; a mismatch installs nothing, a match installs it executable.
cat > "$T/ghbin/gh" <<'GH'
#!/bin/sh
# gh release download TAG --repo R --pattern cosign-PLAT -D DIR
while [ $# -gt 0 ]; do case "$1" in --pattern) pat="$2"; shift 2 ;; -D) dir="$2"; shift 2 ;; *) shift ;; esac; done
printf 'the downloaded cosign\n' > "$dir/$pat"
GH
chmod +x "$T/ghbin/gh"
printf '# test pins\n%s  cosign-%s\n' "$(printf 'the downloaded cosign\n' | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi; } | awk '{print $1}')" "$plat" > "$T/pins.dl"
out="$(PATH="$T/ghbin:$SYS" COSIGN_PINS_FILE="$T/pins.bad" bash "$root/tools/devops/install-cosign.sh" "$T/inst1" 2>&1)"; rc=$?
[ "$rc" != 0 ] && grep -q "NOT installed" <<<"$out" && [ ! -e "$T/inst1/cosign" ] || bad "a download that does not match the pin must install nothing (rc=$rc: $out)"
out="$(PATH="$T/ghbin:$SYS" COSIGN_PINS_FILE="$T/pins.dl" bash "$root/tools/devops/install-cosign.sh" "$T/inst2" 2>&1)"; rc=$?
[ "$rc" = 0 ] && [ -x "$T/inst2/cosign" ] && [ "$(cat "$T/inst2/cosign")" = "the downloaded cosign" ] || bad "a download that matches the pin must be installed executable (rc=$rc: $out)"

# verify-release.sh drives its own cosign row (a fake gh makes the other rows fail or skip; only the cosign row is read).
vr() { # PATHDIRS [flag] -> the cosign rows of the table
  (cd "$root" && PATH="$T/ghbin:$1:$SYS" bash tools/verify-release.sh v1.4.108 --local "$T/assets" --no-prev "${@:2}" 2>&1 | grep -E '^  (PASS|FAIL|SKIP) +cosign')
}
# This gh answers only "release view" (a release with no assets, so --local is used) and fails every other call: no network is needed or used.
cat > "$T/ghbin/gh" <<'GH'
#!/bin/sh
if [ "$1 $2" = "release view" ]; then echo '{"tagName":"v1.4.108","isDraft":false,"isPrerelease":false,"body":"","assets":[],"publishedAt":"2026-01-01T00:00:00Z"}'; exit 0; fi
exit 1
GH
chmod +x "$T/ghbin/gh"
command -v jq >/dev/null 2>&1 && ln -sf "$(command -v jq)" "$T/ghbin/jq"
rm -f "$T"/assets/*
out="$(vr "$T/none")"; grep -q '^  SKIP' <<<"$out" || bad "without the flag and without cosign the row is a SKIP ($out)"
out="$(vr "$T/none" --require-cosign)"; grep -q '^  FAIL' <<<"$out" || bad "with --require-cosign and no cosign the row is a FAIL ($out)"
out="$(vr "$T/fake" --require-cosign)"; grep -q '^  FAIL.*no .sigstore.json bundle' <<<"$out" || bad "with --require-cosign and no bundle the row is a FAIL ($out)"
out="$(vr "$T/fake")"; [ -z "$out" ] || bad "without the flag and with no bundle there is no cosign row ($out)"
printf 'blob\n' > "$T/assets/x"; printf '{}\n' > "$T/assets/x.sigstore.json"
out="$(vr "$T/fake" --require-cosign)"; grep -q '^  PASS.*x.sigstore.json' <<<"$out" || bad "a bundle the cosign verifies is a PASS ($out)"
printf '#!/bin/sh\necho "no matching CertificateIdentity" >&2\nexit 1\n' > "$T/fake/cosign"
out="$(vr "$T/fake" --require-cosign)"; grep -q '^  FAIL.*x.sigstore.json' <<<"$out" || bad "a bundle the cosign refuses is a FAIL ($out)"

[ "$fail" = 0 ] && echo "PASS require_cosign: the pinned cosign is required, install checks the pin, verify-release --require-cosign never skips"
exit "$fail"
