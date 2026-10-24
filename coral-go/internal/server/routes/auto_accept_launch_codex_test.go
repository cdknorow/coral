package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #7106 round 2 (Codex Test's nine ordinary corrections): option arity of flock, the folder find -execdir runs in, tar's cumulative -C and --remove-files, zip's -O and -b, zstd's output folders.
func TestShellRiskLauncherAndArchiveCorrections(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		// flock takes -w/--timeout/--wait and -E/--conflict-exit-code values before the lock file and the command
		`flock -w 5 /tmp/l rm -rf /etc`, `flock --timeout 5 /tmp/l rm -rf /etc`, `flock --timeout=5 /tmp/l rm -rf /etc`, `flock -n -w 5 /tmp/l rm -rf /etc`, `flock -E 9 /tmp/l rm -rf /etc`, `flock -w 5 /tmp/l -c 'rm -rf /etc'`, `flock --wait 5 -x /tmp/l sh -c 'rm -rf /etc'`,
		// find -execdir and -okdir run in the folder of each match: a relative path there is under the search root
		`find /etc -execdir cp {} backup \;`, `find /etc -execdir rm -rf x \;`, `find /etc -execdir sh -c 'rm -rf x' \;`, `find /etc -okdir mv x y \;`, `find /etc/ssh -execdir tee out \;`, `find $HOME -execdir rm -rf x \;`,
		// tar: -C is cumulative, an absolute -C starts over, --remove-files deletes what it archived
		`tar -xf a.tar -C sub -C /etc`, `tar -xf a.tar -C .. -C ..`, `tar -xf a.tar -C sub -C ../..`, `tar -cf out.tar --remove-files /etc/x`, `tar --remove-files -cf out.tar /etc/x`, `tar -cf out.tar --remove-files -C /etc x`, `tar -czf out.tgz --remove-files -C /etc/ssh .`, `tar -xf a.tar -C $X -C sub`,
		// zip -O writes another output file, -b a temporary folder
		`zip -O /etc/new.zip in.zip x`, `zip --output-file /etc/new.zip in.zip x`, `zip --output-file=/etc/new.zip in.zip x`, `zip -b /etc out.zip x`, `zip --temp-path /etc out.zip x`,
		// zstd names the output with -o, --output, --output-dir-flat and --output-dir-mirror
		`zstd --output-dir-flat=/etc x`, `zstd --output-dir-flat /etc x`, `zstd --output-dir-mirror=/etc x`, `zstd -o /etc/x.zst x`, `zstd --output=/etc/x.zst x`, `zstd --rm --output-dir-flat=out /etc/x`,
	} {
		assert.NotEmpty(t, shellRisk(c, dir), "must wait: %s", c)
	}
}

func TestShellRiskLauncherAndArchiveCorrectionsLeaveOrdinaryCommandsAlone(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		`flock -w 5 /tmp/l go test ./...`, `flock --timeout=5 /tmp/l -c 'go build ./...'`, `flock -n /tmp/l ls`, `flock -E 9 -w 5 build.lock make`,
		`find . -execdir cp {} backup \;`, `find sub -execdir rm -rf x \;`, `find . -execdir sh -c 'echo hi' \;`, `find . -name '*.o' -execdir rm {} \;`,
		`tar -xf a.tar -C sub -C inner`, `tar -xf a.tar -C sub -C ..`, `tar -cf out.tar --remove-files x`, `tar --remove-files -cf out.tar -C sub x`, `tar -cf out.tar -C /etc x`, `tar -xf /tmp/a.tar -C out`,
		`zip -O new.zip in.zip x`, `zip -b tmpdir out.zip x`, `zip -O new.zip /tmp/in.zip x`,
		`zstd --output-dir-flat=out x`, `zstd -o x.zst x`, `zstd /tmp/x -o out.zst`, `zstd --rm --output-dir-flat=out x`, `zstd --output-dir-flat out /tmp/x`, `zstd -o out.zst /tmp/x`, `flock -w 5 /tmp/l ls`,
	} {
		assert.Empty(t, shellRisk(c, dir), "must pass: %s", c)
	}
}
