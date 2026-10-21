package jsonl

import (
	"bufio"
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/transcriptlink"
)

// TranscriptPath returns the transcript file for a session, or "" when the
// agent has not written one yet.
func TranscriptPath(sessionID, workingDirectory, agentType string) string {
	return resolveTranscriptPath(sessionID, workingDirectory, agentType)
}

// TranscriptFingerprint names the transcript file of a session as it is now: its path and a fingerprint of it (its size and modification time: a stat, never a read), and false
// when the agent has no transcript here (#5802: the Saved list asks about a message only when this changed).
func TranscriptFingerprint(sessionID, workingDirectory, agentType string) (path, fingerprint string, ok bool) {
	path, fingerprint, ok, _ = TranscriptFingerprintCtx(context.Background(), sessionID, workingDirectory, agentType)
	return path, fingerprint, ok
}

// TranscriptFingerprintCtx is TranscriptFingerprint that gives up when ctx ends (#5820: a Codex history of thousands of rollouts is walked to find a session, and the Saved list
// has a time budget). aborted is true when it did: nothing is known then, which is not "no transcript". Only the agent type given is looked in: a Claude session never walks
// ~/.codex.
func TranscriptFingerprintCtx(ctx context.Context, sessionID, workingDirectory, agentType string) (path, fingerprint string, ok, aborted bool) {
	path, fingerprint, ok, aborted, _ = transcriptFingerprint(ctx, sessionID, workingDirectory, agentType, false)
	return path, fingerprint, ok, aborted
}

// TranscriptFingerprintListCtx is TranscriptFingerprintCtx for a list that must not wait (the Saved list, #5820): a Codex rollout is looked for by its explicit link and its file
// name only, and when that finds none, unsure is true: the rollout may still exist under a name that does not hold the session id (found only by scanning every rollout for the
// session's marker, which the one who opens the bookmark does). Nothing is known then, which is neither "found" nor "no transcript".
func TranscriptFingerprintListCtx(ctx context.Context, sessionID, workingDirectory, agentType string) (path, fingerprint string, ok, aborted, unsure bool) {
	return transcriptFingerprint(ctx, sessionID, workingDirectory, agentType, true)
}

func transcriptFingerprint(ctx context.Context, sessionID, workingDirectory, agentType string, nameOnly bool) (path, fingerprint string, ok, aborted, unsure bool) {
	if agentType == at.Codex {
		path = resolveCodexTranscriptCtx(ctx, sessionID, nameOnly)
	} else {
		path = resolveTranscriptPath(sessionID, workingDirectory, agentType)
	}
	if ctx.Err() != nil {
		return "", "", false, true, false
	}
	if path == "" {
		return "", "", false, false, nameOnly && agentType == at.Codex && transcriptlink.IsSessionID(sessionID)
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", "", false, false, false
	}
	return path, fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano()), true, false, false
}

// resolveTranscriptPath finds the transcript file for a session.
func resolveTranscriptPath(sessionID, workingDirectory, agentType string) string {
	return resolveTranscriptPathMode(sessionID, workingDirectory, agentType, false)
}

func resolveTranscriptPathMode(sessionID, workingDirectory, agentType string, strictLive bool) string {
	switch agentType {
	case at.Agy, at.Antigravity, at.Gemini:
		if strictLive {
			return resolveAgyTranscriptStrict(sessionID, workingDirectory)
		}
		return resolveAgyTranscript(sessionID, workingDirectory)
	case at.Codex:
		return resolveCodexTranscript(sessionID)
	default:
		return resolveClaudeTranscript(sessionID, workingDirectory)
	}
}

