package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #7106: archive and sync commands write where their options say; outside the working folder, or a place that cannot be read, asks.
func TestShellRiskReadsWhereArchivesAndSyncsWrite(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		// tar: extract into -C or the folder it runs in; create or append to -f; -P
		`tar -xf a.tar -C /etc`, `tar -xzf a.tgz -C /etc`, `tar xzf a.tgz -C /etc`, `tar -x -C /etc -f a.tar`, `tar -xf a.tar --directory=/etc`, `tar -xf a.tar --directory /etc`, `tar -xf a.tar -C/etc`,
		`tar -xPf a.tar`, `tar -xf a.tar --absolute-names`, `cd /etc && tar -xf /tmp/a.tar`, `tar -cf /etc/a.tar src`, `tar czf /etc/a.tgz src`, `tar --create --file=/etc/a.tar src`, `tar -rf /etc/a.tar x`, `bsdtar -xf a.tar -C /etc`,
		`tar -xf a.tar -C $HOME`, `tar -xf a.tar -C ~/x`,
		// unzip, zip, 7z
		`unzip a.zip -d /etc`, `unzip -d /etc a.zip`, `unzip -o a.zip -d/etc`, `cd /etc && unzip /tmp/a.zip`, `zip /etc/a.zip x`, `zip -r /etc/a.zip dir`, `7z x a.7z -o/etc`, `7z x a.7z -spf`, `7z a /etc/a.7z dir`, `7za x a.7z -o/etc`, `cd /etc && 7z x /tmp/a.7z`,
		// compressors replace the file they are given
		`gzip /etc/x`, `gunzip /etc/x.gz`, `bzip2 -9 /etc/x`, `xz /etc/x`, `unxz /etc/x.xz`, `zstd --rm /etc/x`,
		// rsync
		`rsync -a src/ /etc/`, `rsync -av --delete src/ /etc/dst`, `rsync -a src/ $HOME/x`, `rsync -a --backup-dir=/etc/b src/ dst/`, `rsync -a --remove-source-files /etc/x dst/`, `rsync -a --log-file /etc/l src/ dst/`,
	} {
		assert.NotEmpty(t, shellRisk(c, dir), "must wait: %s", c)
	}
}

func TestShellRiskArchiveRulesLeaveOrdinaryCommandsAlone(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []string{
		`tar -xf a.tar`, `tar -xzf a.tgz -C out`, `tar xzf a.tgz -C out`, `tar -tf a.tar`, `tar -tzf /tmp/a.tgz`, `tar -cf out.tar src`, `tar czf out.tgz src`, `tar -xf /tmp/a.tar -C sub`, `tar -cf - src | gzip`, `tar -xf /tmp/a.tar`,
		`unzip a.zip`, `unzip a.zip -d out`, `unzip -l /tmp/a.zip`, `unzip -t a.zip`, `unzip -p a.zip x`, `zip out.zip x`, `zip -r out.zip dir`, `7z x a.7z`, `7z x a.7z -oout`, `7z a out.7z dir`, `7z l /tmp/a.7z`,
		`gzip out.log`, `gzip -c out.log`, `gzip -l /tmp/a.gz`, `gunzip -c /tmp/a.gz`, `xz -t /tmp/a.xz`, `xz out.log`,
		`rsync -a src/ dst/`, `rsync -a src/ host:/srv/dst`, `rsync -a host:/srv/src dst/`, `rsync -a --delete src/ dst/`, `rsync -a -e ssh src/ user@host:dst`, `rsync -a src/ rsync://host/module`,
	} {
		assert.Empty(t, shellRisk(c, dir), "must pass: %s", c)
	}
}
