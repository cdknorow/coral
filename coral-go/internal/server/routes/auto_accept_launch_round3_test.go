package routes

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #7106 round 3 (Security Reviewer's three bypasses and the launchers it listed).
func TestShellRiskRound3Bypasses(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		// A: -- before the command
		`eval -- rm -rf /etc`, `eval -- "rm -rf /etc"`, `eval -- -- rm -rf /etc`, `timeout 5 -- rm -rf /etc`, `nice -- rm -rf /etc`, `watch -- rm -rf /etc`, `unshare -- rm -rf /etc`,
		// B: any expansion inside the command word
		`rm${IFS}-rf${IFS}/etc`, `rm$IFS-rf$IFS/etc`, `r${x}m -rf /etc`, `${IFS}rm -rf /etc`, `"$@" -rf /etc`, `rm$(echo ' -rf /etc')`, "rm`echo ' -rf /etc'`", `$X/rm -rf /etc/$Y`, `x${IFS}/etc`, `$HOME/$X -rf /etc`, `${A}/${B} -rf /etc`,
		// C: other writers
		`tar xf x.tar --one-top-level=/etc`, `tar xf x.tar -C /tmp --one-top-level=../etc`, `cpio -idv -D /etc < x`, `cpio -id --directory=/etc < x`, `cpio -iD /etc < x`, `cpio -pdm /etc < list`, `cpio -o -O /etc/out.cpio < list`, `cd /etc && cpio -id < /tmp/x`,
		// D: launchers
		`unshare -r rm -rf /etc`, `unshare --map-root-user rm -rf /etc`, `unshare -S 0 -G 0 rm -rf /etc`, `chroot / rm -rf /etc`, `chroot --userspec=0:0 / rm -rf /etc`, `chroot --userspec 0:0 / rm -rf /etc`, `chroot --groups 0 / rm -rf /etc`, `strace rm -rf /etc`, `strace -f -o /tmp/t rm -rf /etc`, `strace -e trace=file rm -rf /etc`, `ltrace rm -rf /etc`, `valgrind rm -rf /etc`, `valgrind --leak-check=full rm -rf /etc`,
		`script -qc 'rm -rf /etc' /dev/null`, `script -c 'rm -rf /etc' /dev/null`, `script --command 'rm -rf /etc' /dev/null`, `nsenter -t 1 -m rm -rf /etc`, `systemd-run --wait rm -rf /etc`, `systemd-run -p Nice=5 rm -rf /etc`, `runuser -u nobody rm -rf /etc`, `runuser -l nobody -c 'rm -rf /etc'`, `setpriv --reuid 1 rm -rf /etc`,
		// the exotic launchers always ask, whatever follows (design call, #7106 round 4), Codex Test's five cases among them
		`unshare -r go test ./...`, `chroot . ls`, `strace -f ls`, `strace -o out.txt ls`, `ltrace ls`, `valgrind --leak-check=full ./a.out`, `script -qc 'ls' /dev/null`, `nsenter --help`, `systemd-run --help`, `runuser -u nobody ls`, `pkexec ls`, `firejail ls`,
		`strace --output out.txt rm -rf x`, `unshare --map-user x rm -rf x`, `unshare --wd /etc rm -rf x`, `systemd-run --working-directory=/etc rm -rf x`, `chroot / rm -rf x`,
	} {
		assert.NotEmpty(t, shellRisk(c, dir), "must wait: %s", c)
	}
}

func TestShellRiskRound3LeavesOrdinaryCommandsAlone(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		`eval -- echo hi`, `eval echo hi`, `timeout 5 -- go test ./...`,
		`$HOME/bin/tool x`, `${HOME}/bin/tool x`, `"$CLAUDE_PROJECT_DIR"/hooks/x`, `$(git rev-parse --show-toplevel)/scripts/x.sh`, `echo $HOME`, `echo "rm${IFS}-rf"`, `ls "$@"`, `git commit -m "fix $X"`,
		`tar xf x.tar --one-top-level=out`, `tar xf x.tar --one-top-level`, `cpio -id < x`, `cpio -id -D out < x`, `cpio -iD out < x`, `cpio -pdm out < list`, `cpio -o -O out.cpio < list`, `cpio -o > out.cpio < list`,
	} {
		assert.Empty(t, shellRisk(c, dir), "must pass: %s", c)
	}
}

// Commands inside commands are read to a fixed depth and then asked about, so the work does not grow past it however deeply a command is quoted (find -exec sh -c nested, #7106).
func TestShellRiskNestedLaunchersAreBounded(t *testing.T) {
	dir := t.TempDir()
	nest := func(wrap func(string) string, n int) string {
		c := "rm -rf /etc"
		for i := 0; i < n; i++ {
			c = wrap(c)
		}
		return c
	}
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	wraps := map[string]func(string) string{
		"find -exec sh -c": func(c string) string { return `find . -exec sh -c "` + esc.Replace(c) + `" \;` },
		"eval":             func(c string) string { return `eval "` + esc.Replace(c) + `"` },
		"xargs sh -c":      func(c string) string { return `xargs sh -c "` + esc.Replace(c) + `"` },
		"watch":            func(c string) string { return `watch "` + esc.Replace(c) + `"` },
	}
	for name, wrap := range wraps {
		// Past the depth: answered, in a time that does not depend on the clock of the machine beyond a loose bound.
		c := nest(wrap, 15) // each level doubles the text: 15 levels is about 100 KB
		start := time.Now()
		why := shellRisk(c, dir)
		assert.NotEmpty(t, why, name+": 15 deep asks")
		if strings.HasPrefix(name, "find") || strings.HasPrefix(name, "xargs") { // two levels each: past the depth the reading stops and says so
			assert.Contains(t, why, "nested too deeply", name)
		}
		assert.Less(t, time.Since(start), 2*time.Second*raceSlowdown(), name+": 15 deep is answered at once")
		// Shallow nesting around a risky command is still read through to it.
		assert.Contains(t, shellRisk(nest(wrap, 6), dir), "outside the working directory", name+": 6 deep reaches the rm")
		assert.Empty(t, shellRisk(strings.Replace(nest(wrap, 6), "rm -rf /etc", "echo x", 1), dir), name+": 6 deep around an echo is clear")
	}
}
