#!/usr/bin/env bash
set -euo pipefail
trap 'echo "package verification failed at line $LINENO: $BASH_COMMAND" >&2' ERR

# Verify a packaged SQLCipher runtime with the bundled OpenSSL library. This
# executes the shipped Coral self-test instead of recompiling source tests.
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
PACKAGE_DIR="${1:?usage: verify_encrypted_bundle.sh PACKAGE_DIR}"
cd "$ROOT/coral-go"

require_macos_slices() {
  local artifact="$1"
  # Release packages must be true universal binaries. A local arm64-only
  # package may opt into the explicit single-architecture test mode without
  # weakening the default release gate.
  lipo "$artifact" -verify_arch arm64
  if [[ "${CORAL_SINGLE_ARCH_TEST:-0}" != "1" ]]; then
    lipo "$artifact" -verify_arch x86_64
  fi
}

if [[ "$(uname -s)" == "Darwin" ]]; then
  test -f "$PACKAGE_DIR/libcrypto.3.dylib"
  require_macos_slices "$PACKAGE_DIR/libcrypto.3.dylib"
  for binary in "$PACKAGE_DIR"/coral "$PACKAGE_DIR"/launch-coral "$PACKAGE_DIR"/coral-board "$PACKAGE_DIR"/coral-agent "$PACKAGE_DIR"/coral-hook-agentic-state "$PACKAGE_DIR"/coral-hook-message-check "$PACKAGE_DIR"/coral-hook-session-start "$PACKAGE_DIR"/coral-hook-task-sync "$PACKAGE_DIR"/coral-tray "$PACKAGE_DIR"/coral-app; do
    test -x "$binary"
    require_macos_slices "$binary"
    deps="$(otool -L "$binary")"
    if grep -q 'libcrypto.3.dylib' <<<"$deps"; then
      grep -q '@rpath/libcrypto.3.dylib' <<<"$deps"
    fi
  done
  otool -L "$PACKAGE_DIR/coral" | grep -q '@rpath/libcrypto.3.dylib'
  export DYLD_LIBRARY_PATH="$PACKAGE_DIR${DYLD_LIBRARY_PATH:+:$DYLD_LIBRARY_PATH}"
  : "${OPENSSL_PREFIX:?set OPENSSL_PREFIX to matching build headers/libs}"
  export CGO_CFLAGS="-I$OPENSSL_PREFIX/include"
  export CGO_LDFLAGS="-L$OPENSSL_PREFIX/lib"
elif [[ "$(uname -s)" == "Linux" ]]; then
  test -f "$PACKAGE_DIR/libcrypto.so.3"
  for binary in "$PACKAGE_DIR"/coral "$PACKAGE_DIR"/launch-coral "$PACKAGE_DIR"/coral-board "$PACKAGE_DIR"/coral-agent "$PACKAGE_DIR"/coral-hook-agentic-state "$PACKAGE_DIR"/coral-hook-message-check "$PACKAGE_DIR"/coral-hook-session-start "$PACKAGE_DIR"/coral-hook-task-sync; do
    test -x "$binary"
    deps="$(readelf -d "$binary")"
    if grep -q 'NEEDED.*libcrypto.so.3' <<<"$deps"; then
      grep -Eq 'RUNPATH.*\$ORIGIN|RPATH.*\$ORIGIN' <<<"$deps"
    fi
  done
  readelf -d "$PACKAGE_DIR/coral" | grep -Eq 'NEEDED.*libcrypto.so.3'
  export LD_LIBRARY_PATH="$PACKAGE_DIR${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
else
  echo "unsupported host for this verifier: $(uname -s)" >&2
  exit 2
fi

HOME="$(mktemp -d)" "$PACKAGE_DIR/coral" --encryption-self-test
