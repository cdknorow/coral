package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RawJSON is a JSON document stored as TEXT. It marshals as the document
// itself and is omitted from JSON when the column is NULL.
type RawJSON []byte

// Scan implements sql.Scanner.
func (r *RawJSON) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*r = nil
	case string:
		*r = RawJSON(x)
	case []byte:
		*r = append(RawJSON(nil), x...)
	default:
		return fmt.Errorf("RawJSON: cannot scan %T", v)
	}
	return nil
}

// MarshalJSON implements json.Marshaler.
func (r RawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// ── Board settings ───────────────────────────────────────────────────

// BoardSettings are per-board switches. Every setting defaults to off.
type BoardSettings struct {
	// CrossTeamMessaging lets this board's Orchestrators (and the Operator)
	// message other boards' Orchestrators, and receive their messages. Both
	// boards must enable it.
	CrossTeamMessaging bool `json:"cross_team_messaging"`
	// CrossTeamServers are the other Corals (Coral Hub server ids) whose Orchestrators may
	// message this board through Coral Hub (#3489). Empty: none. Needs CrossTeamMessaging.
	CrossTeamServers []string `json:"cross_team_servers"`
}

// BoardSettingsUpdate changes only the settings that are set.
type BoardSettingsUpdate struct {
	CrossTeamMessaging *bool     `json:"cross_team_messaging"`
	CrossTeamServers   *[]string `json:"cross_team_servers"`
}

const (
	settingCrossTeamMessaging = "cross_team_messaging"
	settingCrossTeamServers   = "cross_team_servers"
)

// AllowsServer reports whether a relayed message from server may reach this board.
func (b BoardSettings) AllowsServer(server string) bool {
	for _, s := range b.CrossTeamServers {
		if strings.EqualFold(s, server) {
			return true
		}
	}
	return false
}

func (s *Store) ensureBoardSettingsSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS board_settings (
		project TEXT NOT NULL,
		key     TEXT NOT NULL,
		value   TEXT NOT NULL,
		PRIMARY KEY (project, key)
	)`); err != nil {
		return err
	}
	// Relayed cross-team messages already delivered, by their sender's message id, so a
	// retry through Coral Hub (after a hub restart too) is never delivered twice (#3489).
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS board_relayed_messages (
		source_server     TEXT NOT NULL,
		message_id        TEXT NOT NULL,
		target_message_id INTEGER NOT NULL,
		created_at        TEXT NOT NULL,
		PRIMARY KEY (source_server, message_id)
	)`)
	return err
}

// GetBoardSettings returns a board's settings (defaults when none are stored).
func (s *Store) GetBoardSettings(ctx context.Context, project string) (BoardSettings, error) {
	var rows []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	var out BoardSettings
	if err := s.db.SelectContext(ctx, &rows, `SELECT key, value FROM board_settings WHERE project = ?`, project); err != nil {
		return out, err
	}
	for _, r := range rows {
		switch r.Key {
		case settingCrossTeamMessaging:
			out.CrossTeamMessaging = r.Value == "true"
		case settingCrossTeamServers:
			_ = json.Unmarshal([]byte(r.Value), &out.CrossTeamServers)
		}
	}
	return out, nil
}

// UpdateBoardSettings applies u and returns the resulting settings.
func (s *Store) UpdateBoardSettings(ctx context.Context, project string, u BoardSettingsUpdate) (BoardSettings, error) {
	if u.CrossTeamMessaging != nil {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO board_settings (project, key, value) VALUES (?, ?, ?)
			 ON CONFLICT (project, key) DO UPDATE SET value = excluded.value`,
			project, settingCrossTeamMessaging, fmt.Sprint(*u.CrossTeamMessaging)); err != nil {
			return BoardSettings{}, err
		}
	}
	if u.CrossTeamServers != nil {
		list := *u.CrossTeamServers
		if list == nil {
			list = []string{}
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return BoardSettings{}, err
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO board_settings (project, key, value) VALUES (?, ?, ?)
			 ON CONFLICT (project, key) DO UPDATE SET value = excluded.value`,
			project, settingCrossTeamServers, string(raw)); err != nil {
			return BoardSettings{}, err
		}
	}
	return s.GetBoardSettings(ctx, project)
}

// ── Cross-team messages ──────────────────────────────────────────────

