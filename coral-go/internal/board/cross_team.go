package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jmoiron/sqlx"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/clitext"
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

// BoardSettings are per-board switches. The two cross-team switches default to ON for a board that never set them (#5916: they are kill switches; what admits a team from another
// Coral is the explicit Coral Hub team link, #5862), and an owner's explicit off stays off. The rest default to off.
type BoardSettings struct {
	// CrossTeamMessaging lets this board's Orchestrators (and the Operator)
	// message other boards' Orchestrators, and receive their messages. Both
	// boards must enable it.
	CrossTeamMessaging bool `json:"cross_team_messaging"`
	// CrossTeamServers are the other Corals (Coral Hub server ids) whose Orchestrators may
	// message this board through Coral Hub (#3489). Empty: none. Needs CrossTeamMessaging.
	CrossTeamServers []string `json:"cross_team_servers"`
	// CrossTeamArtifacts lets this board send and receive artifacts with its cross-team messages
	// (coral-board send --attach, #4379). Off by default; only the Operator on this computer may
	// turn it on (never through Coral Hub). Needs CrossTeamMessaging. Received files are not
	// scanned, and artifact bytes are not redacted.
	CrossTeamArtifacts bool `json:"cross_team_artifacts"`
	// CrossTeamAllowed is who may message this board, by stable id (#5508): the only thing that admits a sender once the allowlist is in force. Empty: nobody.
	CrossTeamAllowed []AllowedSender `json:"cross_team_allowed"`
	// Health is the team's health check configuration (#4175).
	Health HealthSettings `json:"health"`
	// MessagingStored and ArtifactsStored say that the owner set the switch (to either value), as against the default.
	MessagingStored bool `json:"-"`
	ArtifactsStored bool `json:"-"`
}

// LocalMessagingOn is whether boards of THIS Coral may message this one: an explicit opt-in only. The default-on of the switches is for the relay, where the Coral Hub team link is the
// grant; two teams on one computer have no link to hold, so they are not opened to each other by a default (#5916).
func (b BoardSettings) LocalMessagingOn() bool { return b.MessagingStored && b.CrossTeamMessaging }

// LocalFilesOn is LocalMessagingOn for files between boards of this Coral.
func (b BoardSettings) LocalFilesOn() bool { return b.ArtifactsStored && b.CrossTeamArtifacts }

// BoardSettingsUpdate changes only the settings that are set.
type BoardSettingsUpdate struct {
	CrossTeamMessaging *bool     `json:"cross_team_messaging"`
	CrossTeamServers   *[]string `json:"cross_team_servers"`
	CrossTeamArtifacts *bool     `json:"cross_team_artifacts"`
	// CrossTeamAllowed replaces the whole list (#5508); the caller has checked it (AllowedProblem).
	CrossTeamAllowed *[]AllowedSender `json:"cross_team_allowed"`
	// Health replaces the whole health configuration (clamped; the handler checks the recipient).
	Health *HealthSettings `json:"health"`
}

