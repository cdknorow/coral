package board

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/querypage"
)

// The board message search (#4745): GET /api/board/{project}/messages/search. Like the Team Artifacts query it takes q, since, until,
// sort and cursor, and answers with total and next_cursor (and prev_cursor: it pages both ways). The text and the names are matched in
// Go on the rows the time bounds leave, on what a viewer sees (MessageQuery.Shown), never by SQL, so nothing a caller sends reaches a
// statement or a pattern and % and _ are ordinary characters. The scan is bounded by rows and by text.

const (
	MessageQueryDefaultLimit = 50
	MessageQueryMaxLimit     = 200
	MessageQueryMaxTextLen   = 200
	messageQueryMaxValues    = 50
)

// messageScanRows and messageScanBytes bound one search's scan: the messages read and the text of them. Past either the scan stops and
// the reply says Truncated. Variables so a test can lower them.
var (
	messageScanRows  = 200000
	messageScanBytes = 64 << 20
)

var ErrMessageQuery = errors.New("invalid message query")

func messageQueryErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrMessageQuery, fmt.Sprintf(format, a...))
}

// MessageQueryParams are the raw values of a request, one slice per parameter.
type MessageQueryParams struct {
	Q, From, Mentions, Since, Until, Sort, Limit, Cursor []string
}

// MessageQuery is a validated query. Build it with ParseMessageQuery; the caller sets Project and, for a request through Coral Hub,
// Shown (what a viewer sees of a text: the match is made on it; nil is the text as stored).
type MessageQuery struct {
	Project  string
	Text     string   // lower-cased; empty: no text search
	From     []string // lower-cased sender ids or names
	Mentions string   // lower-cased: messages that tag this member
	Since    string   // created_at >= (UTC RFC 3339)
	Until    string   // created_at <  (UTC RFC 3339)
	Oldest   bool     // sort=oldest; the default is newest first
	Limit    int
	Shown    func(string) string
	cursor   *messageCursor
	finger   string
}

type messageCursor struct {
	Finger string `json:"f"`
	ID     int64  `json:"i"`
	Prev   bool   `json:"p,omitempty"` // the page before ID, not the one after it
}

func (c messageCursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeMessageCursor(s string) (*messageCursor, error) {
	if len(s) > 512 {
		return nil, errors.New("long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var c messageCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.ID <= 0 || c.Finger == "" {
		return nil, errors.New("bad")
	}
	return &c, nil
}

func messageSingle(name string, vals []string) (string, bool, error) {
	switch len(vals) {
	case 0:
		return "", false, nil
	case 1:
		return vals[0], true, nil
	}
	return "", false, messageQueryErr("%s is given more than once", name)
}

func messageText(name string, vals []string, max int) (string, error) {
	v, ok, err := messageSingle(name, vals)
	if err != nil || !ok {
		return "", err
	}
	if querypage.HasControl(v) {
		return "", messageQueryErr("%s is at most %d characters, without control characters", name, max)
	}
	v = strings.TrimSpace(v)
	if len(v) > max {
		return "", messageQueryErr("%s is at most %d characters, without control characters", name, max)
	}
	return strings.ToLower(v), nil
}

func messageTime(name string, vals []string) (string, error) {
	v, ok, err := messageSingle(name, vals)
	if err != nil || !ok {
		return "", err
	}
	t, perr := time.Parse(time.RFC3339, v)
	if perr != nil {
		return "", messageQueryErr("%s must be an RFC 3339 time, for example 2026-10-18T00:00:00Z", name)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// ParseMessageQuery validates the raw parameters; the error names what was wrong.
func ParseMessageQuery(p MessageQueryParams) (*MessageQuery, error) {
	q := &MessageQuery{}
	var err error
	if q.Text, err = messageText("q", p.Q, MessageQueryMaxTextLen); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, v := range p.From {
		if querypage.HasControl(v) {
			return nil, messageQueryErr("from has a value over 200 characters or with control characters")
		}
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, messageQueryErr("from has an empty value")
			}
			if len(part) > 200 {
				return nil, messageQueryErr("from has a value over 200 characters or with control characters")
			}
			if l := strings.ToLower(part); !seen[l] {
				seen[l] = true
				q.From = append(q.From, l)
			}
		}
	}
	if len(q.From) > messageQueryMaxValues {
		return nil, messageQueryErr("from has more than %d values", messageQueryMaxValues)
	}
	if q.Mentions, err = messageText("mentions", p.Mentions, 200); err != nil {
		return nil, err
	}
	if q.Since, err = messageTime("since", p.Since); err != nil {
		return nil, err
	}
	if q.Until, err = messageTime("until", p.Until); err != nil {
		return nil, err
	}
	if v, ok, serr := messageSingle("sort", p.Sort); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "newest":
		case "oldest":
			q.Oldest = true
		default:
			return nil, messageQueryErr("unknown sort %s (newest or oldest)", v)
		}
	}
	q.Limit = MessageQueryDefaultLimit
	if v, ok, serr := messageSingle("limit", p.Limit); serr != nil {
		return nil, serr
	} else if ok {
		n := 0
		if _, scanErr := fmt.Sscanf(v, "%d", &n); scanErr != nil || fmt.Sprint(n) != v || n < 1 || n > MessageQueryMaxLimit {
			return nil, messageQueryErr("limit must be a whole number from 1 to %d", MessageQueryMaxLimit)
		}
		q.Limit = n
	}
	// What a cursor is tied to: every filter and the sort (not the limit, nor the cursor itself).
	fp := sha256.New()
	fmt.Fprintf(fp, "%s|%v|%s|%s|%s|%v", q.Text, q.From, q.Mentions, q.Since, q.Until, q.Oldest)
	q.finger = hex.EncodeToString(fp.Sum(nil))[:16]
	if v, ok, serr := messageSingle("cursor", p.Cursor); serr != nil {
		return nil, serr
	} else if ok {
		c, cerr := decodeMessageCursor(v)
		if cerr != nil {
			return nil, messageQueryErr("cursor is not one this server gave")
		}
		if c.Finger != q.finger {
			return nil, messageQueryErr("cursor was made for other filters or another sort")
		}
		q.cursor = c
	}
	return q, nil
}

