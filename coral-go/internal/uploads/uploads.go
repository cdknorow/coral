// Package uploads keeps the data folder's uploads/ directory (images pasted or dropped
// into the dashboard for an agent) from growing without bound (#3364): uploads expire
// after a retention window, the directory has a size cap, and one can be deleted.
package uploads

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Settings and their defaults.
const (
	RetentionSetting     = "uploads_retention_days"
	DefaultRetentionDays = 30
	MaxRetentionDays     = 365

	MaxMBSetting = "uploads_max_mb"
	DefaultMaxMB = 1024
	MinMaxMB     = 50
	MaxMaxMB     = 100 * 1024
)

// MinAge is how old an upload must be before the size cap may evict it: an agent
// may still be reading one pasted a moment ago.
const MinAge = time.Hour

// ErrFull is returned when a new upload can't fit under the cap even after evicting
// every upload older than MinAge.
var ErrFull = errors.New("the uploads folder is full")

// ErrBadName is returned for a name that isn't a plain file name in the folder.
var ErrBadName = errors.New("not an upload name")

// RetentionDays reads uploads_retention_days: 1 to 365, default 30.
func RetentionDays(settings map[string]string) int {
	return intSetting(settings, RetentionSetting, DefaultRetentionDays, 1, MaxRetentionDays)
}

// MaxBytes reads uploads_max_mb: 50 MB to 100 GB, default 1 GB.
func MaxBytes(settings map[string]string) int64 {
	return int64(intSetting(settings, MaxMBSetting, DefaultMaxMB, MinMaxMB, MaxMaxMB)) << 20
}

func intSetting(settings map[string]string, key string, def, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(settings[key]))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

type entry struct {
	path string
	size int64
	mod  time.Time
}

// list returns the regular files directly in dir, oldest first. Anything else
// (directories, symlinks) is left alone.
func list(dir string) ([]entry, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []entry
	for _, de := range des {
		if !de.Type().IsRegular() {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, entry{filepath.Join(dir, de.Name()), info.Size(), info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.Before(out[j].mod) })
	return out, nil
}

// Prune removes uploads last modified before cutoff and reports how many files and
// bytes it removed.
func Prune(dir string, cutoff time.Time) (files int, bytes int64, err error) {
	entries, err := list(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if !e.mod.Before(cutoff) {
			break
		}
		if os.Remove(e.path) == nil {
			files++
			bytes += e.size
		}
	}
	return files, bytes, nil
}

// MakeRoom evicts the oldest uploads until incoming more bytes fit under maxBytes,
// never one younger than MinAge. ErrFull when they still don't fit (nothing younger
// is removed then).
func MakeRoom(dir string, incoming, maxBytes int64, now time.Time) error {
	entries, err := list(dir)
	if err != nil {
		return err
	}
	var total int64
	for _, e := range entries {
		total += e.size
	}
	for _, e := range entries {
		if total+incoming <= maxBytes {
			return nil
		}
		if now.Sub(e.mod) < MinAge {
			break
		}
		if os.Remove(e.path) == nil {
			total -= e.size
		}
	}
	if total+incoming > maxBytes {
		return ErrFull
	}
	return nil
}

// Delete removes one upload by its file name (as returned by the upload, the last
// element of its path). Only a regular file directly in dir: no separators, no "..",
// no symlink.
func Delete(dir, name string) error {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ErrBadName
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrBadName
	}
	return os.Remove(path)
}