// MessageOrigin is the envelope stored on both copies of a cross-team
// message. Server ids are carried even on a single server so that coral-hub
// can route by them later.
type MessageOrigin struct {
	Direction  string `json:"direction"` // "inbound" on the target board, "outbound" on the sender's mirror
	ServerID   string `json:"server_id"`
	Board      string `json:"board"`
	Sender     string `json:"sender"`
	SenderRole string `json:"sender_role"`
	// The receiving side of the message.
	TargetServerID  string `json:"target_server_id"`
	TargetBoard     string `json:"target_board"`
	TargetMessageID int64  `json:"target_message_id,omitempty"` // set on the outbound mirror
	// A message relayed by Coral Hub from another Coral (#3489): the sender's message id
	// (the de-duplication key) and how many relayed replies led to it.
	RelayMessageID string `json:"relay_message_id,omitempty"`
	Hops           int    `json:"hops,omitempty"`
}

// remote reports whether the message came from another Coral.
func (o MessageOrigin) remote() bool {
	return o.ServerID != "" && o.ServerID != localServerID
}

// localServerID is store.LocalServerID (not imported here: the board package doesn't
// depend on the sessions store).
const localServerID = "local"

// CrossTeamPost is a validated cross-team message, ready to store.
type CrossTeamPost struct {
	Origin   MessageOrigin // Direction and TargetMessageID are filled in
	Mentions []string      // subscriber ids on the target board to @mention
	Content  string
}

// ErrNoTargetOrchestrator means the target board has nobody to deliver to.
var ErrNoTargetOrchestrator = errors.New("the target board has no active Orchestrator")

// TargetOrchestrators returns the active Orchestrator subscriber ids of a
// board, or, when to is set, that subscriber if it is an active Orchestrator
// there. Cross-team messages only ever address Orchestrators.
func (s *Store) TargetOrchestrators(ctx context.Context, project, to string) ([]string, error) {
	subs, err := s.ListSubscribers(ctx, project)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sub := range subs {
		if sub.IsActive == 0 || !strings.EqualFold(strings.TrimSpace(sub.JobTitle), "Orchestrator") {
			continue
		}
		if to != "" && !strings.EqualFold(sub.SubscriberID, to) {
			continue
		}
		out = append(out, sub.SubscriberID)
	}
	if len(out) == 0 {
		if to != "" {
			return nil, fmt.Errorf("%q is not an active Orchestrator on board %q", to, project)
		}
		return nil, ErrNoTargetOrchestrator
	}
	return out, nil
}

// SenderLabel is how a cross-team message is shown on the receiving board,
// e.g. "Orchestrator (coral-go-team)".
func SenderLabel(o MessageOrigin) string {
	if o.remote() {
		id := o.ServerID
		if len(id) > 8 {
			id = id[:8]
		}
		return fmt.Sprintf("%s (%s, server %s)", o.SenderRole, o.Board, id)
	}
	return fmt.Sprintf("%s (%s)", o.SenderRole, o.Board)
}

