package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	if !objectNameRe.MatchString(rec.ID) { // an id read from a file or a message is a 64-digit hex name before it is joined onto a folder (#5305)
		return errors.New("not an artifact id")
	}
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
	if err != nil || json.Unmarshal(data, &p) != nil || !objectNameRe.MatchString(p.Rec.ID) || p.Source == "" || p.RelayID == "" {
		return pendingReceipt{}, false // the id is a name joined onto the objects folder: a pending record that names anything else is not one (#5305)
	}
	return p, true
}

// sweepPending removes this folder's pending records that are unreadable or whose stored object is gone. They
// are this feature's own bookkeeping; the stored files are never touched, and an expired record stays (#4441).
func (h *BoardHandler) sweepPending(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		p, ok := readPending(path)
		// An expired receipt stays: it is the memory of a stored file no board lists, which counts toward the received-files cap and which the
		// Operator can delete in Settings (#4441). Only a record that is unreadable, or whose object is gone, is removed.
		if _, err := os.Lstat(filepath.Join(h.coralDir, "artifacts", "objects", p.Rec.ID)); !ok || err != nil {
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
		if first, have := h.crossTeamRecord("received", target, p.Rec.ID); have {
			h.noteAlsoReceived(first, p.Rec)
			_ = os.Remove(path)
			continue
		}
		if err := h.writeCrossTeamRecord("received", p.Rec); err != nil {
			slog.Warn("cross-team artifact: could not record the received row", "board", target, "id", p.Rec.ID, "error", err)
			continue
		}
		_ = os.Remove(path)
	}
}

// maxAlsoReceipts bounds the later receipts kept on one row.
const maxAlsoReceipts = 20

// noteAlsoReceived adds a later receipt of the same bytes from another server or board to the row, once
// per (server, board). The first receipt's fields are never changed.
func (h *BoardHandler) noteAlsoReceived(first, later crossTeamArtifactRecord) {
	h.alsoMu.Lock()
	defer h.alsoMu.Unlock()
	// Read again under the lock: a row may have gained an entry since it was read.
	if cur, ok := h.crossTeamRecord("received", first.Board, first.ID); ok {
		first = cur
	}
	if first.Server == later.Server && first.OtherBoard == later.OtherBoard || len(first.Also) >= maxAlsoReceipts {
		return
	}
	for _, a := range first.Also {
		if a.Server == later.Server && a.OtherBoard == later.OtherBoard {
			return
		}
	}
	first.Also = append(first.Also, crossTeamAlso{Server: later.Server, OtherBoard: later.OtherBoard, MessageID: later.MessageID, At: later.At})
	dir := h.boardRecordDir("received", first.Board)
	data, err := json.Marshal(first)
	if dir == "" || err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".rec-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if tmp.Close() != nil {
		return
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, first.ID+".json")); err != nil {
		slog.Warn("cross-team artifact: could not record a later receipt", "board", first.Board, "id", first.ID, "error", err)
	}
}