func resolveClaudeTranscript(sessionID, workingDirectory string) string {
	if !transcriptlink.IsSessionID(sessionID) {
		return ""
	}
	home, _ := os.UserHomeDir()
	basePath := os.Getenv("CLAUDE_PROJECTS_DIR")
	if basePath == "" {
		basePath = filepath.Join(home, ".claude", "projects")
	}

	// Try working directory hint first
	if workingDirectory != "" {
		encoded := transcriptlink.ClaudeProjectDirName(workingDirectory)
		candidate := filepath.Join(basePath, encoded, sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Search all project dirs
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(basePath, entry.Name(), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func resolveAgyTranscript(sessionID string, workingDirectory ...string) string {
	return resolveAgyTranscriptMode(sessionID, false, workingDirectory...)
}

func resolveAgyTranscriptStrict(sessionID string, workingDirectory ...string) string {
	return resolveAgyTranscriptMode(sessionID, true, workingDirectory...)
}

func resolveAgyTranscriptMode(sessionID string, strictLive bool, workingDirectory ...string) string {
	if !safefs.IsSafeID(sessionID) { // joined into paths below; native history passes conversation ids
		return ""
	}
	home, _ := os.UserHomeDir()
	basePath := os.Getenv("ANTIGRAVITY_DATA_DIR")
	if basePath == "" {
		basePath = filepath.Join(home, ".gemini", "antigravity-cli", "brain")
	} else if filepath.Base(basePath) != "brain" {
		if info, err := os.Stat(filepath.Join(basePath, "brain")); err == nil && info.IsDir() {
			basePath = filepath.Join(basePath, "brain")
		}
	}

	// Direct match: brain/<sessionID>/.system_generated/logs/transcript.jsonl
	direct := filepath.Join(basePath, sessionID, ".system_generated", "logs", "transcript.jsonl")
	if _, err := os.Stat(direct); err == nil {
		if !strictLive || agyTranscriptCoralSessionID(direct) == sessionID {
			return direct
		}
	}

	// Search brain directories for embedded coral session ID, newest first
	if entries, err := os.ReadDir(basePath); err == nil {
		type dirEntryWithTime struct {
			name    string
			modTime time.Time
		}
		var dirs []dirEntryWithTime
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if info, err := entry.Info(); err == nil {
				dirs = append(dirs, dirEntryWithTime{name: entry.Name(), modTime: info.ModTime()})
			}
		}
		sort.Slice(dirs, func(i, j int) bool {
			return dirs[i].modTime.After(dirs[j].modTime)
		})
		for _, d := range dirs {
			candidate := filepath.Join(basePath, d.name, ".system_generated", "logs", "transcript.jsonl")
			if _, err := os.Stat(candidate); err == nil {
				if agyTranscriptCoralSessionID(candidate) == sessionID {
					return candidate
				}
			}
		}
	}

	// Fallback: match by workingDirectory in history.jsonl
	var workdir string
	if len(workingDirectory) > 0 {
		workdir = workingDirectory[0]
	}
	if workdir != "" {
		agyHome := filepath.Dir(basePath)
		historyPath := filepath.Join(agyHome, "history.jsonl")
		if candidate := resolveAgyTranscriptFromHistory(historyPath, basePath, workdir); candidate != "" {
			// A live Coral ID is authoritative. A workspace-only history match
			// may belong to an older session and must not be cached under this ID.
			if !strictLive || agyTranscriptCoralSessionID(candidate) == sessionID {
				return candidate
			}
		}
	}

	// Legacy Gemini files have no Coral marker and cannot be trusted for an
	// explicit live session. Native/no-ID callers still retain this fallback.
	if strictLive {
		return ""
	}

	// Fallback to legacy Gemini tmp directory
	legacyBase := os.Getenv("GEMINI_TMP_DIR")
	if legacyBase == "" {
		legacyBase = filepath.Join(home, ".gemini", "tmp")
	}
	if entries, err := os.ReadDir(legacyBase); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(legacyBase, entry.Name(), "chats", sessionID+".json")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

func isAgyAgentType(agentType string) bool {
	switch agentType {
	case at.Agy, at.Antigravity, at.Gemini:
		return true
	default:
		return false
	}
}

func resolveAgyTranscriptFromHistory(historyPath, basePath, workingDirectory string) string {
	f, err := os.Open(historyPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}

	cleanTarget := filepath.Clean(workingDirectory)
	for i := len(lines) - 1; i >= 0; i-- {
		var entry map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &entry); err != nil {
			continue
		}
		ws, _ := entry["workspace"].(string)
		if ws == "" {
			continue
		}
		if filepath.Clean(ws) == cleanTarget {
			// The id is a folder name under basePath, never a path: one with a separator,
			// ".." or a leading "-" is ignored (#3597, weak site W20).
			if convID, _ := entry["conversationId"].(string); safefs.IsSafeID(convID) {
				candidate := filepath.Join(basePath, convID, ".system_generated", "logs", "transcript.jsonl")
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}
	return ""
}

// openTranscript opens an agent's transcript for reading. Codex's rollout files sit in a folder
// the agent can write, so they are opened through a root on that folder, never following a
// symlink (#3599): a Codex session's transcript always, and any path inside that folder.
// The other agents' transcripts are opened by path.
func openTranscript(path, agentType string) (*os.File, error) {
	if agentType == at.Codex {
		return transcriptlink.OpenRollout(path)
	}
	return openTranscriptFile(path)
}

// openTranscriptFile opens path, through transcriptlink.OpenRollout when it is inside Codex's
// sessions folder.
func openTranscriptFile(path string) (*os.File, error) {
	if transcriptlink.InCodexSessions(path) {
		return transcriptlink.OpenRollout(path)
	}
	return os.Open(path)
}

// CodexTranscriptPath is the rollout file of a Coral Codex session, found only by what names
// that session: the link its hook wrote from Codex's own payload, the session id in the file
// name, or the Coral marker in the rollout's developer message. Never by folder or time (#4590).
func CodexTranscriptPath(sessionID string) string { return resolveCodexTranscript(sessionID) }

func resolveCodexTranscript(sessionID string) string {
	return resolveCodexTranscriptCtx(context.Background(), sessionID, false)
}

// resolveCodexTranscriptCtx finds the rollout of a Coral session; its walks of ~/.codex/sessions stop when ctx ends (the answer is then "" and ctx.Err() says why). With nameOnly
// it looks no further than the explicit link and the file name (one glob): the walks, and the scan of every rollout for the session's marker, are left to a caller that can wait.
func resolveCodexTranscriptCtx(ctx context.Context, sessionID string, nameOnly bool) string {
	if !transcriptlink.IsSessionID(sessionID) {
		return ""
	}
	if link := transcriptlink.Codex(sessionID); link.Path != "" {
		return link.Path
	}
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	basePath := filepath.Join(codexHome, "sessions")

	// Codex stores transcripts in YYYY/MM/DD/rollout-{timestamp}-{id}.jsonl
	// Walk the date-based directory structure to find the session
	matches, err := filepath.Glob(filepath.Join(basePath, "*", "*", "*", "rollout-*"+sessionID+"*.jsonl"))
	if err == nil && len(matches) > 0 {
		return matches[0]
	}
	if nameOnly {
		return ""
	}

	// Fallback: search all rollout files for a matching session ID
	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.Contains(filepath.Base(path), sessionID) && strings.HasSuffix(path, ".jsonl") {
			matches = append(matches, path)
			return filepath.SkipAll
		}
		return nil
	})
	if len(matches) > 0 {
		return matches[0]
	}
	return resolveCodexTranscriptByMarkerCtx(ctx, basePath, sessionID)
}

// A restart can keep Coral's identity while changing the native Codex thread.
// Check the small explicit mapping even when a previous rollout is cached.
func refreshCodexLink(c *sessionCache, sessionID string) {
	link := transcriptlink.Codex(sessionID)
	if link.Path == "" || link.Path == c.path {
		return
	}
	c.path = link.Path
	c.resetMessages()
	c.codexState = codexTurnCache{}
}

func resolveCodexTranscriptByMarker(basePath, sessionID string) string {
	return resolveCodexTranscriptByMarkerCtx(context.Background(), basePath, sessionID)
}

func resolveCodexTranscriptByMarkerCtx(ctx context.Context, basePath, sessionID string) string {
	type candidate struct {
		path string
		mod  time.Time
	}
	var matches []candidate
	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(path), "rollout-") || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if codexTranscriptCoralSessionIDCached(path, info) == sessionID {
			matches = append(matches, candidate{path: path, mod: info.ModTime()})
		}
		return nil
	})
	if len(matches) == 0 || ctx.Err() != nil {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].mod.After(matches[j].mod) })
	return matches[0].path
}

