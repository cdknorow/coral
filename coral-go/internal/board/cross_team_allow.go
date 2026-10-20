package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/clitext"
)

// Who may message a board (#5508, coral-hub's spec): default deny. A board accepts a cross-team message, and the files that come with it, only from a sender on its list of
// {server, cross_team_id} entries. A cross_team_id is generated for every board row, never reissued and never a name, so a deleted and re-created board of the same name is a NEW
// sender that no entry names.

// CrossTeamIDShape is a generated cross-team board id: "x-" and 16 lower-case base32 characters (80 random bits).
var CrossTeamIDShape = regexp.MustCompile(`^x-[a-z2-7]{16}$`)

// ValidCrossTeamID reports whether s is the shape of a generated cross-team id. A claimed id is checked with this before anything compares it.
func ValidCrossTeamID(s string) bool { return CrossTeamIDShape.MatchString(s) }

// LocalServer is the "server" of an entry for a board on this Coral.
const LocalServer = "local"

// AllowedSender is one entry of a board's list: the Coral the sender is on (LocalServer, or its Coral Hub server id, verified by the hub) and the sender board's cross-team id. An entry
// needs both: a server alone or an id alone is never an entry.
type AllowedSender struct {
	Server      string `json:"server"`
	CrossTeamID string `json:"cross_team_id"`
	// Label is the board's name as the owner saw it when listing it (cleaned, bounded text, a claim for a board on another Coral): shown beside the id so a removed or renamed board is still
	// recognisable. It is never compared: only Server and CrossTeamID admit a sender.
	Label string `json:"label,omitempty"`
}

// MaxAllowedSenders is the longest list a board may have.
const MaxAllowedSenders = 200

// serverIDShape is a Coral Hub server id as the cross-team settings take it (see the server allowlist).
var serverIDShape = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// AllowsSender reports whether the sender (the verified server, and the board id it claims or this Coral read from its own row) is on the list. The match is exact on both; an empty or
// malformed id never matches, and no entry is a wildcard.
func (b BoardSettings) AllowsSender(server, crossTeamID string) bool {
	if server == "" || !ValidCrossTeamID(crossTeamID) {
		return false
	}
	for _, e := range b.CrossTeamAllowed {
		if e.Server == server && e.CrossTeamID == crossTeamID {
			return true
		}
	}
	return false
}

// AllowedProblem checks a whole list for board self (its own cross-team id): at most MaxAllowedSenders entries, each a well-formed server and id, no duplicate, and none for the board itself.
// It returns the cleaned list, or the first problem as (row, field, message).
func AllowedProblem(list []AllowedSender, self string) ([]AllowedSender, int, string, string) {
	if len(list) > MaxAllowedSenders {
		return nil, MaxAllowedSenders, "cross_team_allowed", fmt.Sprintf("at most %d entries", MaxAllowedSenders)
	}
	out := make([]AllowedSender, 0, len(list))
	seen := map[string]bool{}
	for i, e := range list {
		e.Server = strings.ToLower(strings.TrimSpace(e.Server))
		e.CrossTeamID = strings.TrimSpace(e.CrossTeamID)
		e.Label = CleanClaimedLabel(e.Label)
		if e.Server != LocalServer && !serverIDShape.MatchString(e.Server) {
			return nil, i, "server", "must be \"local\" or a Coral Hub server id"
		}
		if !ValidCrossTeamID(e.CrossTeamID) {
			return nil, i, "cross_team_id", "is not a board's cross-team id"
		}
		if e.Server == LocalServer && self != "" && e.CrossTeamID == self {
			return nil, i, "cross_team_id", "a board is not on its own list"
		}
		key := e.Server + "\x00" + e.CrossTeamID
		if seen[key] {
			return nil, i, "cross_team_id", "is listed twice"
		}
		seen[key] = true
		out = append(out, e)
	}
	return out, 0, "", ""
}

const settingCrossTeamAllowed = "cross_team_allowed"

// crossTeamIDExpr is a random "x-" id built inside SQLite: sixteen characters of the base32 alphabet from its own random source (never from a request).
func crossTeamIDExpr() string {
	const alpha = "'abcdefghijklmnopqrstuvwxyz234567'"
	part := "substr(" + alpha + ", 1 + (abs(random()) % 32), 1)"
	parts := make([]string, 16)
	for i := range parts {
		parts[i] = part
	}
	return "'x-' || " + strings.Join(parts, " || ")
}

