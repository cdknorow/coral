package routes

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/safefs"
)

// A stable key for a chat message (#4510, Save for Later): the same message has the same key after a reload, a poll, another page size and in the History view, so a
// bookmark can find it again. The transcript's lines carry no id the agents share (Claude's have a uuid, Codex's and Gemini's have none), so the key is the first 20 hex
// characters of an HMAC-SHA256, keyed with a secret that belongs to this installation, of what identifies a message: its type, timestamp, text (content or text) and, for
// a tool call or its result, the tool's name and id. The secret matters: through Coral Hub at View the text of a reply is redacted but its key is not, and a plain hash of
// the text would let a viewer test guesses for a redacted secret offline (and ?find_key= would answer them online). Without the secret a key can only be had from a
// reply. Two messages that say the same thing at the same second share a key, and a jump finds the first of them.
func messageKey(secret []byte, m map[string]any) string {
	h := hmac.New(sha256.New, secret)
	str := func(v any) string { s, _ := v.(string); return s }
	for _, k := range []string{"type", "timestamp", "content", "text", "tool_use_id", "tool_name"} {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(str(m[k])))
		h.Write([]byte{0})
	}
	switch tools := m["tool_uses"].(type) {
	case []map[string]any:
		for _, t := range tools {
			h.Write([]byte(str(t["id"]) + "\x00" + str(t["name"]) + "\x00"))
		}
	case []any:
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				h.Write([]byte(str(tm["id"]) + "\x00" + str(tm["name"]) + "\x00"))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

// messageKeySecretFile is the per-install secret's file in the data directory: 32 random bytes in hex, mode 0600, created at the first use and kept (a key must
// survive a restart, or every bookmark would stop finding its message).
const messageKeySecretFile = ".message_key_secret"

var (
	keySecretMu sync.Mutex
	keySecrets  = map[string][]byte{} // data dir -> its secret
)

// messageKeySecretFor is this installation's secret, read from (or created in) its data directory once. When the file can be neither read nor made, a secret that lasts
// for this process is used and the failure is logged: keys are then stable until a restart, and still unguessable.
func messageKeySecretFor(cfg *config.Config) []byte {
	dir := ""
	if cfg != nil {
		dir = cfg.CoralDir()
	}
	keySecretMu.Lock()
	defer keySecretMu.Unlock()
	if s, ok := keySecrets[dir]; ok {
		return s
	}
	s, err := loadOrCreateKeySecret(dir)
	if err != nil {
		slog.Warn("message keys: the per-install secret is not kept; keys last until restart", "dir", logsafe.Path(dir), "error", err)
		s = randomSecret()
	}
	keySecrets[dir] = s
	return s
}

func randomSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("message keys: no randomness: " + err.Error())
	}
	return b
}

func loadOrCreateKeySecret(dir string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("no data directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := safefs.OpenRoot(dir) // the file is reached through the data folder's root: no link out of it, no second name for it
	if err != nil {
		return nil, err
	}
	defer root.Close()
	read := func() ([]byte, error) {
		raw, err := root.ReadFileOneLink(messageKeySecretFile, 1024)
		if err != nil {
			return nil, err
		}
		b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(b) != 32 {
			return nil, errors.New("the message key secret file is not 32 bytes of hex")
		}
		return b, nil
	}
	if b, err := read(); err == nil {
		return b, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	secret := randomSecret()
	f, err := root.CreateNew(messageKeySecretFile, 0o600)
	if errors.Is(err, os.ErrExist) {
		return read() // another handler made it first
	}
	if err != nil {
		return nil, err
	}
	_, werr := f.WriteString(hex.EncodeToString(secret) + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(filepath.Join(dir, messageKeySecretFile))
		return nil, werr
	}
	return secret, nil
}

// withKeys is messages with each one's "key" added. The maps are copies: the reader's own messages are shared between requests and are never written to.
func withKeys(secret []byte, messages []map[string]any) []map[string]any {
	out := make([]map[string]any, len(messages))
	for i, m := range messages {
		c := make(map[string]any, len(m)+1)
		for k, v := range m {
			c[k] = v
		}
		c["key"] = messageKey(secret, m)
		out[i] = c
	}
	return out
}

// findKey is the index of the message with this key in messages, or -1. The scan is from the end: a saved message is usually recent.
func findKey(secret []byte, messages []map[string]any, key string) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if hmac.Equal([]byte(messageKey(secret, messages[i])), []byte(key)) {
			return i
		}
	}
	return -1
}

// writeKeyLookup answers ?find_key=<key> (Save for Later, #4510): whether the transcript still has that message and where, as {found, index, total}.
// The window's messages are searched from the end; when it is not there and the transcript has messages before the window, those are read from the file and the
// last match among them is the answer (#4620), the same message the scan over the whole transcript found.
func writeKeyLookup(w http.ResponseWriter, ctx context.Context, secret []byte, tv *jsonl.Transcript, key string) {
	idx := findKey(secret, tv.Window, key)
	if idx >= 0 {
		idx += tv.Base
	} else if tv.Base > 0 {
		tv.Each(ctx, 0, tv.Base, func(i int, m map[string]any) bool {
			if hmac.Equal([]byte(messageKey(secret, m)), []byte(key)) {
				idx = i
			}
			return true
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": idx >= 0, "index": max(idx, 0), "total": tv.Total})
}

// chatSlice is the messages numbered from..to-1 in the whole transcript: those before the window read from the file, the rest from marked, the window's
// messages as markCoralDeliveries returned them.
func chatSlice(ctx context.Context, tv *jsonl.Transcript, marked []map[string]any, from, to int) []map[string]any {
	from, to = max(from, 0), min(to, tv.Total)
	if from >= to {
		return nil
	}
	var out []map[string]any
	if from < tv.Base {
		out = tv.Slice(ctx, from, min(to, tv.Base))
	}
	if to > tv.Base {
		win := marked[max(from, tv.Base)-tv.Base : to-tv.Base]
		if out == nil {
			return win
		}
		out = append(out, win...)
	}
	return out
}