func codexTranscriptCoralSessionID(path string) string {
	f, err := openTranscriptFile(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	id, _, _ := scanCodexMarker(f)
	return id
}

// scanCodexMarker returns the Coral session id from the first developer
// message carrying a marker. Without one, complete is the number of bytes of
// newline-terminated lines read (an unterminated last line may still be
// mid-write), and stuck reports that scanning stopped at a line too long for
// the scanner, which later appends cannot change.
func scanCodexMarker(r io.Reader) (id string, complete int64, stuck bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var pos int64
	var terminated bool
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		if advance > 0 {
			pos += int64(advance)
			terminated = bytes.IndexByte(data[:advance], '\n') >= 0
		}
		return advance, token, err
	})
	for scanner.Scan() {
		if terminated {
			complete = pos
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		// The marker is injected into Codex's developer instructions. Restrict
		// association to that trusted record; scanning arbitrary user/assistant
		// text lets a transcript mention another UUID and steal its identity.
		if entry["type"] != "response_item" {
			continue
		}
		payload, _ := entry["payload"].(map[string]any)
		if payload == nil || payload["type"] != "message" || payload["role"] != "developer" {
			continue
		}
		if sessionID := agent.ExtractCoralSessionID(payload["content"]); sessionID != "" {
			return sessionID, complete, false
		}
	}
	return "", complete, scanner.Err() != nil
}

