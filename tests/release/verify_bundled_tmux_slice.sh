#!/usr/bin/env bash
# Verify one native tmux payload before it is assembled into Coral.app.
set -euo pipefail
payload="$(cd "${1:?payload directory required}" && pwd -P)"
arch="${2:?arm64 or x86_64 required}"
case "$arch" in arm64|x86_64) ;; *) exit 2;; esac
bin="$payload/bin/tmux"
test -x "$bin"
if ! lipo "$bin" -verify_arch "$arch" >/dev/null; then
    echo "tmux payload is missing $arch: $bin" >&2
    exit 1
fi
test "$(lipo -archs "$bin")" = "$arch" || { echo "tmux payload is not a thin $arch slice" >&2; exit 1; }
for spec in 'x xterm-256color 78' 's screen-256color 73' 't tmux-256color 74'; do
    read -r letter name hex <<<"$spec"
    test -s "$payload/terminfo/$letter/$name" || test -s "$payload/terminfo/$hex/$name" || {
        echo "missing bundled terminfo $name" >&2; exit 1;
    }
done
for name in tmux-COPYING libevent-LICENSE ncurses-COPYING SOURCES.tsv; do
    test -s "$payload/licenses/$name" || { echo "missing bundled tmux notice $name" >&2; exit 1; }
done
if [ -n "$(find "$payload" -name '*.dylib' -print -quit)" ]; then
    echo 'bundled tmux payload includes a dylib' >&2; exit 1
fi
if strings "$bin" | grep -E 'coral-tmux-build\.|/opt/homebrew|/usr/local/opt' >/dev/null; then
    echo 'bundled tmux embeds a temporary build or Homebrew path' >&2; exit 1
fi
deps="$(otool -L "$bin" | tail -n +2)"
if grep -Eiq 'libcrypto|libssl|sqlcipher|libncurses|libevent|/opt/homebrew|/usr/local/opt' <<<"$deps"; then
    echo 'bundled tmux has a forbidden dynamic dependency' >&2; exit 1
fi
while read -r dependency _; do
    [ -n "$dependency" ] || continue
    case "$dependency" in
        /usr/lib/*|/System/Library/*) ;;
        *) echo "bundled tmux loads external $dependency" >&2; exit 1;;
    esac
done <<<"$deps"
minos="$(otool -l "$bin" | awk '
    $1 == "cmd" && $2 == "LC_BUILD_VERSION" { build = 1; next }
    build && $1 == "minos" { print $2; exit }
    $1 == "cmd" && $2 == "LC_VERSION_MIN_MACOSX" { legacy = 1; next }
    legacy && $1 == "version" { print $2; exit }
')"
test "$minos" = 13.0 || { echo "bundled tmux has minos ${minos:-unknown}, expected 13.0" >&2; exit 1; }
echo "Bundled tmux $arch payload passed: $payload"
