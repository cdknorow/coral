// Package jsonl provides incremental JSONL reading for live agent session transcripts.
package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/transcriptlink"
)

// SessionReader incrementally reads JSONL session files for live chat display.
type SessionReader struct {
	mu           sync.Mutex
	cache        map[string]*sessionCache
	firstPrompts map[string]*firstPromptCache
}

type sessionCache struct {
	mu           sync.Mutex
	path         string
	pathVerified bool
	lastResolved time.Time
	offset       int64
	base         int               // the number of messages before messages[0] that were read and let go (#4620)
	messages     []map[string]any  // the newest messages: at most windowMaxMessages or windowMaxBytes of them
	sizes        []int             // approxSize of each message in messages
	msgBytes     int               // their sum
	toolUseNames map[string]string // tool_use_id → tool_name, at most toolNamesMax of them
	toolSeen     map[string]uint32 // the chunk each tool name was first seen in (boundToolNames)
	chunkSeq     uint32
	pending      pendingCalls // the tool calls the checkpoints carry (#5023)
	ckpts        []checkpoint // where in the file the messages before the window can be re-read from (transcript_window.go)
	// The transcript as last read: a different file, a shorter one, or other first bytes mean
	// it was replaced or rewritten, and the messages are read again from the start (#3710).
	info        os.FileInfo
	head        string
	codexState  codexTurnCache
	agyState    agyTurnCache
	claudeState claudeTurnCache
}

// claudeTurnCache tracks the latest end of a turn in a Claude transcript (#3481).
type claudeTurnCache struct {
	transcriptReadState
	event   string
	at      time.Time
	version string         // the newest line's "version": the running Claude Code's own (#3573)
	model   string         // the newest assistant entry's model (#4065)
	effort  string         // the newest assistant entry's effort: the thinking level that turn ran at (#4969)
	turnAt  time.Time      // when that assistant entry was written (#6225)
	apiErr  ClaudeAPIError // the latest message, when it is an API error (#3830)
}

type codexTurnCache struct {
	transcriptReadState
	event  string
	at     time.Time
	active bool
	model  string // the newest turn_context's model (#4065)
	effort string // the newest turn_context's effort: the reasoning level that turn ran at (#4969)
}

type agyTurnCache struct {
	transcriptReadState
	event   string
	at      time.Time
	summary string
	apiErr  string
}

// firstPromptCache is deliberately separate from sessionCache. The live
// sessions list needs one small identity string, not a pinned copy of every
// parsed transcript message.
type firstPromptCache struct {
	info         os.FileInfo
	head         string
	mu           sync.Mutex
	path         string
	lastResolved time.Time
	offset       int64
	prompt       string
}

// NewSessionReader creates a new JSONL session reader.
func NewSessionReader() *SessionReader {
	return &SessionReader{
		cache:        make(map[string]*sessionCache),
		firstPrompts: make(map[string]*firstPromptCache),
	}
}

func (r *SessionReader) getOrCreateSessionCache(sessionID string) *sessionCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cache[sessionID]
	if c == nil {
		c = &sessionCache{toolUseNames: make(map[string]string)}
		r.cache[sessionID] = c
	}
	return c
}

func (r *SessionReader) getOrCreateFirstPromptCache(sessionID string) *firstPromptCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.firstPrompts[sessionID]
	if c == nil {
		c = &firstPromptCache{}
		r.firstPrompts[sessionID] = c
	}
	return c
}

// ReadNewMessages reads new messages since the last call for the given session.
// Returns (new_messages, total_count).
func (r *SessionReader) ReadNewMessages(sessionID, workingDirectory, agentType string) ([]map[string]any, int) {
	return r.readNewMessages(sessionID, workingDirectory, agentType, false)
}

// ReadNewMessagesForLive reads a live session with strict Coral identity
// matching for Antigravity transcripts. Native history callers should use
// ReadNewMessages, which preserves conversation-ID lookup semantics.
func (r *SessionReader) ReadNewMessagesForLive(sessionID, workingDirectory, agentType string) ([]map[string]any, int) {
	return r.readNewMessages(sessionID, workingDirectory, agentType, true)
}

