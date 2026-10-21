// Package transcriptlink persists explicit Coral-to-provider session identities.
package transcriptlink

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/safefs"
)

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsSessionID reports whether id is a lowercase UUID: the only form a Coral session id takes.
// A resolver that globs or joins an id into a path checks it first, so "", "*", "?", "[a-z]" or
// a part of an id never matches (or names) another session's transcript (#4590).
func IsSessionID(id string) bool { return uuid.MatchString(id) }

type CodexLink struct {
	ThreadID string `json:"thread_id"`
	Path     string `json:"path"`
}

func codexHome() string {
	if p := os.Getenv("CODEX_HOME"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// maxLinkBytes caps a link file's size: it holds two short fields.
const maxLinkBytes = 64 << 10

// Codex reads a small explicit mapping, never inferring identity from cwd or
// arbitrary conversation text. The mapping is outside provider transcripts.
//
// The link folder is one the Codex agent can write (#3597, weak site W19), so the file is read
// as a capped regular file (never a symlink or a FIFO), and its Path is believed only when it is
// that thread's own rollout file inside <CODEX_HOME>/sessions: named rollout-*-<thread>.jsonl,
// a regular file, resolving inside the sessions folder. Otherwise the Path is dropped (the
// thread id, a UUID, is kept) and callers find the rollout file themselves.
func Codex(sessionID string) CodexLink {
	if !uuid.MatchString(sessionID) {
		return CodexLink{}
	}
	root, err := safefs.OpenRoot(filepath.Join(codexHome(), "coral-session-links"))
	if err != nil {
		return CodexLink{}
	}
	b, err := root.ReadFile(sessionID+".json", maxLinkBytes)
	root.Close()
	if err != nil {
		return CodexLink{}
	}
	var link CodexLink
	if json.Unmarshal(b, &link) != nil || !uuid.MatchString(link.ThreadID) {
		return CodexLink{}
	}
	link.Path = rolloutPath(link.Path, link.ThreadID)
	return link
}

// InCodexSessions reports whether path is inside <CODEX_HOME>/sessions (as written, or under the
// folder's resolved path): where Codex writes its rollout files, a folder the agent can write.
func InCodexSessions(path string) bool {
	_, ok := sessionsRel(path)
	return ok
}

func sessionsRel(path string) (string, bool) {
	sessions := filepath.Join(codexHome(), "sessions")
	bases := []string{filepath.Clean(sessions)}
	if real, err := safefs.Resolve(sessions); err == nil {
		bases = append([]string{real}, bases...)
	}
	clean := filepath.Clean(path)
	for _, base := range bases {
		r, err := filepath.Rel(base, clean)
		if err == nil && r != "." && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r) {
			return r, true
		}
	}
	return "", false
}

// OpenRollout opens a Codex rollout file, a path inside <CODEX_HOME>/sessions, through a root
// held on that folder (#3599, Security's SHOULD on #3597): the Codex agent can write there, so a
// file checked by Codex() and then swapped for a symlink, or a symlinked folder on the way, is
// refused at open time instead of followed. A regular file only, opened non-blocking. A path
// outside the sessions folder is refused.
func OpenRollout(path string) (*os.File, error) {
	rel, ok := sessionsRel(path)
	if !ok {
		return nil, safefs.ErrOutside
	}
	root, err := safefs.OpenRoot(filepath.Join(codexHome(), "sessions"))
	if err != nil {
		return nil, err
	}
	defer root.Close() // the file stays open
	f, _, err := root.Open(rel)
	return f, err
}

// rolloutPath returns p resolved when it is threadID's rollout file inside
// <CODEX_HOME>/sessions, and "" otherwise.
func rolloutPath(p, threadID string) string {
	if p == "" || !filepath.IsAbs(p) || safefs.IsUNC(p) {
		return ""
	}
	if ok, _ := filepath.Match("rollout-*-"+threadID+".jsonl", filepath.Base(p)); !ok {
		return ""
	}
	sessions := filepath.Join(codexHome(), "sessions")
	rel, err := safefs.Within(sessions, p)
	if err != nil {
		return ""
	}
	real, err := safefs.Resolve(sessions)
	if err != nil {
		return ""
	}
	full := filepath.Join(real, rel)
	if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return full
}

// BindCodex records an explicit identity from a hook or operator repair. Verify
// the native session metadata before persisting it, and replace atomically so
// readers never observe a partially written mapping.
func BindCodex(sessionID, threadID string) error {
	if !uuid.MatchString(sessionID) || !uuid.MatchString(threadID) {
		return fmt.Errorf("Coral session and Codex thread IDs must be UUIDs")
	}
	if old := Codex(sessionID); old.ThreadID == threadID {
		if _, err := os.Stat(old.Path); err == nil {
			return nil
		}
	}
	paths, err := filepath.Glob(filepath.Join(codexHome(), "sessions", "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	if err != nil {
		return err
	}
	var path string
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		line, readErr := bufio.NewReader(f).ReadBytes('\n')
		f.Close()
		var meta struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if readErr == nil && json.Unmarshal(line, &meta) == nil && meta.Type == "session_meta" && meta.Payload.ID == threadID {
			path, _ = filepath.Abs(p)
			break
		}
	}
	if path == "" {
		return fmt.Errorf("no verified Codex transcript for thread %s", threadID)
	}
	// A session that already has a link to a live rollout keeps it unless the new rollout is
	// marked as this session's own: another Codex's hook (a Codex started from this one's shell)
	// must not take the link over (#4590). A first bind, or one over a vanished rollout, needs no marker.
	if old := Codex(sessionID); old.ThreadID != "" && old.ThreadID != threadID && old.Path != "" {
		if _, err := os.Stat(old.Path); err == nil && rolloutMarker(path) != sessionID {
			return fmt.Errorf("session %s keeps its link to thread %s: the rollout of thread %s is not marked as this session's", sessionID, old.ThreadID, threadID)
		}
	}
	dir := filepath.Join(codexHome(), "coral-session-links")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, _ := json.Marshal(CodexLink{ThreadID: threadID, Path: path})
	f, err := os.CreateTemp(dir, ".link-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, sessionID+".json"))
}

// markerScanBytes caps how much of a rollout's head rolloutMarker reads: the marker is in the
// developer instructions Codex records at the start of a session.
const markerScanBytes = 1 << 20

var markerID = regexp.MustCompile(regexp.QuoteMeta(at.CoralSessionMarkerPrefix) + `\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// rolloutMarker is the Coral session id in the first developer message of the rollout at path
// (opened through the sessions root, like every rollout read), or "" when it has none.
func rolloutMarker(path string) string {
	f, err := OpenRollout(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), markerScanBytes)
	for read := 0; sc.Scan() && read < markerScanBytes; {
		line := sc.Bytes()
		read += len(line) + 1
		if !bytes.Contains(line, []byte(at.CoralSessionMarkerPrefix)) {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
				Role string `json:"role"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "response_item" || e.Payload.Type != "message" || e.Payload.Role != "developer" {
			continue // a marker quoted in a user or assistant message names nothing
		}
		if m := markerID.FindSubmatch(line); m != nil {
			return string(m[1])
		}
	}
	return ""
}
