package routes

import (
	"context"
	"encoding/json"
	"fmt"
	setreg "github.com/cdknorow/coral/internal/settings"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Artifact retention (#3554): the same prune, run hourly, only when the Operator turns it on
// (artifact_retention_days, default 0: off). Guards so turning it on can't surprise anyone:
//   - the first run waits until the Operator acknowledged a dry run in Settings, or 24 hours
//     after it was turned on (a notice is logged meanwhile);
//   - each run frees at most retentionRunCap, so a mistaken age can't empty the store at once;
//   - every removal is logged on its own line (id and size only).
const (
	RetentionDaysSetting  = "artifact_retention_days"
	RetentionKindsSetting = "artifact_retention_kinds"
	retentionEnabledAtKey = "artifact_retention_enabled_at" // set by Coral when it sees the value change
	retentionSeenDaysKey  = "artifact_retention_seen_days"
	RetentionAckAtKey     = "artifact_retention_ack_at" // the Operator acknowledged a dry run
	RetentionLastKey      = "artifact_retention_last"   // the last run's summary (JSON)
	retentionRunCap       = 2 << 30
	retentionAckWait      = 24 * time.Hour
	retentionMaxDays      = 3650
)

var (
	retentionFirstDelay = 5 * time.Minute
	retentionInterval   = time.Hour
)

// validRetentionDays: 0 (off) or a whole number of days from 1 to 3650.
func validRetentionDays(v any) (string, bool) {
	var n int64
	switch x := v.(type) {
	case float64:
		if x != float64(int64(x)) {
			return "", false
		}
		n = int64(x)
	case string:
		p, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return "", false
		}
		n = p
	default:
		return "", false
	}
	if n < 0 || n > retentionMaxDays {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

// validRetentionKinds: a comma-separated subset of diffs, objects, diff-objects (or all).
func validRetentionKinds(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	var out []string
	for _, k := range strings.Split(s, ",") {
		switch k = strings.TrimSpace(k); k {
		case pruneKindDiffs, pruneKindObjects, pruneKindDiffObjects, "all":
			out = append(out, k)
		default:
			return "", false
		}
	}
	return strings.Join(out, ","), len(out) > 0
}

func retentionKinds(s string) map[string]bool {
	m := map[string]bool{}
	if strings.TrimSpace(s) == "" {
		s = pruneKindDiffs
	}
	for _, k := range strings.Split(s, ",") {
		switch k = strings.TrimSpace(k); k {
		case "all":
			m[pruneKindDiffs], m[pruneKindObjects], m[pruneKindDiffObjects] = true, true, true
		case pruneKindDiffs, pruneKindObjects, pruneKindDiffObjects:
			m[k] = true
		}
	}
	return m
}

// RunArtifactRetention runs the retention job until ctx ends.
func (h *SessionsHandler) RunArtifactRetention(ctx context.Context) {
	if h.ss == nil {
		return
	}
	t := time.NewTimer(retentionFirstDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h.retentionTick(ctx, time.Now())
		t.Reset(retentionInterval)
	}
}

// retentionTick is one pass: off, waiting for the Operator, or a capped prune.
func (h *SessionsHandler) retentionTick(ctx context.Context, now time.Time) (*pruneReport, string) {
	settings, err := h.ss.GetSettings(ctx)
	if err != nil {
		return nil, "settings unreadable"
	}
	days := int(setreg.Int(settings, RetentionDaysSetting))
	if days <= 0 {
		if setreg.Raw(settings, retentionEnabledAtKey) != "" {
			_ = h.ss.SetSetting(ctx, retentionEnabledAtKey, "")
			_ = h.ss.SetSetting(ctx, retentionSeenDaysKey, "")
		}
		return nil, "off"
	}
	// A new value (turned on, or changed) restarts the wait for the Operator's acknowledgement.
	enabledAt, _ := time.Parse(time.RFC3339, setreg.Raw(settings, retentionEnabledAtKey))
	if setreg.Raw(settings, retentionSeenDaysKey) != strconv.Itoa(days) || enabledAt.IsZero() {
		enabledAt = now
		_ = h.ss.SetSetting(ctx, retentionEnabledAtKey, now.UTC().Format(time.RFC3339))
		_ = h.ss.SetSetting(ctx, retentionSeenDaysKey, strconv.Itoa(days))
	}
	ackAt, _ := time.Parse(time.RFC3339, setreg.Raw(settings, RetentionAckAtKey))
	if ackAt.Before(enabledAt) && now.Sub(enabledAt) < retentionAckWait {
		slog.Info("[artifacts] retention is on but waits until a dry run is acknowledged in Settings, or 24 hours after it was turned on",
			"days", days, "starts_by", enabledAt.Add(retentionAckWait).UTC().Format(time.RFC3339))
		return nil, "waiting"
	}
	rep, err := h.runPrune(ctx, pruneOptions{OlderThan: time.Duration(days) * 24 * time.Hour, Kinds: retentionKinds(setreg.Raw(settings, RetentionKindsSetting)),
		MaxBytes: retentionRunCap, Now: now})
	if err != nil {
		slog.Warn("[artifacts] retention: nothing removed this run", "error", err)
		return nil, "failed"
	}
	last, _ := json.Marshal(map[string]any{"at": now.UTC().Format(time.RFC3339), "freed": rep.Freed, "removed": rep.Removed,
		"skipped": rep.Skipped, "errors": rep.Errors, "capped": rep.Capped})
	_ = h.ss.SetSetting(ctx, RetentionLastKey, string(last))
	slog.Info("[artifacts] retention run", "days", days, "removed", rep.Removed, "freed", rep.Freed, "capped", rep.Capped)
	return rep, "ran"
}

// retentionAckRoute: POST /api/artifacts/retention/ack, the Operator acknowledges the dry run
// Settings showed for the current retention, so the job may start (Operator only).
func (h *SessionsHandler) retentionAckRoute(w http.ResponseWriter, r *http.Request) {
	if refuseNonOperator(w, r, "acknowledge artifact retention") {
		return
	}
	settings, err := h.ss.GetSettings(r.Context())
	if err != nil {
		errInternalServer(w, "could not read settings")
		return
	}
	if days := setreg.Int(settings, RetentionDaysSetting); days <= 0 {
		errBadRequest(w, fmt.Sprintf("%s is off: set it first", RetentionDaysSetting))
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := h.ss.SetSetting(r.Context(), RetentionAckAtKey, now); err != nil {
		errInternalServer(w, "could not save the acknowledgement")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"acknowledged_at": now})
}