func (r *SessionReader) readNewMessages(sessionID, workingDirectory, agentType string, strictLive bool) ([]map[string]any, int) {
	c := r.getOrCreateSessionCache(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	return r.readNewMessagesLocked(context.Background(), c, sessionID, workingDirectory, agentType, strictLive)
}

func (r *SessionReader) readNewMessagesLocked(ctx context.Context, c *sessionCache, sessionID, workingDirectory, agentType string, strictLive bool) ([]map[string]any, int) {
	return r.readLocked(ctx, c, sessionID, workingDirectory, agentType, strictLive, true)
}

// readLocked reads what the transcript has gained since the last read into the cache, a chunk of
// at most readChunkMax at a time (#4620), and returns the messages it parsed when collect is set
// (a caller of ReadNewMessages wants them; the live chat reads the cache's window instead) and the
// number of messages read in all.
func (r *SessionReader) readLocked(ctx context.Context, c *sessionCache, sessionID, workingDirectory, agentType string, strictLive, collect bool) ([]map[string]any, int) {
	if agentType == at.Codex {
		refreshCodexLink(c, sessionID)
	}
	// Resolve path on first call
	if c.path == "" {
		c.path = resolveTranscriptPathMode(sessionID, workingDirectory, agentType, strictLive)
		if c.path == "" {
			return nil, 0
		}
	}
	// A live Coral session must never inherit a transcript selected only by
	// workspace. Older readers could cache that fallback while a new
	// Antigravity transcript was still being created. Revalidate any existing
	// unverified path and discard its parsed messages before retrying resolution.
	if strictLive && isAgyAgentType(agentType) && !c.pathVerified {
		if agyTranscriptCoralSessionID(c.path) != sessionID {
			c.path = ""
			c.resetMessages()
			c.path = resolveTranscriptPathMode(sessionID, workingDirectory, agentType, true)
			if c.path == "" {
				return nil, 0
			}
		}
		c.pathVerified = true
	}

	// Read new data from file
	f, err := openTranscript(c.path, agentType)
	if err != nil {
		return nil, c.total()
	}
	defer f.Close()
	if agentType == at.Codex && agent.IsCodexGuardianRollout(f) {
		c.resetMessages()
		return nil, 0
	}
	size := int64(0) // the file's size when it was read, the hint for the buffer of its chunks
	if info, err := f.Stat(); err == nil {
		size = info.Size()
		head := transcriptHead(f)
		if transcriptReplaced(c.info, info, c.head, head) {
			c.resetMessages()
		}
		c.info, c.head = info, head
	}

	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return nil, c.total()
	}
	// Each chunk is decoded in parallel (the cost of a long transcript read cold, after a restart
	// or an eviction), at most two such decodes at once in the process (#3715); turning entries
	// into messages stays in order, since a tool result is named from the tool call before it
	// (#3710). A wait that ctx ends consumes nothing of its chunk: the next read starts there.
	p := &entryParser{agentType: agentType, names: c.toolUseNames}
	if n := len(c.messages); n > 0 {
		p.last = c.messages[n-1]
	}
	var collected []map[string]any
	for {
		chunk, eof, err := nextChunk(sizedFile{f, size - c.offset}, readChunkMax)
		if err != nil {
			return collected, c.total()
		}
		data := completeLines(chunk, eof)
		if len(data) == 0 {
			break
		}
		entries, ends, err := decodeLimitedEnds(ctx, data, true)
		if err != nil {
			return collected, c.total()
		}
		// The chunk is parsed in runs of about checkpointSpacing bytes, a checkpoint at the start of each
		// (#5023): they were one per chunk, so an old page was read and decoded from 8 to 16 MiB before it.
		start, from := c.offset, 0
		c.offset += int64(len(data))
		for lo := 0; lo < len(entries); {
			hi := lo + 1
			for hi < len(entries) && int64(ends[hi-1]-from) < checkpointSpacing() {
				hi++
			}
			c.noteCheckpoint(start+int64(from), p.last)
			parsed := p.parse(entries[lo:hi])
			c.pending.noteResults(parsed)
			c.addMessages(parsed)
			if collect {
				collected = append(collected, parsed...)
			}
			from, lo = ends[hi-1], hi
		}
		c.boundToolNames()
		if eof {
			break
		}
		if _, err := f.Seek(c.offset, io.SeekStart); err != nil { // past the partial line the chunk ended in
			return collected, c.total()
		}
	}
	return collected, c.total()
}

