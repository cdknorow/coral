//go:build !windows

// Not built on Windows: these test the Linux and macOS apply (POSIX file modes, renames over open files, symlinks, a staged record read back by path); the Windows apply is a separate piece (journal, MSI hand-off), #5395.

package apply

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// The real package has a licenses/ tree beside its programs (v1.4.129: 187 folders, 147 files, 8 levels deep, 88 of them read-only): it unpacks, with
// safe modes and the hash of every file.
func TestExtractUnpacksTheLicensesTreeOfARealisticPackage(t *testing.T) {
	entries := []tarEntry{{name: "./", typ: tar.TypeDir, mode: 0o755}, {name: "./coral", body: "prog", mode: 0o755}}
	entries = append(entries, tarEntry{name: "./licenses/", typ: tar.TypeDir, mode: 0o755})
	deep := "licenses"
	var wantFiles []string
	for i := 0; i < 7; i++ {
		deep += fmt.Sprintf("/d%d", i)
		entries = append(entries, tarEntry{name: "./" + deep + "/", typ: tar.TypeDir, mode: 0o755})
		f := deep + "/LICENSE"
		entries = append(entries, tarEntry{name: "./" + f, body: "licence text " + f, mode: 0o444})
		wantFiles = append(wantFiles, f)
	}
	for i := 0; i < 150; i++ {
		d := fmt.Sprintf("licenses/go/mod%03d@v1.0.%d", i, i)
		entries = append(entries, tarEntry{name: "./" + d + "/", typ: tar.TypeDir, mode: 0o755}, tarEntry{name: "./" + d + "/LICENSE", body: "x", mode: 0o755 /* even an executable bit */})
		wantFiles = append(wantFiles, d+"/LICENSE")
	}
	dst := t.TempDir()
	names, hashes, err := extractTarGz(makeTarGz(t, entries...), dst, allowCoral, DefaultExtractLimits)
	require.NoError(t, err)
	assert.Contains(t, names, "coral")
	for _, f := range wantFiles {
		assert.Contains(t, names, f)
		assert.NotEmpty(t, hashes[f], f)
		fi, err := os.Stat(filepath.Join(dst, filepath.FromSlash(f)))
		require.NoError(t, err, f)
		assert.Equal(t, os.FileMode(0o644), fi.Mode(), f+": read-only or executable in the archive, 0644 here")
	}
	di, err := os.Stat(filepath.Join(dst, "licenses", "go"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755)|os.ModeDir, di.Mode())
	tree, err := plainTree(filepath.Join(dst, "licenses"))
	require.NoError(t, err)
	assert.Len(t, tree, len(wantFiles))
}

func TestExtractRefusesWhatIsNotARightLicensesTree(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"a folder that is not licenses":         {{name: "docs/", typ: tar.TypeDir}},
		"a file in a folder that is not":        {{name: "docs/README", body: "x"}},
		"a symlink in licenses":                 {{name: "licenses/x", typ: tar.TypeSymlink, linkname: "/etc/passwd"}},
		"a hard link in licenses":               {{name: "licenses/x", typ: tar.TypeLink, linkname: "coral"}},
		"a FIFO in licenses":                    {{name: "licenses/x", typ: tar.TypeFifo}},
		"a dot dot in licenses":                 {{name: "licenses/../coral", body: "x", mode: 0o755}},
		"a dot dot folder":                      {{name: "licenses/../", typ: tar.TypeDir}},
		"an absolute licenses":                  {{name: "/licenses/x", body: "x"}},
		"a backslash in licenses":               {{name: `licenses/a\b`, body: "x"}},
		"a drive in licenses":                   {{name: "licenses/C:x", body: "x"}},
		"licenses as a file":                    {{name: "licenses", body: "x"}},
		"a file then a folder of the same name": {{name: "licenses/a", body: "x"}, {name: "licenses/a/b", body: "y"}},
		"a file twice":                          {{name: "licenses/a/b", body: "x"}, {name: "licenses/a/b", body: "y"}},
		"nested too deep":                       {{name: strings.Repeat("licenses/", 1) + strings.Repeat("d/", 14) + "f", body: "x"}},
	} {
		dst := t.TempDir()
		_, _, err := extractTarGz(makeTarGz(t, entries...), dst, allowCoral, DefaultExtractLimits)
		assert.Error(t, err, name)
		_, serr := os.Stat(filepath.Join(filepath.Dir(dst), "coral"))
		assert.Error(t, serr, name+": nothing outside dst")
	}
	// Entry counts are limited too.
	var many []tarEntry
	for i := 0; i < 5; i++ {
		many = append(many, tarEntry{name: fmt.Sprintf("licenses/d%d/", i), typ: tar.TypeDir}, tarEntry{name: fmt.Sprintf("licenses/d%d/f", i), body: "x"})
	}
	_, _, err := extractTarGz(makeTarGz(t, many...), t.TempDir(), allowCoral, ExtractLimits{MaxFiles: 100, MaxDirs: 3, MaxDepth: 8, MaxFile: 50, MaxTotal: 1000})
	assert.Error(t, err, "more folders than allowed")
	_, _, err = extractTarGz(makeTarGz(t, many...), t.TempDir(), allowCoral, ExtractLimits{MaxFiles: 3, MaxDirs: 100, MaxDepth: 8, MaxFile: 50, MaxTotal: 1000})
	assert.Error(t, err, "more files than allowed")
}

// The release tarball as published (tests/release/upgrade_linux.sh and CORAL_APPLY_NEW_TARBALL give its path; a developer can point it at any
// coral-linux-<arch>-<version>.tar.gz): it unpacks under the real limits and the folder it makes passes the folder checks.
func TestTheRealReleaseTarballUnpacksAndIsCorals(t *testing.T) {
	pkg := os.Getenv("CORAL_APPLY_REAL_TARBALL")
	if pkg == "" {
		pkg = os.Getenv("CORAL_APPLY_NEW_TARBALL")
	}
	if pkg == "" {
		t.Skip("set CORAL_APPLY_REAL_TARBALL to a published coral-linux tarball")
	}
	dst := filepath.Join(t.TempDir(), "coral-bin")
	require.NoError(t, os.Mkdir(dst, 0o755))
	names, hashes, err := extractTarGz(pkg, dst, allowedName, DefaultExtractLimits)
	require.NoError(t, err, "the strict unpack accepts the real package")
	assert.Contains(t, names, "coral")
	assert.Len(t, hashes, len(names))
	l := &Linux{ManagedPrefixes: []string{}, SwapSupported: func(string) bool { return true }}
	assert.Empty(t, l.Unsupported(dst), "a real tarball install is Coral's own folder, licenses/ included")
	tree, err := plainTree(filepath.Join(dst, licensesDir))
	require.NoError(t, err)
	assert.NotEmpty(t, tree)
}
