package routes

import (
	"context"
	"encoding/json"
	setreg "github.com/subgentic/coral/internal/settings"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The cap on RECEIVED files (#4441, Security's #4438 SHOULD 2): the daily quotas let a linked Coral leave up to 500 MiB a day on this computer,
// and nothing limited how much stayed. Past the cap new received files are refused as store_full; local uploads are never affected, and nothing
// is ever deleted for it (user data is not removed automatically). The Operator sees the total in Settings > Storage and can raise or clear it.
const (
	ReceivedArtifactsMaxBytesSetting       = "received_artifacts_max_bytes" // whole bytes; unset is the default, 0 is no cap
	ReceivedCapNoticeAckSetting            = "received_cap_notice_ack"      // the cap (in bytes) the Operator has been told about at 80%
	defaultReceivedMaxBytes          int64 = 2 << 30
)

// receivedNoticeNumerator/Denominator: the one-time notice shows at 80% of the cap.
const receivedNoticeNumerator, receivedNoticeDenominator = 4, 5

// receivedObject is a received file no board lists: its receipt is still waiting for the message that names it, or that message never came.
type receivedObject struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	At     string `json:"at"`
	Source string `json:"source"`
}

// receivedUnlistedItems bounds the files Settings lists (and one delete names): the count and size are of all of them.
const receivedUnlistedItems = 50

// receivedUsage totals the objects this Coral holds because another Coral or board sent them: each distinct object once, however many boards
// received it, at the size its receipt recorded. It counts what a board LISTS (a received row) and what is still pending or was never claimed
// (#4440: a file's receipt waits for its message, and one whose message never comes stays stored and unlisted), so the cap covers both. The
// unlisted ones, oldest first, are those with no received row anywhere, no agent's upload of the same bytes, and only expired pending
// receipts; they are what the Operator may delete in Settings. A record that does not parse, whose name is not its id, or whose object is
// gone counts for nothing (as every reader treats it).
func receivedUsage(coralDir string, now time.Time) (bytes int64, objects int, ids map[string]bool, unlisted []receivedObject) {
	ids = map[string]bool{}
	if coralDir == "" {
		return 0, 0, ids, nil
	}
	listed := map[string]bool{}
	root := filepath.Join(coralDir, "artifacts", "received")
	boards, _ := os.ReadDir(root)
	for _, b := range boards {
		if !b.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, b.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || len(name) != 64+len(".json") || name[64:] != ".json" || managedArtifactID("coral://artifacts/"+name[:64]) != name[:64] {
				continue
			}
			path := filepath.Join(root, b.Name(), name)
			st, err := os.Lstat(path)
			if err != nil || !st.Mode().IsRegular() || st.Size() > maxArtifactMetaBytes {
				continue
			}
			data, err := os.ReadFile(path)
			var rec crossTeamArtifactRecord
			if err != nil || json.Unmarshal(data, &rec) != nil || rec.ID != name[:64] || rec.Size < 0 {
				continue
			}
			listed[rec.ID] = true
			if !ids[rec.ID] {
				ids[rec.ID] = true
				bytes += rec.Size
				objects++
			}
		}
	}
	// Pending receipts: counted once each (when the object is still stored); unlisted when every receipt of it has expired.
	type waiting struct {
		obj     receivedObject
		expired bool
	}
	pend := map[string]*waiting{}
	for _, path := range pendingRecordPaths(coralDir, "") {
		p, ok := readPending(path)
		if !ok || managedArtifactID("coral://artifacts/"+p.Rec.ID) != p.Rec.ID || p.Rec.Size < 0 {
			continue
		}
		if st, err := os.Lstat(filepath.Join(coralDir, "artifacts", "objects", p.Rec.ID)); err != nil || !st.Mode().IsRegular() {
			continue
		}
		at, err := time.Parse(time.RFC3339, p.Rec.At)
		expired := err != nil || now.Sub(at) > receivedPendingFor
		if w := pend[p.Rec.ID]; w != nil {
			w.expired = w.expired && expired
			continue
		}
		pend[p.Rec.ID] = &waiting{obj: receivedObject{ID: p.Rec.ID, Size: p.Rec.Size, At: p.Rec.At, Source: p.Source}, expired: expired}
		if !ids[p.Rec.ID] {
			ids[p.Rec.ID] = true
			bytes += p.Rec.Size
			objects++
		}
	}
	for id, w := range pend {
		if w.expired && !listed[id] && !hasUploadRecord(coralDir, id) {
			unlisted = append(unlisted, w.obj)
		}
	}
	sort.Slice(unlisted, func(i, j int) bool {
		if unlisted[i].At != unlisted[j].At {
			return unlisted[i].At < unlisted[j].At
		}
		return unlisted[i].ID < unlisted[j].ID
	})
	return bytes, objects, ids, unlisted
}

