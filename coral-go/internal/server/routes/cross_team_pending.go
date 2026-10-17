package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A file that arrives from another Coral is held as pending until the message that names it arrives
// (#4440, Security's #4438 SHOULD 3). The sender pushes every file before the message, so a failure
// partway (a quota, a switch turned off, a dropped link, a refused message) used to leave the earlier
// files stored and listed as Received with no message. Now the receipt is a pending record, kept apart
// from the board's Received rows: the message (the same relay message id, from the same source server)
// lists it, and one that no message claims within receivedPendingFor is never listed. Nothing stored is
// deleted: the object stays in the store, only its row is not shown.

const receivedPendingFor = 30 * time.Minute

// pendingReceipt is the record of a file waiting for its message.
type pendingReceipt struct {
	Rec     crossTeamArtifactRecord `json:"rec"`
	Source  string                  `json:"source"`
	RelayID string                  `json:"relay_id"`
}

func pendingKey(src, relayID string) string {
	sum := sha256.Sum256([]byte(src + "\x00" + relayID))
	return hex.EncodeToString(sum[:16])
}

func (h *BoardHandler) pendingNow() time.Time {
	if h.received.now != nil {
		return h.received.now()
	}
	return time.Now()
}

// writePendingReceipt records a receipt of rec.ID for the message relayID from src, replacing an earlier
// receipt of the same file for the same message (a retry), and drops this board's expired ones.
func (h *BoardHandler) writePendingReceipt(src, relayID string, rec crossTeamArtifactRecord) error {
	dir := h.boardRecordDir("pending", rec.Board)
	if dir == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	h.sweepPending(dir)
	data, err := json.Marshal(pendingReceipt{Rec: rec, Source: src, RelayID: relayID})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pend-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, pendingKey(src, relayID)+"-"+rec.ID+".json"))
}

// readPending reads one pending record; ok is false for anything that is not a well-formed one.
func readPending(path string) (pendingReceipt, bool) {
	var p pendingReceipt
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxArtifactMetaBytes {
		return p, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &p) != nil || p.Rec.ID == "" || p.Source == "" || p.RelayID == "" {
		return pendingReceipt{}, false
	}
	return p, true
}

// sweepPending removes this folder's pending records that expired or are unreadable. They are this
// feature's own bookkeeping; the stored files are never touched.
func (h *BoardHandler) sweepPending(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := h.pendingNow()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, ok := readPending(path)
		if at, err := time.Parse(time.RFC3339, p.Rec.At); !ok || err != nil || now.Sub(at) > receivedPendingFor {
			_ = os.Remove(path)
		}
	}
}

// promotePending lists the files waiting for the message relayID from src as Received rows of the board,
// each naming the board's message inboundID, and removes their pending records. A record past its time,
// or whose stored object is gone, is not listed.
func (h *BoardHandler) promotePending(target, src, relayID string, inboundID int64) {
	dir := h.boardRecordDir("pending", target)
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := pendingKey(src, relayID) + "-"
	now := h.pendingNow()
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, ok := readPending(path)
		if !ok || p.Source != src || p.RelayID != relayID || p.Rec.Board != target {
			continue
		}
		at, err := time.Parse(time.RFC3339, p.Rec.At)
		if err != nil || now.Sub(at) > receivedPendingFor {
			continue
		}
		if _, err := os.Lstat(filepath.Join(h.coralDir, "artifacts", "objects", p.Rec.ID)); err != nil {
			continue
		}
		p.Rec.MessageID = inboundID
		if err := h.writeCrossTeamRecord("received", p.Rec); err != nil {
			slog.Warn("cross-team artifact: could not record the received row", "board", target, "id", p.Rec.ID, "error", err)
			continue
		}
		_ = os.Remove(path)
	}
}