// ReadAllMessagesContext reads any new data from the file and returns ALL the messages (not just
// the new ones), for a client that needs the full conversation history. The messages before the
// session's window are read from the file again, one chunk at a time, into the returned list
// (#4620): a caller that can work from the window should use ReadTranscriptContext. Its wait for a
// large decode's turn ends with ctx (then the messages already cached are returned).
func (r *SessionReader) ReadAllMessagesContext(ctx context.Context, sessionID, workingDirectory, agentType string) ([]map[string]any, int) {
	t := r.ReadTranscriptContext(ctx, sessionID, workingDirectory, agentType)
	return t.All(ctx), t.Total
}

// ReadAllMessagesForLiveContext is ReadAllMessagesContext's strict counterpart used by the live
// chat API. Its wait for a large decode's turn ends with ctx.
func (r *SessionReader) ReadAllMessagesForLiveContext(ctx context.Context, sessionID, workingDirectory, agentType string) ([]map[string]any, int) {
	t := r.ReadTranscriptForLiveContext(ctx, sessionID, workingDirectory, agentType)
	return t.All(ctx), t.Total
}

// ReadAllMessagesForLiveWithLineageContext reads the exact live transcript and, when it has an
// explicit persisted ancestor, prepends that ancestor's transcript. Resume/restart can leave the
// provider writing the native rollout under the ancestor Coral marker until the replacement
// session emits its first marker. The caller supplies only trusted resume_from_id values; this
// never falls back to newest-workspace or arbitrary transcript discovery. ctx bounds the decode
// wait.
func (r *SessionReader) ReadAllMessagesForLiveWithLineageContext(ctx context.Context, sessionID, workingDirectory, agentType string, ancestorIDs ...string) ([]map[string]any, int) {
	t := r.ReadTranscriptForLiveWithLineageContext(ctx, sessionID, workingDirectory, agentType, ancestorIDs...)
	return t.All(ctx), t.Total
}

// ReadTranscriptContext reads any new data from the file and returns the session's transcript as
// the reader holds it: its newest messages, and the count of all of them (Transcript). Its wait for
// a large decode's turn ends with ctx (then what is cached is returned).
func (r *SessionReader) ReadTranscriptContext(ctx context.Context, sessionID, workingDirectory, agentType string) *Transcript {
	return r.readTranscript(ctx, sessionID, workingDirectory, agentType, false)
}

// ReadTranscriptForLiveContext is ReadTranscriptContext's strict counterpart for the live chat.
func (r *SessionReader) ReadTranscriptForLiveContext(ctx context.Context, sessionID, workingDirectory, agentType string) *Transcript {
	return r.readTranscript(ctx, sessionID, workingDirectory, agentType, true)
}

