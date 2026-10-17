package board

import (
	"context"
	"encoding/json"
	"strings"
)

// HealthSettings is a team's health check configuration (#4175, the operator: "allow the
// operator to configure the health check in the team settings"). A team that never sets it
// gets the defaults (DefaultHealthSettings). Only the Operator changes it, locally (Security's
// #4176 rules 16 and 17: PUT /api/board/{project}/settings); every value is clamped on the
// server (rule 7), and the recipient must be a member of this board (rule 18).
type HealthSettings struct {
	On               bool               `json:"on"`
	IntervalMin      int                `json:"interval_min"` // the check's cadence
	Recipient        string             `json:"recipient"`    // "" = the team's orchestrator
	AllClear         bool               `json:"all_clear"`    // send the one-line check when nothing needs the orchestrator
	DefaultReviewers []string           `json:"default_reviewers"`
	AutoDelegate     HealthRuleOn       `json:"auto_delegate"`
	ClaimNudge       HealthClaimNudge   `json:"claim_nudge"`
	StatusNudge      HealthStatusNudge  `json:"status_nudge"`
	MentionRenudge   HealthMentionNudge `json:"mention_renudge"`
	// HeldNotice tells the Operator about an agent whose automated messages have waited at a
	// dialog for AfterMin (#4175 step 3).
	HeldNotice HealthMentionNudge `json:"held_notice"`
}

// HealthRuleOn is a rule that is only on or off.
type HealthRuleOn struct {
	On bool `json:"on"`
}

// HealthClaimNudge: nudge an idle agent to claim its work after AfterMin, escalate after Nudges.
type HealthClaimNudge struct {
	On       bool `json:"on"`
	AfterMin int  `json:"after_min"`
	Nudges   int  `json:"nudges"`
}

// HealthStatusNudge: ask for a status after AfterMin without activity, escalate EscalateAfterMin later.
type HealthStatusNudge struct {
	On               bool `json:"on"`
	AfterMin         int  `json:"after_min"`
	EscalateAfterMin int  `json:"escalate_after_min"`
}

// HealthMentionNudge: remind the orchestrator of a mention unanswered for AfterMin.
type HealthMentionNudge struct {
	On       bool `json:"on"`
	AfterMin int  `json:"after_min"`
}

// DefaultHealthSettings are the design's values.
func DefaultHealthSettings() HealthSettings {
	return HealthSettings{
		On: true, IntervalMin: 10, AllClear: true,
		AutoDelegate:   HealthRuleOn{On: true},
		ClaimNudge:     HealthClaimNudge{On: true, AfterMin: 10, Nudges: 2},
		StatusNudge:    HealthStatusNudge{On: true, AfterMin: 20, EscalateAfterMin: 10},
		MentionRenudge: HealthMentionNudge{On: true, AfterMin: 10},
		HeldNotice:     HealthMentionNudge{On: true, AfterMin: 10},
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Clamp keeps every value in its range (#4176 rule 7): times at least 5 and at most 1440
// minutes, nudge counts 1 to 5, at most 8 default reviewers.
func (h HealthSettings) Clamp() HealthSettings {
	h.IntervalMin = clampInt(h.IntervalMin, 5, 1440)
	h.ClaimNudge.AfterMin = clampInt(h.ClaimNudge.AfterMin, 5, 1440)
	h.ClaimNudge.Nudges = clampInt(h.ClaimNudge.Nudges, 1, 5)
	h.StatusNudge.AfterMin = clampInt(h.StatusNudge.AfterMin, 5, 1440)
	h.StatusNudge.EscalateAfterMin = clampInt(h.StatusNudge.EscalateAfterMin, 5, 1440)
	h.MentionRenudge.AfterMin = clampInt(h.MentionRenudge.AfterMin, 5, 1440)
	h.HeldNotice.AfterMin = clampInt(h.HeldNotice.AfterMin, 5, 1440)
	h.Recipient = strings.TrimSpace(h.Recipient)
	var revs []string
	for _, r := range h.DefaultReviewers {
		if r = strings.TrimSpace(r); r != "" && len(revs) < 8 {
			revs = append(revs, r)
		}
	}
	h.DefaultReviewers = revs
	return h
}

const settingHealth = "health"

// GetHealthSettings returns a board's health settings, the defaults when none are stored.
func (s *Store) GetHealthSettings(ctx context.Context, project string) HealthSettings {
	h := DefaultHealthSettings()
	if v, err := s.boardSettingValue(ctx, project, settingHealth); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &h)
	}
	return h.Clamp()
}

func (s *Store) setHealthSettings(ctx context.Context, project string, h HealthSettings) error {
	raw, err := json.Marshal(h.Clamp())
	if err != nil {
		return err
	}
	return s.SetBoardSetting(ctx, project, settingHealth, string(raw))
}
