package routes

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/subgentic/coral/internal/secfile"
)

// Read only the existing identity for this instance; viewing Settings must not
// create or rotate it, including when analytics is disabled. Do not follow a
// replacement symlink or expose arbitrary file contents as an installation ID.
func telemetryInstallID(dir string) (string, string) {
	f, err := secfile.OpenNoFollow(filepath.Join(dir, ".install_id"), os.O_RDONLY, 0)
	if os.IsNotExist(err) {
		return "", "missing"
	}
	if err != nil {
		return "", "unavailable"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", "unavailable"
	}
	b, err := io.ReadAll(io.LimitReader(f, 129))
	id := strings.TrimSpace(string(b))
	if err != nil || len(b) > 128 || len(id) != 36 || uuid.Validate(id) != nil {
		return "", "unavailable"
	}
	return id, "available"
}