func (r *SessionReader) readTranscript(ctx context.Context, sessionID, workingDirectory, agentType string, strictLive bool) *Transcript {
	c := r.getOrCreateSessionCache(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	r.readLocked(ctx, c, sessionID, workingDirectory, agentType, strictLive, false)
	return transcriptOf(c, agentType)
}

// ReadTranscriptForLiveWithLineageContext is ReadAllMessagesForLiveWithLineageContext for a
// Transcript: the first ancestor with messages comes before the session's own in its numbering.
func (r *SessionReader) ReadTranscriptForLiveWithLineageContext(ctx context.Context, sessionID, workingDirectory, agentType string, ancestorIDs ...string) *Transcript {
	current := r.ReadTranscriptForLiveContext(ctx, sessionID, workingDirectory, agentType)
	for _, ancestorID := range ancestorIDs {
		if ancestorID == "" || ancestorID == sessionID {
			continue
		}
		ancestor := r.ReadTranscriptForLiveContext(ctx, ancestorID, workingDirectory, agentType)
		if ancestor.Total == 0 {
			continue
		}
		return current.withAncestor(ancestor)
	}
	return current
}

// FirstUserPrompt returns the earliest non-empty user message in a session.
// Messages are read through the normal parser so system-injected user content
// is excluded consistently with the chat transcript.
func (r *SessionReader) FirstUserPrompt(sessionID, workingDirectory, agentType string) string {
	if agentType == at.Terminal {
		return ""
	}

	c := r.getOrCreateFirstPromptCache(sessionID)

	c.mu.Lock()
	defer c.mu.Unlock()
	if agentType == at.Codex {
		if link := transcriptlink.Codex(sessionID); link.Path != "" && link.Path != c.path {
			c.path, c.prompt, c.head = link.Path, "", ""
			c.offset, c.info = 0, nil
		}
	}

	if c.path == "" {
		if c.lastResolved.IsZero() || time.Since(c.lastResolved) >= 2*time.Second {
			c.lastResolved = time.Now()
			// A live session's first prompt comes from its own transcript: for agy, the one
			// carrying its marker, never the newest in its folder (#4590).
			c.path = resolveTranscriptPathMode(sessionID, workingDirectory, agentType, isAgyAgentType(agentType))
		}
		if c.path == "" {
			return ""
		}
	}

	info, err := os.Stat(c.path)
	if err != nil {
		c.path, c.prompt, c.head = "", "", ""
		c.offset, c.info = 0, nil
		return ""
	}
	if sameTranscriptSnapshot(c.info, info) {
		return c.prompt
	}
	f, err := openTranscript(c.path, agentType)
	if err != nil {
		return ""
	}
	defer f.Close()
	if agentType == at.Codex && agent.IsCodexGuardianRollout(f) {
		c.prompt = ""
		return ""
	}
	info, err = f.Stat()
	if err != nil {
		return ""
	}
	head := transcriptHead(f)
	if transcriptReplaced(c.info, info, c.head, head) {
		c.offset, c.prompt = 0, ""
	}
	c.info, c.head = info, head
	if c.prompt != "" {
		return c.prompt
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return ""
	}
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 {
			return ""
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			c.offset += int64(len(line))
			if readErr != nil {
				return ""
			}
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			// A final record may still be in the middle of being written. Leave
			// the offset at its start so the next call retries the completed line.
			if readErr == io.EOF && line[len(line)-1] != '\n' {
				return ""
			}
			c.offset += int64(len(line))
			if readErr != nil {
				return ""
			}
			continue
		}
		c.offset += int64(len(line))
		for _, message := range parseTranscriptEntry(entry, map[string]string{}, agentType) {
			if message["type"] != "user" || message["local_command"] == true { // a slash command the CLI ran is not what the session was asked to do (#5488)
				continue
			}
			content, _ := message["content"].(string)
			if content = strings.TrimSpace(content); content != "" {
				c.prompt = content
				return c.prompt
			}
		}
		if readErr != nil {
			return ""
		}
	}
}

// CachedFirstUserPrompt returns display data without opening a sleeping transcript.
func (r *SessionReader) CachedFirstUserPrompt(sessionID string) string {
	r.mu.Lock()
	c := r.firstPrompts[sessionID]
	r.mu.Unlock()
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prompt
}

// SessionPath is the transcript file this reader last read for the session ("" when it has read none): the file the messages it returned came from (#5802).
func (r *SessionReader) SessionPath(sessionID string) string {
	r.mu.Lock()
	c := r.cache[sessionID]
	r.mu.Unlock()
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path
}

// ClearSession removes cached state for a session.
func (r *SessionReader) ClearSession(sessionID string) {
	r.mu.Lock()
	delete(r.cache, sessionID)
	delete(r.firstPrompts, sessionID)
	r.mu.Unlock()
}

// ReadFromContext parses the complete lines of a transcript file from offset on, and returns the
// offset after the last complete line, so a line still being written is read on the next call. An
// offset past the end of the file (it was truncated or replaced) starts over from the top. Its
// wait for a large decode's turn (the decodelimit slots) ends with ctx.
func ReadFromContext(ctx context.Context, path, agentType string, offset int64) ([]map[string]any, int64, error) {
	f, err := openTranscript(path, agentType)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if agentType == at.Codex && agent.IsCodexGuardianRollout(f) {
		return nil, info.Size(), nil
	}
	if offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil, offset, nil
	}
	messages, err := parseLines(ctx, data[:end], agentType)
	if err != nil {
		return nil, offset, err
	}
	return messages, offset + int64(end) + 1, nil
}
