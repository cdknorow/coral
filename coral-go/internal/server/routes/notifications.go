package routes

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// NotificationLink is an optional link shown in the notification.
type NotificationLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

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
// after maxAge. A notification no dashboard has received yet waits for the
// next one to connect (StartCursor); one already shown somewhere is not
// replayed to a dashboard that connects later, e.g. after a reload.
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
	return n
}

// StartCursor is the cursor for a newly connected dashboard: it will receive
// every live notification no dashboard has received yet, and all later ones.
func (ns *NotificationStore) StartCursor() int64 {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.prune()
	for _, item := range ns.items {
		if !item.delivered {
			return item.n.ID - 1
		}
	}
	return ns.nextID
}

// Since returns the live notifications after cursor, oldest first, and the
// cursor to pass next time.
func (ns *NotificationStore) Since(cursor int64) ([]Notification, int64) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.prune()
	var out []Notification
	for i := range ns.items {
		if ns.items[i].n.ID > cursor {
			out = append(out, ns.items[i].n)
			ns.items[i].delivered = true
		}
	}
	if ns.nextID > cursor {
		cursor = ns.nextID
	}
	return out, cursor
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Message == "" && body.Title == "" {
		errBadRequest(w, "title or message is required")
		return
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