// Rollout files are append-only, so a file's marker answer only changes by
// lines appended after the ones already read. Resolving an unlinked Codex
// session would otherwise re-parse every rollout under ~/.codex/sessions
// (gigabytes on a long-lived install) each time it is retried.
type codexMarkerEntry struct {
	info     os.FileInfo
	size     int64
	modTime  time.Time
	head     [sha256.Size]byte
	complete int64
	id       string
	stuck    bool
}

// codexMarkers keeps the answer for each rollout read, the least recently used going first when it is full (#5820: it was emptied whole at 4096 entries, so with more rollouts
// than that every resolution of a session with no match read them all again, minutes for ten thousand). It is shared by the live chat, the history view and the Saved list.
var codexMarkers struct {
	sync.Mutex
	entries map[string]*list.Element // path -> element of order, whose value is a *codexMarkerItem
	order   *list.List               // most recently used first
}

// codexMarkerMax bounds the rollouts remembered: tens of thousands, a few hundred bytes each.
const codexMarkerMax = 32768

type codexMarkerItem struct {
	path  string
	entry codexMarkerEntry
}

func codexMarkerGet(path string) (codexMarkerEntry, bool) {
	codexMarkers.Lock()
	defer codexMarkers.Unlock()
	el, ok := codexMarkers.entries[path]
	if !ok {
		return codexMarkerEntry{}, false
	}
	codexMarkers.order.MoveToFront(el)
	return el.Value.(*codexMarkerItem).entry, true
}

func codexMarkerPut(path string, e codexMarkerEntry) {
	codexMarkers.Lock()
	defer codexMarkers.Unlock()
	if codexMarkers.entries == nil {
		codexMarkers.entries, codexMarkers.order = map[string]*list.Element{}, list.New()
	}
	if el, ok := codexMarkers.entries[path]; ok {
		el.Value.(*codexMarkerItem).entry = e
		codexMarkers.order.MoveToFront(el)
		return
	}
	codexMarkers.entries[path] = codexMarkers.order.PushFront(&codexMarkerItem{path: path, entry: e})
	for codexMarkers.order.Len() > codexMarkerMax {
		last := codexMarkers.order.Back()
		delete(codexMarkers.entries, last.Value.(*codexMarkerItem).path)
		codexMarkers.order.Remove(last)
	}
}

const codexMarkerHeadBytes = 4096

// codexTranscriptCoralSessionIDCached returns what codexTranscriptCoralSessionID
// would, reading only the bytes appended since the previous call. A replaced,
// truncated or rewritten file (identity, size or first 4KB changed) is read
// again from the start. seen is the file's info from the directory walk, if
// any: a file whose identity, size and mtime are unchanged is not opened.
func codexTranscriptCoralSessionIDCached(path string, seen os.FileInfo) string {
	if seen != nil {
		prev, ok := codexMarkerGet(path)
		if ok && os.SameFile(prev.info, seen) && seen.Size() == prev.size && seen.ModTime().Equal(prev.modTime) {
			return prev.id
		}
	}
	f, err := openTranscriptFile(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	headBuf := make([]byte, codexMarkerHeadBytes)
	n, _ := f.ReadAt(headBuf, 0)
	head := sha256.Sum256(headBuf[:n])

	prev, ok := codexMarkerGet(path)
	var start int64
	if ok && os.SameFile(prev.info, info) && prev.head == head && info.Size() >= prev.size {
		if prev.id != "" || prev.stuck || info.Size() == prev.size {
			return prev.id
		}
		start = prev.complete
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	id, complete, stuck := scanCodexMarker(f)

	codexMarkerPut(path, codexMarkerEntry{info: info, size: info.Size(), modTime: info.ModTime(), head: head, complete: start + complete, id: id, stuck: stuck})
	return id
}

func agyTranscriptCoralSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	lineCount := 0
	for scanner.Scan() {
		lineCount++
		if lineCount > 25 {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if sessionID := agent.ExtractCoralSessionID(entry); sessionID != "" {
			return sessionID
		}
	}
	return ""
}
