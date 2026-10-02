#!/usr/bin/env bash
set -euo pipefail

# Validate that the attestation describes the exact native library copied into
# a package. Policy is fail-closed: only an explicit patched/vendor-backport
# decision with a review reference is accepted. This avoids treating a CLI
# version string as proof about a different loaded library.
ATTESTATION="${1:?usage: verify_openssl_attestation.sh ATTESTATION_FILE LIBRARY [SLICE_ARM64] [SLICE_X86_64]}"
LIBRARY="${2:?usage: verify_openssl_attestation.sh ATTESTATION_FILE LIBRARY [SLICE_ARM64] [SLICE_X86_64]}"
SLICE_ARM64="${3:-}"
SLICE_X86_64="${4:-}"
POLICY_FILE="${CORAL_OPENSSL_POLICY:-$(cd "$(dirname "$0")" && pwd)/openssl_policy.tsv}"

field() { sed -n "s/^$1=//p" "$ATTESTATION" | head -1; }

for key in platform vendor package version library_sha256; do
  [[ -n "$(field "$key")" ]] || { echo "missing attestation field: $key" >&2; exit 1; }
done

version_ge_patch() {
  local actual="$1" branch="$2" minimum="$3" major minor patch
  [[ "$actual" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] || return 1
  major="${BASH_REMATCH[1]}"; minor="${BASH_REMATCH[2]}"; patch="${BASH_REMATCH[3]}"
  [[ "$branch" == "$major.$minor" ]] || return 1
  (( patch >= minimum ))
}

policy_accepts() {
  local requested="$1" pplatform pvendor ppackage pversion pmin pmode pref
  while IFS='|' read -r pplatform pvendor ppackage pversion pmin pmode pref; do
    [[ -z "$pplatform" || "$pplatform" == \#* ]] && continue
    [[ "$pplatform" == "$(field platform)" && "$pvendor" == "$(field vendor)" && "$ppackage" == "$(field package)" ]] || continue
    if [[ "$pmode" == vendor-backport && "$requested" == "$pversion" ]] ||
       [[ "$pmode" == patched ]] && version_ge_patch "$requested" "$pversion" "$pmin"; then
      [[ "$pref" == https://* ]] || { echo "policy reference must be HTTPS" >&2; exit 1; }
      return 0
    fi
  done < "$POLICY_FILE"
  return 1
}

policy_accepts "$(field version)" || { echo "OpenSSL identity/version is not in the reviewed policy" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then actual_hash="$(sha256sum "$LIBRARY" | awk '{print $1}')"; else actual_hash="$(shasum -a 256 "$LIBRARY" | awk '{print $1}')"; fi
[[ "$actual_hash" == "$(field library_sha256)" ]] || { echo "library hash does not match attestation" >&2; exit 1; }

if [[ -n "$SLICE_ARM64" || -n "$SLICE_X86_64" ]]; then
  [[ -n "$SLICE_ARM64" && -n "$SLICE_X86_64" ]] || { echo "both macOS slice inputs are required" >&2; exit 1; }
  [[ -n "$(field slice_arm64_version)" && -n "$(field slice_x86_64_version)" ]] || { echo "both macOS slice versions are required" >&2; exit 1; }
  policy_accepts "$(field slice_arm64_version)" || { echo "arm64 OpenSSL slice version is not in the reviewed policy" >&2; exit 1; }
  policy_accepts "$(field slice_x86_64_version)" || { echo "x86_64 OpenSSL slice version is not in the reviewed policy" >&2; exit 1; }
  for arch in arm64 x86_64; do
    if [[ "$arch" == arm64 ]]; then slice="$SLICE_ARM64"; else slice="$SLICE_X86_64"; fi
    [[ -f "$slice" ]] || { echo "missing $arch slice input" >&2; exit 1; }
    if command -v sha256sum >/dev/null 2>&1; then hash="$(sha256sum "$slice" | awk '{print $1}')"; else hash="$(shasum -a 256 "$slice" | awk '{print $1}')"; fi
    [[ "$hash" == "$(field slice_${arch}_sha256)" ]] || { echo "$arch slice hash does not match attestation" >&2; exit 1; }
  done
  [[ "$(field combined_sha256)" == "$actual_hash" ]] || { echo "combined library hash does not match attestation" >&2; exit 1; }
fi
exit 0
