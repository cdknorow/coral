package trigger

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The server must not link the Sigstore verifier (internal/updater/verify's link test: only coral-update does), and the stage package does. So the server's check that
// something is staged is this light one: the names are the stager's (a test keeps them equal), and the updater verifies everything again before it asks Windows anything.
const (
	stagingDir = "staging"
	recordFile = "record.json"
)

var (
	// ErrNoStaged: nothing usable is staged for an installer to install.
	ErrNoStaged = errors.New("no staged update")

	stagedVersionRe = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,4}$`)
)

// StagedMSI is the version of the one staged update, which must be an MSI (a portable folder is updated another way), or ErrNoStaged: a folder named by a plain
// version, a record of that version naming a plain .msi file, and that file there as a regular file. It reads no package and verifies nothing.
func StagedMSI(dataDir string) (string, error) {
	root := filepath.Join(dataDir, UpdatesDir, stagingDir)
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
		return "", ErrNoStaged
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", ErrNoStaged
	}
	var found []os.DirEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // a partial download
		}
		found = append(found, e)
	}
	if len(found) != 1 || !found[0].IsDir() || !stagedVersionRe.MatchString(found[0].Name()) {
		return "", ErrNoStaged
	}
	version := found[0].Name()
	dir := filepath.Join(root, version)
	rf, err := os.Open(filepath.Join(dir, recordFile))
	if err != nil {
		return "", ErrNoStaged
	}
	defer rf.Close()
	b, err := io.ReadAll(io.LimitReader(rf, 1<<20))
	if err != nil {
		return "", ErrNoStaged
	}
	var rec struct {
		Version string `json:"version"`
		Asset   string `json:"asset"`
	}
	if json.Unmarshal(b, &rec) != nil || rec.Version != version || rec.Asset == "" {
		return "", ErrNoStaged
	}
	if rec.Asset != filepath.Base(rec.Asset) || strings.ContainsAny(rec.Asset, "/\\\x00") || strings.HasPrefix(rec.Asset, ".") || !strings.HasSuffix(strings.ToLower(rec.Asset), ".msi") {
		return "", ErrNoStaged
	}
	if fi, err := os.Lstat(filepath.Join(dir, rec.Asset)); err != nil || !fi.Mode().IsRegular() {
		return "", ErrNoStaged
	}
	return version, nil
}
