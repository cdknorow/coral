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

// DefaultHealthSettings are the design's values. The check is OFF for a team that has not turned it on (#4962, the operator: it
// uses tokens each time and a user may not know what it does); the rules and numbers below are what it uses once it is on.
func DefaultHealthSettings() HealthSettings {
	return HealthSettings{
		On: false, IntervalMin: 10, AllClear: true,
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

// HealthOnByProject is, for each board that stores health settings, whether its check is on. A board with none is not in the map:
// it has the default (DefaultHealthSettings().On). A read that fails is an error and not an empty map: an empty map reads as every team
// having the default (off), and the live list's rows flipped a team's icon off for the tick (#5119).
func (s *Store) HealthOnByProject(ctx context.Context) (map[string]bool, error) {
	out := map[string]bool{}
	var rows []struct {
		Project string `db:"project"`
		Value   string `db:"value"`
	}
	if err := s.reader().SelectContext(ctx, &rows, `SELECT project, value FROM board_settings WHERE key = ?`, settingHealth); err != nil {
		return nil, err
	}
	for _, r := range rows {
		h := DefaultHealthSettings()
		if json.Unmarshal([]byte(r.Value), &h) == nil {
			out[r.Project] = h.On
		}
	}
	return out, nil
}

// SetHealthOn turns a board's health check on or off, leaving every other health setting as it is stored (or the defaults).
func (s *Store) SetHealthOn(ctx context.Context, project string, on bool) error {
	h := s.GetHealthSettings(ctx, project)
	h.On = on
	return s.setHealthSettings(ctx, project, h)
}

const healthDefaultOffMarker = "migration:health_default_off"

// migrateHealthDefaultOff keeps the teams that were running the health check when its default became OFF (#4962): a team that never
// stored a health setting had the check on by default, so the first time this runs it stores "on" for every board that exists and has
// none. It runs once (a marker row in board_settings under the project "~store"); a board made later has the new default, off.
func (s *Store) migrateHealthDefaultOff(ctx context.Context) error {
	var done []string
	if err := s.db.SelectContext(ctx, &done, `SELECT value FROM board_settings WHERE project = '~store' AND key = ?`, healthDefaultOffMarker); err != nil {
		return err
	}
	if len(done) > 0 {
		return nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO board_settings (project, key, value)
		SELECT p.project, ?, '{"on":true}' FROM (
			SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
			UNION SELECT DISTINCT project FROM board_messages
		) p WHERE p.project <> ''`, settingHealth); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO board_settings (project, key, value) VALUES ('~store', ?, 'done')`, healthDefaultOffMarker); err != nil {
		return err
	}
	return tx.Commit()
}