// ensureCrossTeamIDSchema gives every board row a cross-team id: the column, a backfill of the rows that have none, a trigger that gives each later row one whichever statement inserted it
// (several places insert boards), and a unique index. The id lives and dies with its row: it is never reissued.
func (s *Store) ensureCrossTeamIDSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE boards ADD COLUMN cross_team_id TEXT`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	for _, stmt := range []string{
		`UPDATE boards SET cross_team_id = ` + crossTeamIDExpr() + ` WHERE cross_team_id IS NULL`,
		`CREATE TRIGGER IF NOT EXISTS boards_cross_team_id AFTER INSERT ON boards WHEN NEW.cross_team_id IS NULL
			BEGIN UPDATE boards SET cross_team_id = ` + crossTeamIDExpr() + ` WHERE id = NEW.id; END`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_boards_cross_team_id ON boards(cross_team_id)`,
		`CREATE TABLE IF NOT EXISTS board_settings_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			board_id   TEXT NOT NULL,
			at         TEXT NOT NULL,
			kind       TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			field      TEXT NOT NULL DEFAULT '',
			old_value  TEXT NOT NULL DEFAULT '',
			new_value  TEXT NOT NULL DEFAULT '',
			window     TEXT NOT NULL DEFAULT '',
			count      INTEGER NOT NULL DEFAULT 0,
			senders    TEXT NOT NULL DEFAULT '[]'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_board_settings_log ON board_settings_log(board_id, id)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// CrossTeamID is board key's cross-team id; ok is false when the board has no row or no valid id (such a board matches no entry and is nobody's sender).
func (s *Store) CrossTeamID(ctx context.Context, key string) (string, bool, error) {
	var id sql.NullString
	if err := s.db.GetContext(ctx, &id, `SELECT cross_team_id FROM boards WHERE id = ?`, key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if !id.Valid || !ValidCrossTeamID(id.String) {
		return "", false, nil
	}
	return id.String, true, nil
}

// BoardByCrossTeamID is the board key and label for a cross-team id on this Coral (for the owner's picker and suggestions).
func (s *Store) BoardByCrossTeamID(ctx context.Context, id string) (key, label string, ok bool, err error) {
	if !ValidCrossTeamID(id) {
		return "", "", false, nil
	}
	var row struct {
		ID    string `db:"id"`
		Label string `db:"label"`
	}
	if err := s.db.GetContext(ctx, &row, `SELECT id, label FROM boards WHERE cross_team_id = ?`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return row.ID, row.Label, true, nil
}

// CrossTeamCandidate is a board of this Coral the owner may put on another board's list.
type CrossTeamCandidate struct {
	CrossTeamID string `json:"cross_team_id"`
	Label       string `json:"label"`
	CreatedAt   string `json:"created_at"`
}

// CrossTeamCandidates lists this Coral's boards (never subscribers or content) except except (the board being edited), for the owner's picker. Labels are cleaned like any text another
// party chose.
func (s *Store) CrossTeamCandidates(ctx context.Context, except string) ([]CrossTeamCandidate, error) {
	var rows []struct {
		ID          string         `db:"id"`
		Label       string         `db:"label"`
		CreatedAt   string         `db:"created_at"`
		CrossTeamID sql.NullString `db:"cross_team_id"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT id, label, created_at, cross_team_id FROM boards ORDER BY created_at, id LIMIT 1000`); err != nil {
		return nil, err
	}
	out := []CrossTeamCandidate{}
	for _, r := range rows {
		if r.ID == except || !r.CrossTeamID.Valid || !ValidCrossTeamID(r.CrossTeamID.String) {
			continue
		}
		out = append(out, CrossTeamCandidate{CrossTeamID: r.CrossTeamID.String, Label: CleanClaimedLabel(r.Label), CreatedAt: r.CreatedAt})
	}
	return out, nil
}

// maxClaimedLabel bounds a label another party chose, when it is shown.
const maxClaimedLabel = 80

// CleanClaimedLabel is a board label (or a sender's claimed one) reduced to plain bounded text: no control or format characters, at most maxClaimedLabel runes.
func CleanClaimedLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x200b && r <= 0x200f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2060 && r <= 0x206f) || r == 0xfeff {
			return ' '
		}
		return r
	}, clitext.Clean(s))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxClaimedLabel {
		s = strings.TrimSpace(string(r[:maxClaimedLabel]))
	}
	return s
}

// -- The settings log (#5508) --

const (
	// LogEdit is an owner's change to the list (who, which field, old and new value).
	LogEdit = "edit"
	// LogRefused is the hourly count of sends the list refused (flag on); LogWouldRefuse the same with the flag off ("would refuse", shadow).
	LogRefused     = "refused"
	LogWouldRefuse = "would_refuse"
	// LogEditRefused is the hourly count of refused edits.
	LogEditRefused = "edit_refused"

	maxLogRows       = 500
	maxLoggedSenders = 20 // distinct senders named in one hourly row; the rest are only counted ("others")
)

// LogEntry is one row of a board's settings log. Senders are ids only: never content, never labels.
type LogEntry struct {
	ID      int64           `json:"id"`
	At      string          `json:"at"`
	Kind    string          `json:"kind"`
	Actor   string          `json:"actor,omitempty"`
	Field   string          `json:"field,omitempty"`
	Old     string          `json:"old,omitempty"`
	New     string          `json:"new,omitempty"`
	Window  string          `json:"window,omitempty"`
	Count   int             `json:"count,omitempty"`
	Senders []AllowedSender `json:"senders,omitempty"`
	Others  int             `json:"others,omitempty"`
	// Added and Removed are what an edit changed (computed from its old and new lists): entries with their labels.
	Added   []AllowedSender `json:"added,omitempty"`
	Removed []AllowedSender `json:"removed,omitempty"`
}

// AppendEditLog records a successful edit: who and when, the field, and the old and new values (the list as ids). It keeps the board's last maxLogRows rows.
func (s *Store) AppendEditLog(ctx context.Context, board, actor, field, oldValue, newValue string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO board_settings_log (board_id, at, kind, actor, field, old_value, new_value) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		board, nowUTC(), LogEdit, actor, field, oldValue, newValue); err != nil {
		return err
	}
	return s.pruneLog(ctx, board)
}

func (s *Store) pruneLog(ctx context.Context, board string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM board_settings_log WHERE board_id = ? AND id <= (SELECT id FROM board_settings_log WHERE board_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?)`, board, board, maxLogRows)
	return err
}

