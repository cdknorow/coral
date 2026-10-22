// Package trigger is the server's half of the Windows "Install update" (#166, design #162): the one-time marker that says a person clicked it, and the start of
// the updater from a copy of its own. coral-update apply consumes the marker (Consume) before it asks Windows anything.
package trigger

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/secfile"
)

// UpdatesDir is the data folder's updates folder (stage.UpdatesDir; a test keeps them equal).
const UpdatesDir = "updates"

const (
	markerFile = "install-trigger.json"
	// MarkerTTL is how long a marker is good for: the updater is started right after it is written, so a marker found later than this is a leftover.
	MarkerTTL = 2 * time.Minute
	markerMax = 4 << 10
)

// Marker is the one-time note the dashboard's "Install update" leaves for the updater (#166, Security's N5): a UAC prompt only ever follows a click in Settings.
// Apply consumes it and refuses to ask Windows anything without it, so running coral-update by hand, or from a script, or a stale run, shows no prompt.
type Marker struct {
	Nonce    string `json:"nonce"`
	Version  string `json:"version"`
	IssuedAt int64  `json:"issued_at"` // unix seconds
	// InstallDir is the folder of the server that wrote the marker (the program that quits for the update). When the updater installs nothing
	// (the apply is off, the install is portable or not registered), it starts Coral again from here (#5946): the server has already quit.
	InstallDir string `json:"install_dir,omitempty"`
}

// Errors Consume answers with (the updater reports them as a fixed word, never the path).
var (
	ErrNoMarker      = errors.New("no install was requested")
	ErrMarkerExpired = errors.New("the install request is too old")
	ErrMarkerInvalid = errors.New("the install request is damaged")
)

func markerPath(dataDir string) string { return filepath.Join(dataDir, UpdatesDir, markerFile) }

// WriteMarker writes the marker, replacing any older one, in the private updates folder (a plain file only this user can read or write).
func WriteMarker(dataDir, version, installDir string, now time.Time) error {
	dir := filepath.Join(dataDir, UpdatesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := secfile.ProtectDir(dir); err != nil {
		return err
	}
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return err
	}
	b, err := json.Marshal(Marker{Nonce: hex.EncodeToString(n), Version: version, IssuedAt: now.Unix(), InstallDir: installDir})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+markerFile+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := secfile.Protect(tmp); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, markerPath(dataDir))
}

// RemoveMarker deletes the marker (a start that failed leaves none behind).
func RemoveMarker(dataDir string) { _ = os.Remove(markerPath(dataDir)) }

// Consume reads the marker and deletes it, whatever it holds, so it is good for one apply only. It is an error (and the apply must not ask Windows anything) when there is
// none, when it is not a plain file this user owns, when it is damaged or older than MarkerTTL (now is the clock).
func Consume(dataDir string, now time.Time) (Marker, error) {
	p := markerPath(dataDir)
	f, err := secfile.OpenNoFollow(p, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Marker{}, ErrNoMarker
		}
		_ = os.Remove(p)
		return Marker{}, fmt.Errorf("%w: %v", ErrMarkerInvalid, err)
	}
	defer f.Close()
	defer os.Remove(p) // one apply only, whatever it finds
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return Marker{}, ErrMarkerInvalid
	}
	if err := secfile.CheckOpen(f); err != nil {
		return Marker{}, fmt.Errorf("%w: %v", ErrMarkerInvalid, err)
	}
	b, err := io.ReadAll(io.LimitReader(f, markerMax+1))
	if err != nil || len(b) > markerMax {
		return Marker{}, ErrMarkerInvalid
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil || len(m.Nonce) != 32 || m.Version == "" || m.IssuedAt == 0 {
		return Marker{}, ErrMarkerInvalid
	}
	if m.InstallDir != "" && (len(m.InstallDir) > 1024 || !filepath.IsAbs(m.InstallDir)) {
		return Marker{}, ErrMarkerInvalid // a folder that is not an absolute path is no folder to start Coral from
	}
	age := now.Sub(time.Unix(m.IssuedAt, 0))
	if age > MarkerTTL || age < -time.Minute {
		return Marker{}, ErrMarkerExpired
	}
	return m, nil
}
