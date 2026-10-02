#!/usr/bin/env bash
# Verify the default package has no SQLCipher/OpenSSL payload or host crypto ABI.
set -euo pipefail

package_dir="$(cd "${1:?usage: verify_standard_bundle.sh PACKAGE_DIR}" && pwd -P)"
commands=(coral launch-coral coral-board coral-agent coral-hook-agentic-state coral-hook-message-check coral-hook-session-start coral-hook-task-sync)

search_roots=("$package_dir")
resources_dir="$(dirname "$package_dir")/Resources"
if [ -d "$resources_dir" ]; then search_roots+=("$resources_dir"); fi
for forbidden in libcrypto.so.3 libcrypto.3.dylib openssl-attestation.env THIRD_PARTY_NOTICES_SQLCIPHER.md OPENSSL-LICENSE.txt SQLCIPHER-LICENSE.md; do
  if [ -n "$(find "${search_roots[@]}" -name "$forbidden" -print -quit)" ]; then
    echo "Standard package contains $forbidden" >&2
    exit 1
  fi
done

case "$(file -b "$package_dir/coral")" in
  *ELF*)
    for cmd in "${commands[@]}"; do
      binary="$package_dir/$cmd"
      test -x "$binary"
      headers="$(objdump -x "$binary")"
      if grep -Eq '^[[:space:]]*INTERP[[:space:]]+off' <<<"$headers"; then
        echo "Standard Linux $cmd has a dynamic loader dependency" >&2
        exit 1
      fi
      if grep -Eq '^[[:space:]]*DYNAMIC[[:space:]]+off|^[[:space:]]*NEEDED[[:space:]]+' <<<"$headers" || objdump -h "$binary" | grep -Eq '^[[:space:]]*[0-9]+[[:space:]]+\.dynamic[[:space:]]'; then
        echo "Standard Linux $cmd has a dynamic section or shared-library dependency" >&2
        exit 1
      fi
      if go version -m "$binary" | grep -Eq 'github.com/(0xCarbon|mattn)/go-sqlite3|build[[:space:]]+-tags=.*sqlcipher'; then
        echo "Standard Linux $cmd includes the SQLCipher build" >&2
        exit 1
      fi
    done
    ;;
  *Mach-O*)
    commands+=(coral-tray coral-app)
    for cmd in "${commands[@]}"; do
      binary="$package_dir/$cmd"
      test -x "$binary"
      for arch in arm64 x86_64; do
        lipo -verify_arch "$arch" "$binary"
        deps="$(otool -L -arch "$arch" "$binary" | tail -n +2)"
        if grep -Eiq 'libcrypto|libssl|sqlcipher|/opt/homebrew|/usr/local/opt' <<<"$deps"; then
          echo "Standard macOS $cmd ($arch) has a bundled/developer library dependency" >&2
          exit 1
        fi
        while read -r dependency _; do
          [ -n "$dependency" ] || continue
          case "$dependency" in
            /usr/lib/*|/System/Library/*) ;;
            *) echo "Standard macOS $cmd ($arch) loads external $dependency" >&2; exit 1 ;;
          esac
        done <<<"$deps"
        # The compiler and linker must not silently adopt the newer runner's
        # deployment version. The app Info.plist advertises macOS 13.0.
        minos="$(otool -l -arch "$arch" "$binary" | awk '
          $1 == "cmd" && $2 == "LC_BUILD_VERSION" { build = 1; next }
          build && $1 == "minos" { print $2; exit }
          $1 == "cmd" && $2 == "LC_VERSION_MIN_MACOSX" { legacy = 1; next }
          legacy && $1 == "version" { print $2; exit }
        ')"
        if [ -z "$minos" ] || ! awk -v v="$minos" 'BEGIN { split(v, p, "."); exit !((p[1] + 0) < 13 || ((p[1] + 0) == 13 && (p[2] + 0) == 0)) }'; then
          echo "Standard macOS $cmd ($arch) requires macOS ${minos:-unknown}, above advertised 13.0" >&2
          exit 1
        fi
      done
    done
    if [ -f "$package_dir/../Info.plist" ]; then
      advertised="$(plutil -extract LSMinimumSystemVersion raw -o - "$package_dir/../Info.plist")"
      if [ "$advertised" != 13.0 ]; then
        echo "Standard app advertises macOS $advertised, expected 13.0" >&2
        exit 1
      fi
    fi
    ;;
  *) echo "unsupported package format: $package_dir/coral" >&2; exit 2 ;;
esac

echo "Standard package dependency and deployment checks passed: $package_dir"