// PostCrossTeamMessage stores the inbound message on the target board (from
// SenderLabel, with the @mentions so the notifier wakes the recipients) and
// the outbound mirror on the sender's board (from the sender, linked to the
// inbound message id), in one transaction.
func (s *Store) PostCrossTeamMessage(ctx context.Context, p CrossTeamPost) (inbound, outbound *Message, err error) {
	in := p.Origin
	in.Direction = "inbound"
	in.TargetMessageID = 0
	inJSON, err := json.Marshal(in)
	if err != nil {
		return nil, nil, err
	}
	var mentions []string
	for _, m := range p.Mentions {
		mentions = append(mentions, "@"+m)
	}
	// Each copy is defused against the board it lands on.
	targetNames, err := s.boardMentionNames(ctx, in.TargetBoard)
	if err != nil {
		return nil, nil, err
	}
	senderNames, err := s.boardMentionNames(ctx, p.Origin.Board)
	if err != nil {
		return nil, nil, err
	}
	inContent := strings.TrimSpace(strings.Join(mentions, " ") + " " + DefuseMentions(p.Content, targetNames))
	mirrorContent := DefuseMentions(p.Content, senderNames)
	from := SenderLabel(in)
	now := nowUTC()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	insert := func(project, subscriber, content string, envelope []byte) (int64, error) {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO board_messages (project, session_id, subscriber_id, content, external_json, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`, project, subscriber, subscriber, content, string(envelope), now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	inID, err := insert(in.TargetBoard, from, inContent, inJSON)
	if err != nil {
		return nil, nil, err
	}
	out := p.Origin
	out.Direction = "outbound"
	out.TargetMessageID = inID
	outJSON, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	outID, err := insert(out.Board, out.Sender, mirrorContent, outJSON)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	inbound = &Message{ID: inID, Project: in.TargetBoard, SubscriberID: from, Content: inContent, CreatedAt: now, JobTitle: from, External: inJSON}
	outbound = &Message{ID: outID, Project: out.Board, SubscriberID: out.Sender, Content: mirrorContent, CreatedAt: now, External: outJSON}
	return inbound, outbound, nil
}

// relayDedupKeep is how long a relayed message id is remembered.
const relayDedupKeep = 30 * 24 * time.Hour

// PostRelayedCrossTeamMessage stores a cross-team message that Coral Hub relayed from
// another Coral (#3489): only the inbound copy (the sender's server keeps the outbound
// mirror), from SenderLabel with the @mentions, and its (source server, message id), in
// one transaction. A message id already delivered from that server is not stored again:
// duplicate is true and inbound is the first delivery's id only.
func (s *Store) PostRelayedCrossTeamMessage(ctx context.Context, p CrossTeamPost) (inbound *Message, duplicate bool, err error) {
	in := p.Origin
	in.Direction = "inbound"
	in.TargetMessageID = 0
	if !in.remote() || in.RelayMessageID == "" {
		return nil, false, errors.New("a relayed message needs its source server and message id")
	}
	inJSON, err := json.Marshal(in)
	if err != nil {
		return nil, false, err
	}
	var mentions []string
	for _, m := range p.Mentions {
		mentions = append(mentions, "@"+m)
	}
	targetNames, err := s.boardMentionNames(ctx, in.TargetBoard)
	if err != nil {
		return nil, false, err
	}
	content := strings.TrimSpace(strings.Join(mentions, " ") + " " + DefuseMentions(p.Content, targetNames))
	from := SenderLabel(in)
	now := nowUTC()

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// Claim the message id FIRST: the write takes SQLite's write lock, so two deliveries
	// of the same message at once (a hub retry racing the first) serialise, and the second
	// sees the claim and answers duplicate instead of failing on the key.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO board_relayed_messages (source_server, message_id, target_message_id, created_at) VALUES (?, ?, 0, ?)
		 ON CONFLICT (source_server, message_id) DO NOTHING`, in.ServerID, in.RelayMessageID, now)
	if err != nil {
		return nil, false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, false, err
	} else if n == 0 {
		var first int64
		if err := tx.GetContext(ctx, &first, `SELECT target_message_id FROM board_relayed_messages WHERE source_server = ? AND message_id = ?`,
			in.ServerID, in.RelayMessageID); err != nil {
			return nil, false, err
		}
		return &Message{ID: first, Project: in.TargetBoard}, true, nil
	}
	res, err = tx.ExecContext(ctx,
		`INSERT INTO board_messages (project, session_id, subscriber_id, content, external_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, in.TargetBoard, from, from, content, string(inJSON), now)
	if err != nil {
		return nil, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE board_relayed_messages SET target_message_id = ? WHERE source_server = ? AND message_id = ?`,
		id, in.ServerID, in.RelayMessageID); err != nil {
		return nil, false, err
	}
	// De-duplication only has to outlast Coral Hub's retries: ids older than 30 days go.
	if _, err := tx.ExecContext(ctx, `DELETE FROM board_relayed_messages WHERE created_at < ?`,
		time.Now().UTC().Add(-relayDedupKeep).Format(time.RFC3339)); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return &Message{ID: id, Project: in.TargetBoard, SubscriberID: from, Content: content, CreatedAt: now, JobTitle: from, External: inJSON}, false, nil
}

// PostRelayMirror stores the sender's copy of a cross-team message this Coral sent to
// another Coral through Coral Hub (#3489): on the sender's board, from the sender, with
// the outbound envelope (the target Coral and board, the target's message id, and the
// relay message id and hops). The target Coral stores the inbound copy.
func (s *Store) PostRelayMirror(ctx context.Context, o MessageOrigin, content string) (*Message, error) {
	o.Direction = "outbound"
	raw, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	names, err := s.boardMentionNames(ctx, o.Board)
	if err != nil {
		return nil, err
	}
	content = DefuseMentions(content, names)
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO board_messages (project, session_id, subscriber_id, content, external_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, o.Board, o.Sender, o.Sender, content, string(raw), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Message{ID: id, Project: o.Board, SubscriberID: o.Sender, Content: content, CreatedAt: now, External: raw}, nil
}

// RelayMirrorID returns the id of the sender's copy already stored on board for a relay
// message id (0 if none), so a retry that Coral Hub answers "duplicate" stores no second copy.
func (s *Store) RelayMirrorID(ctx context.Context, project, relayMessageID string) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id, `SELECT id FROM board_messages
		WHERE project = ? AND external_json IS NOT NULL
		  AND json_extract(external_json, '$.direction') = 'outbound'
		  AND json_extract(external_json, '$.relay_message_id') = ?
		ORDER BY id LIMIT 1`, project, relayMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// RelayedOrigin returns the envelope of message id on board if it is a cross-team
// message received from another Coral (#3489): ok is false otherwise.
func (s *Store) RelayedOrigin(ctx context.Context, project string, id int64) (MessageOrigin, bool, error) {
	m, err := s.GetMessageByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageOrigin{}, false, nil
	}
	if err != nil || m == nil || m.Project != project || len(m.External) == 0 {
		return MessageOrigin{}, false, err
	}
	var o MessageOrigin
	if json.Unmarshal(m.External, &o) != nil || o.Direction != "inbound" || !o.remote() {
		return MessageOrigin{}, false, nil
	}
	return o, true, nil
}

// zeroWidthSpace is invisible when rendered but breaks every mention pattern.
const zeroWidthSpace = "\u200b"

// DefuseMentions makes text unable to mention anyone on a board while it
// reads the same: a cross-team message may only wake the recipients the
// server addresses, never the board's workers or "@all". names are the
// board's subscriber ids and job titles.
//
// Every '@' gets a zero-width space after it, which breaks "@name" and the
// broadcasts. The other mention forms mentionTerms matches, "Name:",
// "Name —" and "Name—", are broken by a zero-width space before the ':' or
// dash, but only where the text before it ends with one of names. Every
// other colon and dash is left byte-identical, so commands ("8080:80"),
// "host:port", times, JSON and URLs survive being copied.
//
// Known gap: someone who subscribes to the board after the message, and is
// named in it in the "Name:" form, can count it as a mention.
func DefuseMentions(text string, names []string) string {
	var b strings.Builder
	b.Grow(len(text) + 16)
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '@':
			b.WriteByte(c)
			b.WriteString(zeroWidthSpace)
			continue
		case c == ':' && endsWithName(text[:i], names):
			b.WriteString(zeroWidthSpace)
		case strings.HasPrefix(text[i:], "—"): // em dash: "Name—" or "Name —"
			before := text[:i]
			if endsWithName(before, names) || (strings.HasSuffix(before, " ") && endsWithName(before[:len(before)-1], names)) {
				b.WriteString(zeroWidthSpace)
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// endsWithName reports whether s ends with one of names (case-insensitive),
// as written or with its spaces removed.
func endsWithName(s string, names []string) bool {
	for _, name := range names {
		for _, n := range []string{strings.TrimSpace(name), strings.ReplaceAll(name, " ", "")} {
			if n != "" && len(s) >= len(n) && strings.EqualFold(s[len(s)-len(n):], n) {
				return true
			}
		}
	}
	return false
}

// boardMentionNames are the names DefuseMentions guards on a board: every
// subscriber's id and job title.
func (s *Store) boardMentionNames(ctx context.Context, project string) ([]string, error) {
	subs, err := s.ListSubscribers(ctx, project)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, 2*len(subs))
	for _, sub := range subs {
		names = append(names, sub.SubscriberID, sub.JobTitle)
	}
	return names, nil
}

// BoardExists reports whether a board has any subscriber or message.
func (s *Store) BoardExists(ctx context.Context, project string) (bool, error) {
	var one int
	err := s.db.GetContext(ctx, &one, `SELECT 1 WHERE EXISTS (SELECT 1 FROM board_subscribers WHERE project = ?)
		OR EXISTS (SELECT 1 FROM board_messages WHERE project = ?)`, project, project)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