const (
	settingCrossTeamMessaging = "cross_team_messaging"
	settingCrossTeamServers   = "cross_team_servers"
	settingCrossTeamArtifacts = "cross_team_artifacts"
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

// settingPaused marks a board the Operator (or a team sleep) paused (#3668).
const settingPaused = "paused"

// SetBoardPaused stores whether a board is paused.
func (s *Store) SetBoardPaused(ctx context.Context, project string, paused bool) error {
	if !paused {
		_, err := s.db.ExecContext(ctx, `DELETE FROM board_settings WHERE project = ? AND key = ?`, project, settingPaused)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_settings (project, key, value) VALUES (?, ?, 'true')
		 ON CONFLICT (project, key) DO UPDATE SET value = excluded.value`, project, settingPaused)
	return err
}

// PausedBoards lists the boards stored as paused.
func (s *Store) PausedBoards(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.SelectContext(ctx, &out, `SELECT project FROM board_settings WHERE key = ? AND value = 'true'`, settingPaused)
	return out, err
}

// GetBoardSettings returns a board's settings (defaults when none are stored).
func (s *Store) GetBoardSettings(ctx context.Context, project string) (BoardSettings, error) {
	var rows []struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}
	out := BoardSettings{CrossTeamMessaging: true, CrossTeamArtifacts: true} // the defaults (#5916): a stored value, either way, replaces them
	if err := s.db.SelectContext(ctx, &rows, `SELECT key, value FROM board_settings WHERE project = ?`, project); err != nil {
		return BoardSettings{}, err
	}
	for _, r := range rows {
		switch r.Key {
		case settingCrossTeamMessaging:
			out.CrossTeamMessaging, out.MessagingStored = r.Value == "true", true
		case settingCrossTeamServers:
			_ = json.Unmarshal([]byte(r.Value), &out.CrossTeamServers)
		case settingCrossTeamArtifacts:
			out.CrossTeamArtifacts, out.ArtifactsStored = r.Value == "true", true
		case settingCrossTeamAllowed:
			_ = json.Unmarshal([]byte(r.Value), &out.CrossTeamAllowed)
		}
	}
	out.Health = s.GetHealthSettings(ctx, project)
	return out, nil
}

// UpdateBoardSettings applies u and returns the resulting settings.
func (s *Store) UpdateBoardSettings(ctx context.Context, project string, u BoardSettingsUpdate) (BoardSettings, error) {
	// One transaction: an update that sets several settings (the cross-team switches and the health settings) is applied whole or not at all, and a reader never sees half of
	// it. Before, each was its own statement, so a failure after the first left the board with some of the change and an error for the caller (#5318).
	upsert := func(tx *sqlx.Tx, key, value string) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO board_settings (project, key, value) VALUES (?, ?, ?)
			 ON CONFLICT (project, key) DO UPDATE SET value = excluded.value`, project, key, value)
		return err
	}
	var servers, health, allowed string
	if u.CrossTeamServers != nil {
		list := *u.CrossTeamServers
		if list == nil {
			list = []string{}
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return BoardSettings{}, err
		}
		servers = string(raw)
	}
	if u.CrossTeamAllowed != nil {
		list := *u.CrossTeamAllowed
		if list == nil {
			list = []AllowedSender{}
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return BoardSettings{}, err
		}
		allowed = string(raw)
	}
	if u.Health != nil {
		raw, err := json.Marshal(u.Health.Clamp())
		if err != nil {
			return BoardSettings{}, err
		}
		health = string(raw)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return BoardSettings{}, err
	}
	defer tx.Rollback()
	if u.CrossTeamMessaging != nil {
		if err := upsert(tx, settingCrossTeamMessaging, fmt.Sprint(*u.CrossTeamMessaging)); err != nil {
			return BoardSettings{}, err
		}
	}
	if u.CrossTeamArtifacts != nil {
		if err := upsert(tx, settingCrossTeamArtifacts, fmt.Sprint(*u.CrossTeamArtifacts)); err != nil {
			return BoardSettings{}, err
		}
	}
	if u.CrossTeamServers != nil {
		if err := upsert(tx, settingCrossTeamServers, servers); err != nil {
			return BoardSettings{}, err
		}
	}
	if u.CrossTeamAllowed != nil {
		if err := upsert(tx, settingCrossTeamAllowed, allowed); err != nil {
			return BoardSettings{}, err
		}
	}
	if u.Health != nil {
		if err := upsert(tx, settingHealth, health); err != nil {
			return BoardSettings{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return BoardSettings{}, err
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
	// FromCrossTeamID is the sender board's cross-team id (#5508): read from this Coral's own row for a local sender, the sending Coral's validated claim for a relayed one. Ids only; it is what
	// the owner's suggestions are computed from.
	FromCrossTeamID string `json:"from_cross_team_id,omitempty"`
	// Attachments are the files stored on this computer for an inbound message, in the order the sender attached them (#5506); left out when there are none.
	Attachments []MessageAttachment `json:"attachments,omitempty"`
}

// MessageAttachment is one file that came with a cross-team message and is stored here: its artifact id (the 64 hex digits), its name and media type, and its size.
type MessageAttachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type,omitempty"`
	Size      int64  `json:"size"`
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
	p = p.cleaned()
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
	p = p.cleaned()
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
	content = DefuseMentions(clitext.Clean(content), names) // cleaned first: no control character can hide a mention from the defusing
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
	m, err := s.GetMessageByID(ctx, project, id)
	if errors.Is(err, sql.ErrNoRows) {
		return MessageOrigin{}, false, nil
	}
	if err != nil || m == nil || len(m.External) == 0 {
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

// cleaned is p with no terminal control in its text or mentions (#3580). It runs before
// DefuseMentions, so a control character can't hide a mention from the defusing and then be
// removed.
func (p CrossTeamPost) cleaned() CrossTeamPost {
	p.Content = clitext.Clean(p.Content)
	mentions := make([]string, len(p.Mentions))
	for i, m := range p.Mentions {
		mentions[i] = clitext.Clean(m)
	}
	p.Mentions = mentions
	return p
}

// maxMessageAttachments bounds what one message's envelope lists.
const maxMessageAttachments = 16

// SetInboundAttachments records, in the envelope of cross-team message id on board, the files that came with it and are stored here (#5506), in the order given. It changes nothing but the
// envelope's "attachments" (an empty list removes the key), and only on an inbound message; any other message is left as it is.
func (s *Store) SetInboundAttachments(ctx context.Context, project string, id int64, atts []MessageAttachment) error {
	if len(atts) > maxMessageAttachments {
		atts = atts[:maxMessageAttachments]
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw RawJSON
	if err := tx.GetContext(ctx, &raw, `SELECT external_json FROM board_messages WHERE id = ? AND project = ? AND external_json IS NOT NULL`, id, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	var direction string
	_ = json.Unmarshal(env["direction"], &direction)
	if direction != "inbound" {
		return nil
	}
	if len(atts) == 0 {
		delete(env, "attachments")
	} else {
		b, err := json.Marshal(atts)
		if err != nil {
			return err
		}
		env["attachments"] = b
	}
	out, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE board_messages SET external_json = ? WHERE id = ? AND project = ?`, string(out), id, project); err != nil {
		return err
	}
	return tx.Commit()
}