// MessageQueryResult is one page, in the query's order. Total is every message that matches, whatever the page; Truncated says the scan
// stopped at its bound, so the total and the pages are those of what it read.
type MessageQueryResult struct {
	Messages   []Message
	Total      int
	NextCursor string // the page after the last message, when there is one
	PrevCursor string // the page before the first message, when there is one
	Truncated  bool
}

func (q *MessageQuery) shown(s string) string {
	if q.Shown == nil || s == "" {
		return s
	}
	return q.Shown(s)
}

// QueryMessages runs q. The messages the time bounds leave are read once, in the query's order (bounded), each is tested, and the
// page is cut out of the matches around the cursor.
func (s *Store) QueryMessages(ctx context.Context, q *MessageQuery) (*MessageQueryResult, error) {
	var mentionID, mentionTitle string
	var numbered map[string]bool
	if q.Mentions != "" {
		mentionID, mentionTitle = q.Mentions, q.Mentions
		if subs, err := s.ListSubscribers(ctx, q.Project); err == nil {
			for _, sub := range subs {
				if strings.EqualFold(sub.SubscriberID, q.Mentions) || strings.EqualFold(sub.JobTitle, q.Mentions) {
					mentionID, mentionTitle = sub.SubscriberID, sub.JobTitle
					break
				}
			}
		}
		numbered = s.numberedNames(ctx, s.reader(), q.Project)
	}
	where := []string{"m.project = ?"}
	args := []any{q.Project}
	if q.Since != "" {
		where = append(where, "m.created_at >= ?")
		args = append(args, q.Since)
	}
	if q.Until != "" {
		where = append(where, "m.created_at < ?")
		args = append(args, q.Until)
	}
	dir := "DESC"
	if q.Oldest {
		dir = "ASC"
	}
	rows, err := s.reader().QueryxContext(ctx, listMessagesSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY m.id "+dir, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pg := &querypage.Pager[Message]{Limit: q.Limit}
	if q.cursor != nil {
		cur := q.cursor.ID
		pg.Prev = q.cursor.Prev
		pg.After = func(m Message) bool { // the message comes after the cursor in the query's order
			if q.Oldest {
				return m.ID > cur
			}
			return m.ID < cur
		}
		pg.AtCursor = func(m Message) bool { return m.ID == cur }
	}
	res := &MessageQueryResult{}
	scannedRows, scannedBytes := 0, 0
	for rows.Next() {
		var m Message
		if err := rows.StructScan(&m); err != nil {
			return nil, err
		}
		if scannedRows++; scannedRows > messageScanRows || scannedBytes+len(m.Content) > messageScanBytes {
			res.Truncated = true
			break
		}
		scannedBytes += len(m.Content)
		if q.matches(m, mentionID, mentionTitle, numbered) {
			pg.Add(m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.Total = pg.Total
	res.Messages = pg.Page
	if res.Messages == nil {
		res.Messages = []Message{}
	}
	if pg.HasNext() {
		res.NextCursor = messageCursor{Finger: q.finger, ID: pg.Page[len(pg.Page)-1].ID}.encode()
	}
	if pg.HasPrev() {
		res.PrevCursor = messageCursor{Finger: q.finger, ID: pg.Page[0].ID, Prev: true}.encode()
	}
	return res, nil
}

// matches reports whether m passes every filter: the sender, the member it tags, and the text, each as a viewer sees it.
func (q *MessageQuery) matches(m Message, mentionID, mentionTitle string, numbered map[string]bool) bool {
	if len(q.From) > 0 {
		id, title := strings.ToLower(q.shown(m.SubscriberID)), strings.ToLower(q.shown(m.JobTitle))
		ok := false
		for _, f := range q.From {
			ok = ok || f == id || f == title
		}
		if !ok {
			return false
		}
	}
	content := m.Content
	if q.Shown != nil {
		content = q.Shown(content)
	}
	if q.Mentions != "" && !containsExplicitTag(content, mentionID, mentionTitle, numbered) {
		return false
	}
	if q.Text != "" && !textHasFold(content, q.Text) {
		return false
	}
	return true
}
