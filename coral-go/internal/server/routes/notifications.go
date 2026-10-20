package routes

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// NotificationLink is an optional link shown in the notification.
type NotificationLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// A notification's link is written by an agent (coral-agent notify), so it may only go where the
// dashboard allows (#3622 F2, the same rule as utils.js notificationLinkTarget): docs://<name>,
// an http(s) address with a host and no credentials (opened in a new tab), or a root-relative
// dashboard path (the dashboard checks it against its router). javascript:, data:, file:,
// //host, \ and control characters, and /api/... are refused.
const (
	maxNotificationLinkLabel = 200
	maxNotificationLinkURL   = 2048
)

var docsLinkRE = regexp.MustCompile(`^docs://[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func checkNotificationLink(l *NotificationLink) error {
	if utf8.RuneCountInString(l.Label) > maxNotificationLinkLabel {
		return errors.New("link.label is too long")
	}
	u := strings.TrimSpace(l.URL)
	switch {
	case u == "":
		return errors.New("link.url is required")
	case len(u) > maxNotificationLinkURL:
		return errors.New("link.url is too long")
	case strings.ContainsAny(u, "\\") || strings.IndexFunc(u, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return errors.New("link.url has characters a link can't have")
	case docsLinkRE.MatchString(u):
		return nil
	}
	if lower := strings.ToLower(u); strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		p, err := url.Parse(u)
		if err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Hostname() != "" && p.User == nil {
			return nil
		}
		return errors.New("link.url is not a valid web address")
	}
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") && u != "/api" && !strings.HasPrefix(u, "/api/") {
		return nil
	}
	return errors.New("link.url must be docs://<name>, an http(s) address, or a dashboard path")
}

// What a notification may hold, and how many the store keeps (#4602).
const (
	maxNotificationTitle   = 200
	maxNotificationMessage = 4096
	maxNotificationItems   = 200
)

// Notification represents a UI notification pushed via the API.
type Notification struct {
	ID        int64             `json:"id"`
	Title     string            `json:"title"`
	Message   string            `json:"message"`
	Type      string            `json:"type"`  // "toast" (default) or "alert" (centered modal, requires OK click)
	Level     string            `json:"level"` // "info", "success", "warning", "error"
	Link      *NotificationLink `json:"link,omitempty"`
	CreatedAt string            `json:"created_at"`
}

// NotificationStore holds UI notifications for the /ws/coral connections.
// Every connected dashboard receives each notification: a connection reads
// past its own cursor (Since) rather than removing items, and items expire
// after maxAge. A notification counts as delivered only once some connection
// has written it successfully (Ack). A dashboard that connects later receives
// exactly the live notifications that were never delivered (Connect), so a
// failed write does not lose one and a reload does not replay one.
type NotificationStore struct {
	mu     sync.Mutex
	items  []storedNotification // ascending ID
	nextID int64
	maxAge time.Duration
	now    func() time.Time
}

type storedNotification struct {
	n         Notification
	at        time.Time
	delivered bool // sent to at least one connection
}

// NewNotificationStore creates a new notification store.
func NewNotificationStore() *NotificationStore {
	return &NotificationStore{
		maxAge: 5 * time.Minute,
		now:    time.Now,
	}
}

// Push adds a notification for every connected dashboard.
func (ns *NotificationStore) Push(n Notification) Notification {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.prune()
	ns.nextID++
	n.ID = ns.nextID
	now := ns.clock()
	n.CreatedAt = now.UTC().Format(time.RFC3339)
	if n.Type == "" {
		n.Type = "toast"
	}
	if n.Level == "" {
		n.Level = "info"
	}
	ns.items = append(ns.items, storedNotification{n: n, at: now})
	if over := len(ns.items) - maxNotificationItems; over > 0 {
		ns.items = append([]storedNotification(nil), ns.items[over:]...) // the oldest go first (#4602)
	}
	return n
}

// Connect starts a dashboard connection: backlog is every live notification
// no dashboard has received yet (oldest first), and cursor is where Since
// continues from.
func (ns *NotificationStore) Connect() (backlog []Notification, cursor int64) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.prune()
	for _, item := range ns.items {
		if !item.delivered {
			backlog = append(backlog, item.n)
		}
	}
	return backlog, ns.nextID
}

// Since returns the live notifications after cursor, oldest first, and the
// cursor to pass next time. It does not mark them delivered; call Ack once
// they have been written.
func (ns *NotificationStore) Since(cursor int64) ([]Notification, int64) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.prune()
	var out []Notification
	for _, item := range ns.items {
		if item.n.ID > cursor {
			out = append(out, item.n)
		}
	}
	if ns.nextID > cursor {
		cursor = ns.nextID
	}
	return out, cursor
}

// Ack marks notifications delivered after a connection wrote them.
func (ns *NotificationStore) Ack(sent []Notification) {
	if len(sent) == 0 {
		return
	}
	ids := make(map[int64]bool, len(sent))
	for _, n := range sent {
		ids[n.ID] = true
	}
	ns.mu.Lock()
	defer ns.mu.Unlock()
	for i := range ns.items {
		if ids[ns.items[i].n.ID] {
			ns.items[i].delivered = true
		}
	}
}

func (ns *NotificationStore) clock() time.Time {
	if ns.now == nil {
		return time.Now()
	}
	return ns.now()
}

// prune drops expired notifications. Callers hold ns.mu.
func (ns *NotificationStore) prune() {
	cutoff := ns.clock().Add(-ns.maxAge)
	i := 0
	for i < len(ns.items) && !ns.items[i].at.After(cutoff) {
		i++
	}
	if i > 0 {
		ns.items = append(ns.items[:0], ns.items[i:]...)
	}
}

// NotificationHandler handles the POST /api/notifications endpoint.
type NotificationHandler struct {
	store *NotificationStore
}

// NewNotificationHandler creates a new handler with the given store.
func NewNotificationHandler(store *NotificationStore) *NotificationHandler {
	return &NotificationHandler{store: store}
}

// Create handles POST /api/notifications.
// Accepts {"title": "...", "message": "...", "level": "info|success|warning|error"}
func (h *NotificationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title   string            `json:"title"`
		Message string            `json:"message"`
		Type    string            `json:"type"`
		Level   string            `json:"level"`
		Link    *NotificationLink `json:"link,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Message == "" && body.Title == "" {
		errBadRequest(w, "title or message is required")
		return
	}
	// A toast is a line or two: bounded here, where a client or an agent chooses the text (#4602; the body cap alone was 8 MiB, held
	// in memory for 5 minutes and sent to every open dashboard).
	if len(body.Title) > maxNotificationTitle || len(body.Message) > maxNotificationMessage {
		errBadRequest(w, fmt.Sprintf("a notification's title is at most %d bytes and its message at most %d", maxNotificationTitle, maxNotificationMessage))
		return
	}
	// An agent (coral-agent notify, with its token) or the Operator; no anonymous
	// local process puts a toast or an alert on every open dashboard (#3495).
	if _, ok := callerSession(w, r, "", true, nil); !ok {
		return
	}
	if body.Link != nil {
		if body.Link.Label == "" && body.Link.URL == "" {
			body.Link = nil
		} else if err := checkNotificationLink(body.Link); err != nil {
			errBadRequest(w, err.Error())
			return
		} else {
			body.Link.URL = strings.TrimSpace(body.Link.URL)
		}
	}

	n := h.store.Push(Notification{
		Title:   body.Title,
		Message: body.Message,
		Type:    body.Type,
		Level:   body.Level,
		Link:    body.Link,
	})
	writeJSON(w, http.StatusOK, n)
}