// NoteHourly counts one event of an hourly kind (LogRefused, LogWouldRefuse, LogEditRefused) for board: at most ONE row per hour and kind, holding a count and up to maxLoggedSenders distinct
// senders (any more fold into "others" by count alone, so the row is bounded whatever the number of senders). sender is the (server, id) pair, ids only; an empty sender is not listed.
func (s *Store) NoteHourly(ctx context.Context, board, kind string, sender AllowedSender, at time.Time) error {
	window := at.UTC().Truncate(time.Hour).Format(time.RFC3339)
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var row struct {
		ID      int64  `db:"id"`
		Count   int    `db:"count"`
		Senders string `db:"senders"`
	}
	existing := true
	if err := tx.GetContext(ctx, &row, `SELECT id, count, senders FROM board_settings_log WHERE board_id = ? AND kind = ? AND window = ?`, board, kind, window); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		existing = false
	}
	var listed []AllowedSender
	if existing {
		_ = json.Unmarshal([]byte(row.Senders), &listed)
	}
	if sender.Server != "" || sender.CrossTeamID != "" {
		have := false
		for _, l := range listed {
			if l == sender {
				have = true
				break
			}
		}
		if !have && len(listed) < maxLoggedSenders {
			listed = append(listed, sender)
		}
	}
	raw, err := json.Marshal(listed)
	if err != nil {
		return err
	}
	if !existing {
		if _, err := tx.ExecContext(ctx, `INSERT INTO board_settings_log (board_id, at, kind, window, count, senders) VALUES (?, ?, ?, ?, 1, ?)`, board, nowUTC(), kind, window, string(raw)); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE board_settings_log SET count = count + 1, senders = ?, at = ? WHERE id = ?`, string(raw), nowUTC(), row.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.pruneLog(ctx, board)
}

// ListLog is board's settings log, newest first (at most maxLogRows).
func (s *Store) ListLog(ctx context.Context, board string) ([]LogEntry, error) {
	var rows []struct {
		ID      int64  `db:"id"`
		At      string `db:"at"`
		Kind    string `db:"kind"`
		Actor   string `db:"actor"`
		Field   string `db:"field"`
		Old     string `db:"old_value"`
		New     string `db:"new_value"`
		Window  string `db:"window"`
		Count   int    `db:"count"`
		Senders string `db:"senders"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT id, at, kind, actor, field, old_value, new_value, window, count, senders FROM board_settings_log WHERE board_id = ? ORDER BY id DESC LIMIT ?`, board, maxLogRows); err != nil {
		return nil, err
	}
	out := make([]LogEntry, 0, len(rows))
	for _, r := range rows {
		e := LogEntry{ID: r.ID, At: r.At, Kind: r.Kind, Actor: r.Actor, Field: r.Field, Old: r.Old, New: r.New, Window: r.Window, Count: r.Count}
		_ = json.Unmarshal([]byte(r.Senders), &e.Senders)
		if len(e.Senders) > 0 && e.Count > len(e.Senders) {
			e.Others = e.Count - len(e.Senders)
		}
		if e.Kind == LogEdit {
			e.Added, e.Removed = listDiff(e.Old, e.New)
		}
		out = append(out, e)
	}
	return out, nil
}

// SeenSender is a sender a board has received from lately, as its stored messages say: for the owner's suggestions. The label is the sender's own claim (cleaned and bounded); the server id is
// the hub-verified one. A sender seen only before ids existed has no CrossTeamID and cannot be listed until it sends again.
type SeenSender struct {
	Server      string `json:"server"`
	CrossTeamID string `json:"cross_team_id"`
	Label       string `json:"label"`
	LastSeen    string `json:"last_seen"`
	Count       int    `json:"count"`
}

// RecentSenders aggregates the inbound cross-team messages of project since a time, by (server, sender board id), newest first. It reads only the envelope: never the content.
func (s *Store) RecentSenders(ctx context.Context, project string, since time.Time) ([]SeenSender, error) {
	var rows []struct {
		External RawJSON `db:"external_json"`
		At       string  `db:"created_at"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT external_json, created_at FROM board_messages
		WHERE project = ? AND external_json IS NOT NULL AND json_extract(external_json, '$.direction') = 'inbound' AND created_at >= ?
		ORDER BY id DESC LIMIT 5000`, project, since.UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	type key struct{ server, id, label string }
	agg := map[key]*SeenSender{}
	var order []key
	for _, r := range rows {
		var o MessageOrigin
		if json.Unmarshal(r.External, &o) != nil || o.Direction != "inbound" {
			continue
		}
		server, id := o.ServerID, o.FromCrossTeamID
		if server == "" {
			continue
		}
		if !ValidCrossTeamID(id) {
			id = ""
			if server == localServerID { // a local sender's board id is on its own row
				if own, ok, err := s.CrossTeamID(ctx, o.Board); err == nil && ok {
					id = own
				}
			}
		}
		if server == localServerID {
			server = LocalServer
		}
		k := key{server, id, ""}
		if id == "" {
			k.label = o.Board // no id: kept apart per claimed label, and not listable
		}
		e := agg[k]
		if e == nil {
			e = &SeenSender{Server: server, CrossTeamID: id, Label: CleanClaimedLabel(o.Board), LastSeen: r.At}
			agg[k] = e
			order = append(order, k)
		}
		e.Count++
	}
	out := make([]SeenSender, 0, len(order))
	for _, k := range order {
		out = append(out, *agg[k])
	}
	return out, nil
}

// listDiff is what changed between two stored lists (JSON of []AllowedSender): the entries only in the new one, and only in the old one, by server and id.
func listDiff(oldJSON, newJSON string) (added, removed []AllowedSender) {
	var before, after []AllowedSender
	_ = json.Unmarshal([]byte(oldJSON), &before)
	_ = json.Unmarshal([]byte(newJSON), &after)
	in := func(list []AllowedSender, e AllowedSender) bool {
		for _, x := range list {
			if x.Server == e.Server && x.CrossTeamID == e.CrossTeamID {
				return true
			}
		}
		return false
	}
	for _, e := range after {
		if !in(before, e) {
			added = append(added, e)
		}
	}
	for _, e := range before {
		if !in(after, e) {
			removed = append(removed, e)
		}
	}
	return added, removed
}
