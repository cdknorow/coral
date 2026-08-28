#!/bin/bash
# Put the Coral command-line tools on your PATH.
#
# Run this after installing Coral.app:
#   /Applications/Coral.app/Contents/MacOS/install-cli.sh
#
# Options:
#   --dir <path>   link into <path> instead of the default
#   --force        replace files that are not Coral symlinks (asks otherwise)
#
# Links into ~/.local/bin by default. The previous version used
# /usr/local/bin, which does not exist on an Apple Silicon Mac that has never
# had Intel Homebrew and is root-owned, so `mkdir -p` failed and `set -e`
# aborted the install with no explanation.

set -uo pipefail

LINK_DIR="${CORAL_LINK_DIR:-$HOME/.local/bin}"
FORCE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --dir) LINK_DIR="${2:?--dir needs a path}"; shift 2 ;;
        --force) FORCE=1; shift ;;
        -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "Unknown option: $1" >&2; exit 2 ;;
    esac
done

# Find the tools next to this script first, so an app installed anywhere works
# — Homebrew casks can install to a custom --appdir. Fall back to /Applications
# for anyone running a copy of this script from elsewhere.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_BIN=""
for candidate in "$SCRIPT_DIR" "/Applications/Coral.app/Contents/MacOS"; do
    if [ -x "$candidate/coral" ]; then APP_BIN="$candidate"; break; fi
done
if [ -z "$APP_BIN" ]; then
    echo "Error: could not find the Coral tools."
    echo "  Looked in: $SCRIPT_DIR"
    echo "             /Applications/Coral.app/Contents/MacOS"
    echo "Drag Coral.app to your Applications folder, then run:"
    echo "  /Applications/Coral.app/Contents/MacOS/install-cli.sh"
    exit 1
fi

TOOLS=(coral coral-board launch-coral coral-hook-agentic-state coral-hook-message-check coral-hook-task-sync)

if ! mkdir -p "$LINK_DIR" 2>/dev/null; then
    echo "Error: cannot create $LINK_DIR"
    echo "Pick a directory you own and re-run, for example:"
    echo "  $SCRIPT_DIR/install-cli.sh --dir \"\$HOME/bin\""
    exit 1
fi
if [ ! -w "$LINK_DIR" ]; then
    echo "Error: $LINK_DIR is not writable by $(whoami)."
    echo "Pick a directory you own and re-run, for example:"
    echo "  $SCRIPT_DIR/install-cli.sh --dir \"\$HOME/bin\""
    exit 1
fi

echo "Installing Coral CLI tools from $APP_BIN into $LINK_DIR"

linked=0; missing=(); skipped=()
for tool in "${TOOLS[@]}"; do
    src="$APP_BIN/$tool"
    dest="$LINK_DIR/$tool"
    if [ ! -f "$src" ]; then
        missing+=("$tool")
        continue
    fi
    # Never silently replace something we did not create. A different product
    # ships binaries with these exact names, and clobbering one without saying
    # so would swap a user's tools underneath them.
    if [ -e "$dest" ] || [ -L "$dest" ]; then
        current="$(readlink "$dest" 2>/dev/null || echo "")"
        if [ "$current" != "$src" ] && [ "$FORCE" -ne 1 ]; then
            skipped+=("$tool")
            continue
        fi
    fi
    if ln -sfn "$src" "$dest" 2>/dev/null; then
        echo "  linked  $tool"
        linked=$((linked+1))
    else
        skipped+=("$tool")
    fi
done

echo ""
if [ "${#missing[@]}" -gt 0 ]; then
    echo "Not in this build, so not linked: ${missing[*]}"
fi
if [ "${#skipped[@]}" -gt 0 ]; then
    echo "ALREADY TAKEN by something else, left alone: ${skipped[*]}"
    for tool in "${skipped[@]}"; do
        echo "  $LINK_DIR/$tool -> $(readlink "$LINK_DIR/$tool" 2>/dev/null || echo 'a real file, not a symlink')"
    done
    echo "Another program owns those names. Inspect them, then re-run with --force to replace."
fi
if [ "$linked" -eq 0 ]; then
    echo "Nothing was linked."
    exit 1
fi

# Only claim success if it is actually true: the directory has to be on PATH
# AND `coral` has to resolve to ours. The previous version printed "now
# available on your PATH" unconditionally.
on_path=0
case ":${PATH}:" in *":${LINK_DIR}:"*) on_path=1 ;; esac

if [ "$on_path" -ne 1 ]; then
    echo ""
    echo "$LINK_DIR is NOT on your PATH, so the tools are not usable yet."
    echo "Add this line to your ~/.zshrc (or ~/.bashrc), then open a new terminal:"
    echo ""
    echo "  export PATH=\"$LINK_DIR:\$PATH\""
    echo ""
    echo "Then check it worked:"
    echo "  command -v coral    # expect $LINK_DIR/coral"
    exit 0
fi

resolved="$(command -v coral 2>/dev/null || echo "")"
if [ "$resolved" != "$LINK_DIR/coral" ]; then
    echo ""
    echo "WARNING: 'coral' on your PATH is NOT the one just installed."
    echo "  you will run: $resolved"
    echo "  just installed: $LINK_DIR/coral"
    echo "Another program of the same name comes earlier on your PATH."
    exit 0
fi

echo "Done. $linked tool(s) linked and 'coral' resolves to $resolved"
echo ""
echo "Verify:"
echo "  command -v coral && coral --help | head -1"
