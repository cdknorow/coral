# shellcheck shell=bash disable=SC2034  # sourced by the other scripts, which use MAIN_TREE and IDENT
# tools/devops/lib.sh: shared helpers for the landing and release scripts (sourced, not run).
# MAIN_TREE is the main checkout (the shared tree), found from any worktree; IDENT is the landing identity.
set -o pipefail
MAIN_TREE=$(cd "$(git rev-parse --git-common-dir)/.." && pwd)
IDENT=(-c user.name="${CORAL_LAND_NAME:-Coral DevOps stack}" -c user.email="${CORAL_LAND_EMAIL:-devops-stack@example.test}")
die() { echo "$*" >&2; exit 1; }
# The release home (where releases are built, signed and downloaded from) and its tap: tools/release-homes.txt (#5299).
. "$(dirname "${BASH_SOURCE[0]}")/../release-homes.sh"

# ---- cosign (#5112): the release checks need the pinned cosign, never "whatever is installed" or nothing ----
# sha256_of FILE: the file's sha256.
sha256_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }
# cosign_platform: os-arch as cosign names its release files (darwin or linux, amd64 or arm64), or fail.
cosign_platform() {
  local os arch
  case "$(uname -s)" in Darwin) os=darwin ;; Linux) os=linux ;; *) return 1 ;; esac
  case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) return 1 ;; esac
  echo "$os-$arch"
}
# cosign_pin PLATFORM: the sha256 tools/devops/cosign.sha256 (or $COSIGN_PINS_FILE, for tests) pins for cosign-PLATFORM.
cosign_pin() {
  local f="${COSIGN_PINS_FILE:-$(dirname "${BASH_SOURCE[0]}")/cosign.sha256}" p
  p="$(awk -v n="cosign-$1" '$1 !~ /^#/ && $2==n {print $1}' "$f")"
  [ -n "$p" ] && echo "$p"
}
# require_cosign: die unless a cosign is on PATH AND it is the pinned release binary for this machine. A cosign that is not (a distro build,
# an unknown file) is refused, with the install line; it is the tool that vouches for every release, so it is not taken on trust.
require_cosign() {
  local here p plat pin got
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  p="$(command -v cosign 2>/dev/null)" || die "cosign is required to verify a release and is not on PATH: run tools/devops/install-cosign.sh, then put its folder on PATH"
  plat="$(cosign_platform)" || die "cosign: no pinned build for this machine ($(uname -s) $(uname -m))"
  pin="$(cosign_pin "$plat")" || die "cosign: no pin for cosign-$plat in tools/devops/cosign.sha256"
  got="$(sha256_of "$p")"
  [ "$got" = "$pin" ] || die "the cosign on PATH ($p, sha256 $got) is not the pinned cosign-$plat ($pin): run tools/devops/install-cosign.sh and put its folder first on PATH"
  echo "cosign: $p matches the pin ($plat)"
}
# scan_patch FILE: refuse a patch that carries personal data or binaries (placeholder paths like /Users/someone are fine).
scan_patch() {
  local hits
  hits=$(grep -nE "${CORAL_SCAN_PATTERN:-/Users/[^/ ]+/|^Binary files .* differ$|^GIT binary patch}" "$1" | grep -vE '/Users/(someone|alice|bob|me|user)/' || true)
  [ -z "$hits" ] || { echo "$hits" | head -5; return 1; }
}
