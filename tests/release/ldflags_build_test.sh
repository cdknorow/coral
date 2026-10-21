#!/usr/bin/env bash
# The linker flags of every build script really take effect (#5539). A -X flag with a wrong package path is silently ignored: the build succeeds and
# Version, PostHogKey and DevBase come out empty. This takes the "-X path.Var" flags out of each script's own text, gives them test values, builds
# ./cmd/coral with them and checks the binary: "coral --version" prints the test version, and "go tool nm" lists each variable as initialised data
# (D): a variable no flag reached stays in bss (B). The PostHog key itself is not searched for in the binary: the build's own flags are recorded in
# it, so the text is there either way. A control build with a wrong path must come out as a development build, which proves the check can fail.
# --version is answered before any data folder is opened, but it runs with its own HOME and CORAL_DATA_DIR anyway, and no hub URL.
set -euo pipefail
export CORAL_TELEMETRY_DISABLED=1
root=$(cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d /tmp/ldf.XXXX); case "$work" in /tmp/ldf.????) ;; *) echo "bad temp dir"; exit 1;; esac
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/home" "$work/data"
fail=0
VER=9.9.9-ldtest; KEY=phc_ldtest_key_0123456789; BASE=v9.9.8-ldtest

run_version() { env -i PATH="$PATH" HOME="$work/home" CORAL_DATA_DIR="$work/data" CORAL_TELEMETRY_DISABLED=1 "$1" --version; }
build() { (cd "$root/coral-go" && CGO_ENABLED=0 go build -tags fts5 -ldflags "$1" -o "$2" ./cmd/coral); }

# a flag set from one file: its own "-X path.Var" words, each with a test value
flags_of() {
  local f="$1" out="" w var
  while read -r w; do
    var=${w##*.}
    case "$var" in
      Version) out="$out -X $w=$VER" ;;
      PostHogKey) out="$out -X $w=$KEY" ;;
      DevBase) out="$out -X $w=$BASE" ;;
      # this function runs in a command substitution: a variable set here is lost with its subshell, so the failure is a file the end of the script reads
      *) echo "FAIL: $f: -X $w sets an unknown variable" >&2; : > "$work/unknown-variable" ;;
    esac
  done < <(sed 's/[[:space:]]#.*$//' "$f" | grep -o -- '-X [^ "=]*' | sed 's/^-X //' | sort -u) # a trailing "# ... -X flags ..." comment is not a flag
  echo "$out"
}

# initialised "go tool nm" type of config.<var> in a binary built without -s: D when a flag set it, B when none did
nm_type() { go tool nm "$1" | awk -v v="/internal/config.$2" 'index($3, v) && substr($3, length($3) - length(v) + 1) == v {print $2}'; }

check() {
  local f="$1" want_key="$2" flags bin w var t
  flags=$(flags_of "$root/$f")
  [ -n "$flags" ] || { echo "FAIL: $f: no -X flag found"; fail=1; return; }
  bin="$work/coral-$(echo "$f" | tr '/.' '__')"
  build "$flags" "$bin" || { echo "FAIL: $f: the build failed"; fail=1; return; }
  local v; v=$(run_version "$bin" 2>&1 | head -1)
  if echo "$flags" | grep -q 'Version='; then
    if [ "$v" = "coral $VER" ]; then echo "ok: $f: --version is $VER"; else echo "FAIL: $f: --version printed [$v], want [coral $VER]: the Version flag did not take effect"; fail=1; fi
  fi
  for w in $(echo "$flags" | grep -o -- '-X [^ =]*' | sed 's/^-X //'); do
    var=${w##*.}; t=$(nm_type "$bin" "$var")
    if [ "$t" = D ]; then echo "ok: $f: $var is set in the binary"; else echo "FAIL: $f: $var is not set in the binary (nm type [$t]): its -X flag did not take effect"; fail=1; fi
  done
  if [ "$want_key" = key ] && ! echo "$flags" | grep -q PostHogKey; then echo "FAIL: $f: no PostHogKey flag"; fail=1; fi
}

check .github/workflows/release.yml key
check .github/workflows/linux-runner-dryrun.yml nokey
check installers/build-macos.sh key
check installers/build-linux.sh nokey
check tools/build_windows.ps1 nokey
if grep -q 'config.DevBase' "$root/coral-go/Makefile"; then
  flags=$(flags_of "$root/coral-go/Makefile"); build "$flags" "$work/coral-make"
  t=$(nm_type "$work/coral-make" DevBase)
  if [ "$t" = D ]; then echo "ok: Makefile: DevBase is set in the binary"; else echo "FAIL: Makefile: DevBase is not set in the binary (nm type [$t])"; fail=1; fi
fi

# the control: a wrong path builds fine and is ignored, and this check sees it
build "-X github.com/cdknorow/coral/internal/config.Version=$VER" "$work/coral-wrong"
v=$(run_version "$work/coral-wrong" 2>&1 | head -1)
case "$v" in
  "coral (development build)"*) echo "ok: control: a wrong -X path is ignored (--version: $v), and the checks above would have failed on it" ;;
  *) echo "FAIL: control: a wrong -X path printed [$v]"; fail=1 ;;
esac
[ ! -e "$work/unknown-variable" ] || fail=1
exit "$fail"
