package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/subgentic/coral/internal/secfile"
)

// The files the updater keeps in <data dir>/updates (the directory is 0700, each file 0600 or the owner-only ACL on Windows). They survive a
// restart. Nothing the hub or an agent reaches writes them: no route does, and the file routes refuse a path under the data directory.
const (
	StateFile    = "state.json"
	AckUsedFile  = "ack_used.json"
	maxStateSize = 1 << 20
	maxHistory   = 50
)

// ErrCorrupt means a file existed and could not be read as what it should be. For the state it is treated as a first run (a zero State) and never as
// permission: every rule that needs the state still applies, and the installed version bounds the newest manifest from below.
var ErrCorrupt = errors.New("the updater's file is unreadable")

// ensureDir makes dir (0700) and refuses a dir that is a symlink or not a directory. On Windows, where the mode means nothing, the folder gets
// the private access list secfile.ProtectDir makes, inherited by what is created in it (the staging folders, the package, the state files):
// this user and SYSTEM only, whatever the parent allows (#126 F8). A folder that can't be made private is an error, not a folder used as it is.
func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err := secfile.ProtectDir(dir); err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

// readPrivate reads path (at most maxStateSize) refusing a symlink or anything that is not a regular file. A missing file is fs.ErrNotExist.
func readPrivate(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrCorrupt, path)
	}
	if err := checkPrivate(fi); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := secfile.CheckOpen(f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStateSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxStateSize {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrCorrupt, path, maxStateSize)
	}
	return b, nil
}

// writePrivate writes data to path atomically (a temp file in the same directory, synced, renamed) with mode 0600.
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensureDir(dir); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	// Private before anything is written to it, and an error when it can't be (#126 F8): it was set after the data, its error ignored.
	if err := secfile.Protect(name); err != nil {
		return fmt.Errorf("make %s private: %w", name, err)
	}
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// LoadState reads dir's state.json. A missing file is a first run (a zero State, nil error). An unreadable or invalid one is a zero State and
// ErrCorrupt, for the caller to log: it is not permission to install anything the rules would refuse.
func LoadState(dir string) (State, error) {
	b, err := readPrivate(filepath.Join(dir, StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if _, err := ParseVersionOrEmpty(st.Version); err != nil || st.Sequence < 0 {
		return State{}, fmt.Errorf("%w: invalid version or sequence", ErrCorrupt)
	}
	for _, r := range st.Revoked {
		if _, err := ParseVersion(r); err != nil {
			return State{}, fmt.Errorf("%w: invalid revoked version", ErrCorrupt)
		}
	}
	return st, nil
}

// ParseVersionOrEmpty accepts "" (nothing accepted yet) as well as a version.
func ParseVersionOrEmpty(s string) ([3]int, error) {
	if s == "" {
		return [3]int{}, nil
	}
	return ParseVersion(s)
}

// SaveState writes the state atomically, 0600, with the history capped.
func SaveState(dir string, st State) error {
	if len(st.History) > maxHistory {
		st.History = st.History[len(st.History)-maxHistory:]
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, StateFile), b)
}

// Accept moves the state forward to rec once the new version is installed and has passed its health check (never at staging: a rollback must not
// leave the high-water mark ahead of what runs). The version and sequence only go up, and the revoked lists are unioned: a revoked version stays so.
func (s *State) Accept(rec *Record) error {
	if rec.Version == "" {
		return errors.New("an empty record")
	}
	if s.Version != "" {
		if c, err := CompareVersions(rec.Version, s.Version); err != nil || c < 0 {
			return fmt.Errorf("version %s is lower than the accepted %s", rec.Version, s.Version)
		}
	}
	if rec.Sequence < s.Sequence {
		return fmt.Errorf("sequence %d is lower than the accepted %d", rec.Sequence, s.Sequence)
	}
	s.Version, s.Sequence = rec.Version, rec.Sequence
	s.Revoked = unionVersions(s.Revoked, rec.Revoked)
	s.LastFailure = nil
	return nil
}

// Observe records what a manifest that passed verification revokes, at once and not only at install: a version it names is revoked for good. Version and Sequence stay
// what is installed (Accept moves them), so a staged older release stays installable if a newer one cannot be downloaded. Without this, a manifest replayed between staging
// and install (an older one, still unexpired, that does not revoke what a later one did) would be taken as new (#5301). Seen keeps the highest verified sequence: where the
// next check starts looking for promotions (#326), a hint only, never a rule.
func (s *State) Observe(rec *Record) {
	s.Revoked = unionVersions(s.Revoked, rec.Revoked)
	if rec.Sequence > s.Seen {
		s.Seen = rec.Sequence
	}
}

// Refuse records a refusal for Settings > Updates.
func (s *State) Refuse(version string, f *Failure, now time.Time) {
	s.LastFailure = &FailureRecord{Version: version, Stage: f.Stage, Reason: f.Reason, At: now.UTC().Format(time.RFC3339)}
}

func unionVersions(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range append(append([]string{}, a...), b...) {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// AckStore is the set of relaunch tokens already used (#5245 section 13), kept in ack_used.json so a token used before a restart is still used.
// Entries are kept until they would have expired.
type AckStore struct {
	dir string
}

type ackFile struct {
	Used map[string]int64 `json:"used"` // token id -> unix seconds it expires
}

const maxAckEntries = 4096

// NewAckStore opens dir's store.
func NewAckStore(dir string) *AckStore { return &AckStore{dir: dir} }

// Use marks id used until expires and reports whether it was unused: false is a repeat. An unreadable store is an error (fail closed: nothing is
// treated as unused when the record of what was used cannot be read).
func (a *AckStore) Use(id string, expires, now time.Time) (bool, error) {
	if id == "" {
		return false, errors.New("an empty token id")
	}
	path := filepath.Join(a.dir, AckUsedFile)
	var f ackFile
	b, err := readPrivate(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, err
	default:
		if err := json.Unmarshal(b, &f); err != nil {
			return false, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
	}
	if f.Used == nil {
		f.Used = map[string]int64{}
	}
	for k, exp := range f.Used {
		if exp <= now.Unix() {
			delete(f.Used, k)
		}
	}
	if _, repeat := f.Used[id]; repeat {
		return false, nil
	}
	if len(f.Used) >= maxAckEntries {
		return false, errors.New("too many unexpired tokens")
	}
	f.Used[id] = expires.Unix()
	out, err := json.Marshal(f)
	if err != nil {
		return false, err
	}
	if err := writePrivate(path, out); err != nil {
		return false, err
	}
	return true, nil
}
