#!/usr/bin/env bash
# The real upgrade from the previous release (#5339), on a NATIVE Linux runner (amd64 and arm64 each on their own): the previous release's tarball
# is unpacked into a folder of its own and the new release's tarball goes through coral-update's Linux apply steps with the real tools: the strict
# unpack beside the install, every program checked to be a 64-bit ELF of this machine, the new coral's --version, then the atomic exchange
# (renameat2 RENAME_EXCHANGE) and the rollback. Coral is not started.
#
# usage: tests/release/upgrade_linux.sh <previous coral-linux-<arch>-X.tar.gz> <new coral-linux-<arch>-Y.tar.gz> <new version, e.g. 1.4.131>
# The two tarballs are the releases' own (check them against SHA256SUMS.txt first, as the updater does).
set -euo pipefail

prev=${1:?previous tarball}
new=${2:?new tarball}
version=${3:?new version}

here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../../coral-go"
CORAL_APPLY_OLD_TARBALL="$(cd "$(dirname "$prev")" && pwd)/$(basename "$prev")" \
CORAL_APPLY_NEW_TARBALL="$(cd "$(dirname "$new")" && pwd)/$(basename "$new")" \
CORAL_APPLY_NEW_VERSION="$version" \
  go test ./internal/updater/apply -run 'TestRealLinuxUpgradeFromThePreviousRelease' -count=1 -v
