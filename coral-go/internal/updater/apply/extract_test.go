package apply

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tarEntry struct {
	name     string
	typ      byte
	body     string
	mode     int64
	linkname string
}

func makeTarGz(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: e.mode, Linkname: e.linkname}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(h))
		if typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	p := filepath.Join(t.TempDir(), "pkg.tar.gz")
	require.NoError(t, os.WriteFile(p, buf.Bytes(), 0o600))
	return p
}

func allowCoral(name string) bool { return name == "coral" || name == "NOTICE" }

func TestExtractWritesOnlyPlainFilesWithSafeModes(t *testing.T) {
	pkg := makeTarGz(t,
		tarEntry{name: "./", typ: tar.TypeDir, mode: 0o755},
		tarEntry{name: "./coral", body: "prog", mode: 0o4755}, // setuid in the archive
		tarEntry{name: "NOTICE", body: "text", mode: 0o666},
	)
	dst := t.TempDir()
	names, hashes, err := extractTarGz(pkg, dst, allowCoral, DefaultExtractLimits)
	require.NoError(t, err)
	assert.Equal(t, []string{"coral", "NOTICE"}, names)
	sum := sha256.Sum256([]byte("prog"))
	assert.Equal(t, hex.EncodeToString(sum[:]), hashes["coral"], "the hash of each file as it was written")
	fi, _ := os.Stat(filepath.Join(dst, "coral"))
	assert.Equal(t, os.FileMode(0o755), fi.Mode(), "a setuid bit never survives: a program is 0755")
	fi, _ = os.Stat(filepath.Join(dst, "NOTICE"))
	assert.Equal(t, os.FileMode(0o644), fi.Mode(), "a file that is not marked executable is 0644 whatever the archive says")
	b, _ := os.ReadFile(filepath.Join(dst, "coral"))
	assert.Equal(t, "prog", string(b))
}

func TestExtractRefusesEverythingThatIsNotAPlainAllowedFile(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"a symlink":            {{name: "coral", typ: tar.TypeSymlink, linkname: "/etc/passwd"}},
		"a hard link":          {{name: "coral", typ: tar.TypeLink, linkname: "/etc/passwd"}},
		"a device":             {{name: "coral", typ: tar.TypeChar}},
		"a block device":       {{name: "coral", typ: tar.TypeBlock}},
		"a FIFO":               {{name: "coral", typ: tar.TypeFifo}},
		"an absolute path":     {{name: "/usr/bin/coral", body: "x", mode: 0o755}},
		"a dot dot":            {{name: "../coral", body: "x", mode: 0o755}},
		"a dot dot inside":     {{name: "a/../coral", body: "x", mode: 0o755}},
		"a sub folder":         {{name: "bin/coral", body: "x", mode: 0o755}},
		"a folder entry":       {{name: "bin/", typ: tar.TypeDir}},
		"a backslash":          {{name: `a\coral`, body: "x"}},
		"a drive":              {{name: `C:coral`, body: "x"}},
		"a name nobody allows": {{name: "evil", body: "x", mode: 0o755}},
		"a name twice":         {{name: "coral", body: "x"}, {name: "./coral", body: "y"}},
		"a hidden name":        {{name: ".coral", body: "x"}},
		"just dots":            {{name: "..", body: "x"}},
		"a control character":  {{name: "cor\nal", body: "x"}},
	} {
		dst := t.TempDir()
		_, _, err := extractTarGz(makeTarGz(t, entries...), dst, allowCoral, DefaultExtractLimits)
		assert.Error(t, err, name)
		left, _ := os.ReadDir(dst)
		for _, e := range left {
			assert.Equal(t, "coral", e.Name(), name+": only an earlier plain entry may have been written")
		}
		// Nothing is ever written outside dst.
		_, serr := os.Stat(filepath.Join(filepath.Dir(dst), "coral"))
		assert.Error(t, serr, name)
	}
}

func TestExtractHasLimits(t *testing.T) {
	pkg := makeTarGz(t, tarEntry{name: "coral", body: strings.Repeat("x", 100), mode: 0o755})
	_, _, err := extractTarGz(pkg, t.TempDir(), allowCoral, ExtractLimits{MaxFiles: 10, MaxFile: 50, MaxTotal: 1000})
	assert.Error(t, err, "a file over the limit")
	pkg = makeTarGz(t, tarEntry{name: "coral", body: strings.Repeat("x", 40)}, tarEntry{name: "NOTICE", body: strings.Repeat("y", 40)})
	_, _, err = extractTarGz(pkg, t.TempDir(), allowCoral, ExtractLimits{MaxFiles: 10, MaxFile: 50, MaxTotal: 60})
	assert.Error(t, err, "the total over the limit")
	_, _, err = extractTarGz(pkg, t.TempDir(), allowCoral, ExtractLimits{MaxFiles: 1, MaxFile: 50, MaxTotal: 1000})
	assert.Error(t, err, "too many files")

	// Not gzip, a truncated archive.
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	require.NoError(t, os.WriteFile(bad, []byte("not gzip"), 0o600))
	_, _, err = extractTarGz(bad, t.TempDir(), allowCoral, DefaultExtractLimits)
	assert.Error(t, err)
	full, _ := os.ReadFile(makeTarGz(t, tarEntry{name: "coral", body: strings.Repeat("z", 5000)}))
	require.NoError(t, os.WriteFile(bad, full[:len(full)/2], 0o600))
	_, _, err = extractTarGz(bad, t.TempDir(), allowCoral, DefaultExtractLimits)
	assert.Error(t, err, "a truncated archive")
}
