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

	"github.com/subgentic/coral/internal/secfile"
	"github.com/subgentic/coral/internal/updater/verify"
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

// beforeOpen is a test seam: it runs just before a staged file is opened, where a racing process could swap the file for a link.
var beforeOpen func(path string)

// OpenPlain opens a staged file for reading without following a link at the path: the open itself refuses a symbolic link, a junction and a
// folder (secfile.OpenNoFollow), and what was opened is then checked to be a regular file (a FIFO or a device is not). There is no check before
// the open that a swap could outrun (#5303; it was Lstat, then Open). On Windows the open file must also pass secfile.CheckOpen, as the state file
// does: one name, owned by this user, an access list that lets no other account write it (an inherited one is made private) (#126 F8). The
// caller holds the returned file for as long as it relies on it.
func OpenPlain(path string) (*os.File, error) {
	if beforeOpen != nil {
		beforeOpen(path)
	}
	f, err := secfile.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a plain file", filepath.Base(path))
	}
	if err := secfile.CheckOpen(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("staged file %s: %w", filepath.Base(path), err)
	}
	return f, nil
}

// readPlain reads a plain file of at most max bytes: a symlink, junction or anything that is not a regular file is refused.
func readPlain(path string, max int64) ([]byte, error) {
	f, err := OpenPlain(path)
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
	return loadStagedIn(filepath.Join(dataDir, UpdatesDir, StagingDir))
}

// loadStagedIn is LoadStaged for the one version folder under root (the staging folder, or the rollback folder: rollback.go).
func loadStagedIn(root string) (*Staged, error) {
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
	pf, err := OpenPlain(s.PackagePath)
	if err != nil {
		return nil, ErrNoStaged
	}
	pf.Close()
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
	f, err := OpenPlain(s.PackagePath)
	if err != nil {
		return nil, &verify.Failure{Stage: verify.StagePackage, Reason: "the staged package cannot be opened"}
	}
	defer f.Close()
	if _, err := rec.CheckPackage(f, maxPackage); err != nil {
		return nil, err
	}
	return rec, nil
}
