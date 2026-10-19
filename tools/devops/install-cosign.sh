#!/usr/bin/env bash
# tools/devops/install-cosign.sh [DIR]: install the pinned cosign (#5112) that after-release.sh and verify-release.sh need.
#
# Downloads cosign COSIGN_VERSION's release binary for this machine, checks its sha256 against tools/devops/cosign.sha256 (the pin in this
# repo: the second source that keeps the install from being circular, as the checksum file on the release page and the binary's own signature
# both come from the same place), and only then puts it at DIR/cosign (default $COSIGN_DIR, else $HOME/.local/bin). Put DIR on PATH.
# Needs gh. A mismatch installs nothing.
set -euo pipefail
COSIGN_VERSION=v2.4.1
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"
dir="${1:-${COSIGN_DIR:-$HOME/.local/bin}}"
plat="$(cosign_platform)" || die "cosign: no pinned build for this machine ($(uname -s) $(uname -m)); add it to tools/devops/cosign.sha256 in a reviewed commit"
pin="$(cosign_pin "$plat")" || die "cosign: tools/devops/cosign.sha256 has no line for cosign-$plat"
command -v gh >/dev/null 2>&1 || die "install-cosign: gh is required"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/install-cosign.XXXXXX")" || exit 1
trap 'rm -rf -- "$tmp"' EXIT
gh release download "$COSIGN_VERSION" --repo sigstore/cosign --pattern "cosign-$plat" -D "$tmp" >/dev/null || die "install-cosign: could not download cosign-$plat $COSIGN_VERSION"
got="$(sha256_of "$tmp/cosign-$plat")"
[ "$got" = "$pin" ] || die "install-cosign: cosign-$plat is $got, the pin says $pin: NOT installed"
mkdir -p "$dir"
install -m 0755 "$tmp/cosign-$plat" "$dir/cosign"
echo "cosign $COSIGN_VERSION ($plat, sha256 $got) installed at $dir/cosign"
case ":$PATH:" in *":$dir:"*) ;; *) echo "put $dir on PATH (export PATH=\"$dir:\$PATH\")" ;; esac