// pendingRecordPaths lists the pending receipts' files: every board's, or only those of one object (id "").
func pendingRecordPaths(coralDir, id string) []string {
	root := filepath.Join(coralDir, "artifacts", "pending")
	boards, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, b := range boards {
		if !b.IsDir() || !boardDirName.MatchString(b.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, b.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			name := f.Name()
			if f.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
				continue
			}
			if id != "" && !strings.HasSuffix(name, "-"+id+".json") {
				continue
			}
			out = append(out, filepath.Join(root, b.Name(), name))
		}
	}
	return out
}

// hasUploadRecord reports whether an agent on this Coral uploaded these bytes (the same object may be both received and uploaded here).
func hasUploadRecord(coralDir, id string) bool {
	if !objectNameRe.MatchString(id) { // an id read from a file or a message is a 64-digit hex name before it is joined onto a folder (#5305)
		return false
	}
	sessions, err := os.ReadDir(filepath.Join(coralDir, "artifacts", "uploads"))
	if err != nil {
		return false
	}
	for _, s := range sessions {
		if up := uploadsDir(coralDir, s.Name()); up != "" && s.IsDir() {
			if _, err := os.Lstat(filepath.Join(up, id+".json")); err == nil {
				return true
			}
		}
	}
	return false
}

// receivedFits reports whether a file of n bytes with this id may be received: an object already received (listed or waiting for its message)
// adds nothing, and with no cap (0) everything fits. The check is made before the bytes are stored; two receipts at the same moment can pass
// it together, so the cap is soft by at most the files in flight (the receiver admits few at a time).
func receivedFits(coralDir, id string, n int64, now time.Time) bool {
	max := storeFor(coralDir).receivedCap()
	if max <= 0 {
		return true
	}
	used, _, ids, _ := receivedUsage(coralDir, now)
	return ids[id] || used+n <= max
}

// receivedCap is the cap in bytes (0: none).
func (s *artifactStore) receivedCap() int64 {
	s.mu.Lock()
	f := s.receivedMaxBytes
	s.mu.Unlock()
	if f == nil {
		return defaultReceivedMaxBytes
	}
	return f()
}

// receivedArtifactsMaxBytes reads the setting: unset (or anything unreadable) is the default 2 GiB, never "no cap", and 0 is no cap.
func (h *SessionsHandler) receivedArtifactsMaxBytes() int64 {
	if h.ss == nil {
		return defaultReceivedMaxBytes
	}
	s, err := h.ss.GetSettings(context.Background())
	if err != nil {
		return defaultReceivedMaxBytes
	}
	v, ok := setreg.Raw(s, ReceivedArtifactsMaxBytesSetting), setreg.Has(s, ReceivedArtifactsMaxBytesSetting)
	if !ok || v == "" {
		return defaultReceivedMaxBytes
	}
	n, valid := parseStoreCap(v)
	if !valid {
		slog.Warn("received_artifacts_max_bytes is not a whole number of bytes; the default applies", "value", v)
		return defaultReceivedMaxBytes
	}
	return n
}

// receivedStatus is what GET /api/system/artifact-store reports for Settings: the total, the cap and whether the one-time notice is due (at 80%
// of a cap the Operator has not yet been told about). The unlisted files' digests (items) go to the Operator only: a digest is enough to read the
// file through GET /api/artifacts/{id}, and an unlisted file is one whose message never came to any board here (#4489, Security's #4488); everyone else
// gets the count and size.
func (h *SessionsHandler) receivedStatus(withItems bool) map[string]any {
	dir := h.cfg.CoralDir()
	used, objects, _, unlisted := receivedUsage(dir, time.Now())
	max := h.receivedArtifactsMaxBytes()
	notice := false
	if max > 0 && used*receivedNoticeDenominator >= max*receivedNoticeNumerator && h.ss != nil {
		if s, err := h.ss.GetSettings(context.Background()); err == nil && setreg.Raw(s, ReceivedCapNoticeAckSetting) != strconv.FormatInt(max, 10) {
			notice = true
		}
	}
	var unlistedBytes int64
	for _, u := range unlisted {
		unlistedBytes += u.Size
	}
	items := unlisted
	if len(items) > receivedUnlistedItems {
		items = items[:receivedUnlistedItems]
	}
	if items == nil || !withItems {
		items = []receivedObject{}
	}
	return map[string]any{"bytes": used, "objects": objects, "max_bytes": max, "default_bytes": defaultReceivedMaxBytes, "notice": notice,
		"unlisted": map[string]any{"count": len(unlisted), "bytes": unlistedBytes, "items": items}}
}
