package stage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/updater/verify"
)

// ErrNoStaged means nothing usable is staged.
var ErrNoStaged = errors.New("no staged update")

// Staged is what a staging folder holds. The Record is a pointer to what to check, not proof: apply verifies again (Reverify).
type Staged struct {
	Version     string
	Dir         string
	PackagePath string
	Candidate   verify.Candidate
	Record      *verify.Record
}

// readPlain reads a plain file of at most max bytes: a symlink, junction or anything that is not a regular file is refused.
func readPlain(path string, max int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), max)
	}
	return b, nil
}

// LoadStaged reads the one staged version under dataDir. It is ErrNoStaged for nothing, for more than one version folder, for a folder that is not
// named by its record's version, and for a file that is missing, a link, or too large: the staging folder is user-writable and trusted as little
// as anything else.
func LoadStaged(dataDir string) (*Staged, error) {
	root := filepath.Join(dataDir, UpdatesDir, StagingDir)
	fi, err := os.Lstat(root)
	if err != nil || !fi.IsDir() {
		return nil, ErrNoStaged
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, ErrNoStaged
	}
	var versions []os.DirEntry
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			continue // a partial download
		}
		versions = append(versions, e)
	}
	if len(versions) != 1 || !versions[0].IsDir() {
		return nil, ErrNoStaged
	}
	version := versions[0].Name()
	if _, err := verify.ParseVersion(version); err != nil {
		return nil, ErrNoStaged
	}
	dir := filepath.Join(root, version)
	rb, err := readPlain(filepath.Join(dir, RecordFile), 1<<20)
	if err != nil {
		return nil, ErrNoStaged
	}
	var rec verify.Record
	if err := json.Unmarshal(rb, &rec); err != nil || rec.Version != version || rec.Asset == "" {
		return nil, ErrNoStaged
	}
	// The record sits in a user-writable folder: its asset name is a plain file name of this folder, never a path out of it.
	if rec.Asset != filepath.Base(rec.Asset) || rec.Asset == "." || rec.Asset == ".." || strings.ContainsAny(rec.Asset, "/\\\x00") || rec.Asset[0] == '.' {
		return nil, ErrNoStaged
	}
	s := &Staged{Version: version, Dir: dir, Record: &rec, PackagePath: filepath.Join(dir, rec.Asset)}
	for _, f := range []struct {
		name string
		max  int64
		dst  *[]byte
	}{
		{ManifestFile, verify.MaxManifestBytes, &s.Candidate.Manifest}, {ManifestBundleFile, verify.MaxBundleBytes, &s.Candidate.ManifestBundle},
		{SumsFile, verify.MaxSumsBytes, &s.Candidate.Sums}, {SumsBundleFile, verify.MaxBundleBytes, &s.Candidate.SumsBundle},
	} {
		b, err := readPlain(filepath.Join(dir, f.name), f.max)
		if err != nil {
			return nil, ErrNoStaged
		}
		*f.dst = b
	}
	if pfi, err := os.Lstat(s.PackagePath); err != nil || !pfi.Mode().IsRegular() {
		return nil, ErrNoStaged
	}
	return s, nil
}

// Reverify is what apply does with a staged release: the whole chain again from the staged files against the pins (the record proves nothing), then
// the package hashed against the sums file's entry. It returns the fresh record. A *verify.Failure names the stage that refused.
func Reverify(v verify.BundleVerifier, pins verify.Pins, s *Staged, asset func(string) string, st verify.State, installed string, now time.Time, maxPackage int64) (*verify.Record, error) {
	rec, err := verify.Release(v, pins, s.Candidate, asset, st, installed, now)
	if err != nil {
		return nil, err
	}
	if rec.Version != s.Version || filepath.Join(s.Dir, rec.Asset) != s.PackagePath {
		return nil, &verify.Failure{Stage: verify.StagePackage, Reason: "the staged folder is not the verified release's"}
	}
	f, err := os.Open(s.PackagePath)
	if err != nil {
		return nil, &verify.Failure{Stage: verify.StagePackage, Reason: "the staged package cannot be opened"}
	}
	defer f.Close()
	if _, err := rec.CheckPackage(f, maxPackage); err != nil {
		return nil, err
	}
	return rec, nil
}
