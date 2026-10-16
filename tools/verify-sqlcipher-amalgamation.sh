#!/usr/bin/env bash
# verify-sqlcipher-amalgamation.sh: prove the SQLCipher amalgamation inside the pinned
# github.com/0xCarbon/go-sqlite3 module (sqlite3-binding.c and .h, the C code the encrypted build
# links) is exactly what the official SQLCipher release builds to (#3679, from the #3677 review).
#
# It reads the module version from coral-go/go.mod, downloads that module (the bytes go.sum pins),
# takes the SQLCipher version from the bundled file and the release commit from the module's own
# upgrade/sqlcipher.sh pin table, clones the OFFICIAL github.com/sqlcipher/sqlcipher tag (the fork's
# script clones a mirror nobody else can reach), checks the tag is that exact commit, runs the same
# configure and make steps and the same USE_LIBSQLITE3 wrap as upgrade/sqlcipher.sh, and compares
# sha256. Exit 0 when both files match byte for byte, 1 when they differ, 2 on a setup error.
#
# Needs: go, git, a C compiler, tclsh, and the OpenSSL headers (configure checks for libcrypto).
# Usage (from anywhere in the repository): tools/verify-sqlcipher-amalgamation.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
module=github.com/0xCarbon/go-sqlite3
official=https://github.com/sqlcipher/sqlcipher

version=$(awk -v m="$module" '$1 == m {print $2; exit}' "$root/coral-go/go.mod")
[ -n "$version" ] || { echo "error: $module is not in coral-go/go.mod" >&2; exit 2; }
dir=$(cd "$root/coral-go" && go mod download -json "$module@$version" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["Dir"])')
[ -f "$dir/sqlite3-binding.c" ] && [ -f "$dir/upgrade/sqlcipher.sh" ] || { echo "error: $module@$version has no sqlite3-binding.c or upgrade/sqlcipher.sh" >&2; exit 2; }

cipher=$(grep -m1 '#define CIPHER_VERSION_NUMBER' "$dir/sqlite3-binding.c" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
[ -n "$cipher" ] || { echo "error: no CIPHER_VERSION_NUMBER in the bundled amalgamation" >&2; exit 2; }
pin=$(sed -nE "s/^${cipher//./\\.}\) PIN=([0-9a-f]{40}) ;;\$/\\1/p" "$dir/upgrade/sqlcipher.sh")
[ -n "$pin" ] || { echo "error: upgrade/sqlcipher.sh has no pinned commit for SQLCipher $cipher" >&2; exit 2; }
echo "module:    $module $version"
echo "SQLCipher: v$cipher, pinned release commit $pin"

work=$(mktemp -d "${TMPDIR:-/tmp}/sqlcipher-verify.XXXXXX")
trap 'rm -rf -- "$work"' EXIT

git -c advice.detachedHead=false clone --quiet --depth 1 --branch "v$cipher" "$official" "$work/sqlcipher"
head=$(git -C "$work/sqlcipher" rev-parse HEAD)
[ "$head" = "$pin" ] || { echo "error: official tag v$cipher is $head, the module pins $pin" >&2; exit 1; }
echo "official tag v$cipher is the pinned commit"

# The same steps as the module's upgrade/sqlcipher.sh.
( cd "$work/sqlcipher"
  ./configure --with-tempstore=yes \
    CFLAGS="-DSQLITE_HAS_CODEC -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown" \
    LDFLAGS="-lcrypto" > "$work/configure.log" 2>&1 || { tail -20 "$work/configure.log" >&2; exit 2; }
  make sqlite3.c > "$work/make.log" 2>&1 || { tail -20 "$work/make.log" >&2; exit 2; } )

# The module script's wrap(): the USE_LIBSQLITE3 guard and the retargeted self-include.
wrap() {
  {
    printf '#ifndef USE_LIBSQLITE3\n'
    awk '{
      if ($0 == "#include \"sqlite3.h\"") {
        print "#include \"sqlite3-binding.h\""
        print "#ifdef __clang__"
        print "#define assert(condition) ((void)0)"
        print "#endif"
      } else {
        print
      }
    }' "$1"
    printf '#else // USE_LIBSQLITE3\n // If users really want to link against the system sqlite3 we\n// need to make this file a noop.\n #endif'
  } > "$2"
}
wrap "$work/sqlcipher/sqlite3.c" "$work/sqlite3-binding.c"
wrap "$work/sqlcipher/sqlite3.h" "$work/sqlite3-binding.h"

sum() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'; }
fail=0
for f in sqlite3-binding.c sqlite3-binding.h; do
  want=$(sum "$dir/$f"); got=$(sum "$work/$f")
  if [ "$want" = "$got" ]; then echo "SAME $f $want"
  else echo "DIFF $f: the module has $want, the official release builds $got"; fail=1; fi
done
[ "$fail" = 0 ] && echo "PASS: $module $version bundles exactly the official SQLCipher v$cipher amalgamation"
exit "$fail"
