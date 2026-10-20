#!/usr/bin/env bash
# tools/devops/download-section.sh VER: the "Download" section every GitHub release's notes start with (#5467), on stdout. GitHub hides the
# disk image behind "Show more assets", so the notes carry direct links to that tag's stable-name files (Coral.dmg, Coral-x64.msi,
# Coral-x64-portable.zip, coral-linux-amd64.tar.gz, coral-linux-arm64.tar.gz) and the Homebrew line. The links name the release home of
# tools/release-homes.txt, so they follow a repository move. RELEASE_NOTES.md is not changed: finish-release.sh puts this above the
# tag's notes when it sets the release body. A release before 1.4.130 has no stable names: this refuses it.
set -o pipefail
V=${1:?usage: download-section.sh VER}; V=${V#v}
. "$(dirname "${BASH_SOURCE[0]}")/../release-homes.sh"
release_has_stable_names "$V" || { echo "download-section: v$V has no stable-name files (1.4.130 and later do)" >&2; exit 1; }
home=$(release_home_for_tag "v$V") || exit 1
base="https://github.com/$home/releases/download/v$V"
tap="${home%%/*}/coral"
cat <<SECTION
### Download

[**macOS (.dmg)**]($base/Coral.dmg) &bull; [Windows installer (.msi)]($base/Coral-x64.msi) &bull; [Windows portable (.zip)]($base/Coral-x64-portable.zip) &bull; [Linux x86-64]($base/coral-linux-amd64.tar.gz) &bull; [Linux ARM64]($base/coral-linux-arm64.tar.gz)

Or with Homebrew: \`brew install --cask $tap/coral\`

---

SECTION
