package board

import (
	"context"
	"time"
)

// TeamLink is one link Coral Hub keeps between two teams, as its owner made it (#5572): the team on another Coral (SourceServer, SourceTeam) may message TargetTeam, a team on THIS computer.
// The labels are display text from the hub (cleaned, never trusted for anything).
type TeamLink struct {
	SourceServer      string `db:"source_server" json:"source_server"`
	SourceTeam        string `db:"source_team" json:"source_team"`
	TargetTeam        string `db:"target_team" json:"target_team"`
	SourceLabel       string `db:"source_label" json:"source_label"`
	TargetLabel       string `db:"target_label" json:"target_label"`
	SourceServerLabel string `db:"source_server_label" json:"source_server_label"`
	AppliedAt         string `db:"applied_at" json:"applied_at"`
}

func (s *Store) ensureTeamLinksSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cross_team_links (
		source_server       TEXT NOT NULL,
		source_team         TEXT NOT NULL,
		target_team         TEXT NOT NULL,
		source_label        TEXT NOT NULL DEFAULT '',
		target_label        TEXT NOT NULL DEFAULT '',
		source_server_label TEXT NOT NULL DEFAULT '',
		applied_at          TEXT NOT NULL,
		PRIMARY KEY (target_team, source_server, source_team)
	)`)
	return err
}

// ReplaceTeamLinks makes links the whole stored list, in one transaction. It is kept FOR THE PANEL: the door never reads it (the in-memory table the connector's snapshots fill decides, with its
// own clock, so a stored row can never make a link fresh after a restart; #5572, Security's condition 3).
func (s *Store) ReplaceTeamLinks(ctx context.Context, links []TeamLink, at time.Time) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM cross_team_links`); err != nil {
		return err
	}
	stamp := at.UTC().Format(time.RFC3339)
	for _, l := range links {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO cross_team_links (source_server, source_team, target_team, source_label, target_label, source_server_label, applied_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			l.SourceServer, l.SourceTeam, l.TargetTeam, l.SourceLabel, l.TargetLabel, l.SourceServerLabel, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TeamLinksTo lists the stored links whose target is team, newest applied first then by source.
func (s *Store) TeamLinksTo(ctx context.Context, team string) ([]TeamLink, error) {
	out := []TeamLink{}
	err := s.reader().SelectContext(ctx, &out,
		`SELECT source_server, source_team, target_team, source_label, target_label, source_server_label, applied_at FROM cross_team_links WHERE target_team = ? ORDER BY source_server, source_team`, team)
	return out, err
}
