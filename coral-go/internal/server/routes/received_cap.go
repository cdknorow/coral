package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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

// receivedUsage totals the objects this Coral holds because another Coral or board sent them: each distinct object once, however many boards
// received it, at the size its first receipt recorded. A record that does not parse, or whose name is not its id, counts for nothing (as every
// reader treats it).
func receivedUsage(coralDir string) (bytes int64, objects int, ids map[string]bool) {
	ids = map[string]bool{}
	if coralDir == "" {
		return 0, 0, ids
	}
	root := filepath.Join(coralDir, "artifacts", "received")
	boards, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, ids
	}
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
			if err != nil || json.Unmarshal(data, &rec) != nil || rec.ID != name[:64] || rec.Size < 0 || ids[rec.ID] {
				continue
			}
			ids[rec.ID] = true
			bytes += rec.Size
			objects++
		}
	}
	return bytes, objects, ids
}

// receivedFits reports whether a file of n bytes with this id may be received: an object already received here adds nothing, and with no cap
// (0) everything fits. The check is made before the bytes are stored; two receipts at the same moment can pass it together, so the cap is
// soft by at most the files in flight (the receiver admits few at a time).
func receivedFits(coralDir, id string, n int64) bool {
	max := storeFor(coralDir).receivedCap()
	if max <= 0 {
		return true
	}
	used, _, ids := receivedUsage(coralDir)
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
	v, ok := s[ReceivedArtifactsMaxBytesSetting]
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
// of a cap the Operator has not yet been told about).
func (h *SessionsHandler) receivedStatus() map[string]any {
	dir := h.cfg.CoralDir()
	used, objects, _ := receivedUsage(dir)
	max := h.receivedArtifactsMaxBytes()
	notice := false
	if max > 0 && used*receivedNoticeDenominator >= max*receivedNoticeNumerator && h.ss != nil {
		if s, err := h.ss.GetSettings(context.Background()); err == nil && s[ReceivedCapNoticeAckSetting] != strconv.FormatInt(max, 10) {
			notice = true
		}
	}
	return map[string]any{"bytes": used, "objects": objects, "max_bytes": max, "default_bytes": defaultReceivedMaxBytes, "notice": notice}
}
