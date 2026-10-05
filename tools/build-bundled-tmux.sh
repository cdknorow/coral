#!/usr/bin/env bash
# Build one native macOS slice of the tmux payload. Usage: script ARCH OUTPUT_DIR
# CORAL_TMUX_SOURCE_DIR may contain cached archives; every archive is checked.
set -euo pipefail

arch="${1:?arm64 or x86_64 required}"
out="${2:?output directory required}"
case "$arch" in arm64|x86_64) ;; *) echo "Unsupported arch: $arch" >&2; exit 2;; esac
[ "$(uname -s)" = Darwin ] || { echo 'macOS host required' >&2; exit 2; }
[ "$(uname -m)" = "$arch" ] || { echo "Native $arch runner required" >&2; exit 2; }
root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/coral-tmux-build.XXXXXX")"
mkdir -p "$out"
out="$(cd "$out" && pwd -P)"
cleanup() {
    result=$?
    if [ "$result" -ne 0 ]; then
        mkdir -p "$out/build-logs"
        find "$work" -name config.log -o -name '*-configure.log' -o -name '*-make.log' | while read -r log; do
            cp "$log" "$out/build-logs/$(basename "$(dirname "$log")")-$(basename "$log")"
        done
        echo "Build failed; logs at $out/build-logs" >&2
    fi
    rm -rf "$work"
    return "$result"
}
trap cleanup EXIT
sources="${CORAL_TMUX_SOURCE_DIR:-$work/downloads}"
mkdir -p "$sources" "$work/src" "$work/prefix" "$out/bin" "$out/terminfo" "$out/licenses"
sources="$(cd "$sources" && pwd -P)"

while IFS=$'\t' read -r component version url sha; do
    [[ "$component" == \#* || -z "$component" ]] && continue
    archive="$sources/${url##*/}"
    if [ ! -f "$archive" ]; then
        curl --fail --location --silent --show-error --retry 2 --connect-timeout 10 --max-time 120 "$url" -o "$archive"
    fi
    actual="$(shasum -a 256 "$archive" | cut -d ' ' -f 1)"
    [ "$actual" = "$sha" ] || { echo "Checksum mismatch: $archive" >&2; exit 1; }
    tar -xzf "$archive" -C "$work/src"
done < "$root/tools/bundled-tmux-sources.tsv"

prefix="$work/prefix"
export MACOSX_DEPLOYMENT_TARGET=13.0
# /usr/bin/clang is Apple's shim and selects the matching SDK as well as the
# compiler. Invoking the Command Line Tools clang path directly can omit it.
export CC=/usr/bin/clang
export CFLAGS='-O2 -mmacosx-version-min=13.0'
export CPPFLAGS="-I$prefix/include"
export LDFLAGS="-L$prefix/lib -mmacosx-version-min=13.0"
export PKG_CONFIG_LIBDIR="$prefix/lib/pkgconfig"
mkdir -p "$PKG_CONFIG_LIBDIR"

(
    cd "$work/src/libevent-2.1.13-stable"
    ./configure --prefix="$prefix" --disable-shared --enable-static --disable-openssl --disable-samples --disable-libevent-regress > "$work/libevent-configure.log"
    make -j2 > "$work/libevent-make.log"
    make install > "$work/libevent-install.log"
)
(
    cd "$work/src/ncurses-6.6-20260926"
    ./configure --prefix="$prefix" --datadir=/usr/share --with-normal --without-shared --without-cxx --without-ada --without-tests --without-manpages --disable-db-install --with-default-terminfo-dir=/usr/share/terminfo --with-terminfo-dirs=/usr/share/terminfo > "$work/ncurses-configure.log"
    make -j2 > "$work/ncurses-make.log"
    make install.libs install.includes > "$work/ncurses-install.log"
)
(
    cd "$work/src/tmux-3.7c"
    ./configure --prefix="$prefix" --disable-utf8proc --disable-jemalloc --disable-utempter --disable-systemd --disable-cgroups > "$work/tmux-configure.log"
    # configure also adds a generic -lncurses on Darwin. Override that
    # link-time variable so it cannot resolve the host's curses dylib.
    test -f "$prefix/lib/libncursesw.a"
    test -f "$prefix/lib/libevent_core.a"
    make -j2 LIBS="$prefix/lib/libncursesw.a $prefix/lib/libevent_core.a -lm -lresolv" > "$work/tmux-make.log"
    cp tmux "$out/bin/tmux"
)
chmod 755 "$out/bin/tmux"

# Apple's old tic cannot compile the current ncurses terminfo source. Use the
# freshly built native tic; only its data output is packaged.
"$work/src/ncurses-6.6-20260926/progs/tic" -x -e 'xterm,xterm-256color,screen,screen-256color,tmux,tmux-256color' -o "$out/terminfo" "$work/src/ncurses-6.6-20260926/misc/terminfo.src"
cp "$work/src/tmux-3.7c/COPYING" "$out/licenses/tmux-COPYING"
cp "$work/src/libevent-2.1.13-stable/LICENSE" "$out/licenses/libevent-LICENSE"
cp "$work/src/ncurses-6.6-20260926/COPYING" "$out/licenses/ncurses-COPYING"
cp "$root/tools/bundled-tmux-sources.tsv" "$out/licenses/SOURCES.tsv"

echo "Built native $arch tmux payload at $out"
