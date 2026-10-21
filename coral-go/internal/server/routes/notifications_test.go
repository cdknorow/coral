package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

func notificationIDs(ns []Notification) []int64 {
	var ids []int64
	for _, n := range ns {
		ids = append(ids, n.ID)
	}
	return ids
}

// deliver is one dashboard tick: read what is new and, when the write
// succeeds, acknowledge it.
func deliver(ns *NotificationStore, cursor int64, writeOK bool) ([]Notification, int64) {
	got, next := ns.Since(cursor)
	if !writeOK {
		return nil, cursor
	}
	ns.Ack(got)
	return got, next
}

func TestNotificationStore_EveryDashboardGetsEachNotification(t *testing.T) {
	ns := NewNotificationStore()
	_, a := ns.Connect()
	_, b := ns.Connect()
	first := ns.Push(Notification{Title: "one"})

	got, a := deliver(ns, a, true)
	assert.Equal(t, []int64{first.ID}, notificationIDs(got))
	got, b = deliver(ns, b, true)
	assert.Equal(t, []int64{first.ID}, notificationIDs(got), "a second dashboard gets it too")

	got, a = deliver(ns, a, true)
	assert.Empty(t, got, "each dashboard gets a notification once")
	second := ns.Push(Notification{Title: "two"})
	got, _ = deliver(ns, b, true)
	assert.Equal(t, []int64{second.ID}, notificationIDs(got))
	got, _ = deliver(ns, a, true)
	assert.Equal(t, []int64{second.ID}, notificationIDs(got))
}

func TestNotificationStore_PendingWaitsForNextDashboardWithoutReplay(t *testing.T) {
	ns := NewNotificationStore()
	early := ns.Push(Notification{Title: "pushed with no dashboard open"})

	backlog, cursor := ns.Connect()
	assert.Equal(t, []int64{early.ID}, notificationIDs(backlog), "queued until a dashboard connects")
	ns.Ack(backlog)
	got, _ := deliver(ns, cursor, true)
	assert.Empty(t, got, "the backlog is not repeated by Since")

	late := ns.Push(Notification{Title: "pushed after delivery"})
	backlog, _ = ns.Connect() // e.g. a page reload or another tab
	assert.Equal(t, []int64{late.ID}, notificationIDs(backlog))
}

// The only dashboard's write fails (socket closing): the notification is not
// lost; the next dashboard still gets it.
func TestNotificationStore_FailedWriteIsNotDelivered(t *testing.T) {
	ns := NewNotificationStore()
	_, only := ns.Connect()
	n := ns.Push(Notification{Title: "build finished"})
	got, _ := deliver(ns, only, false)
	assert.Empty(t, got)

	backlog, _ := ns.Connect()
	assert.Equal(t, []int64{n.ID}, notificationIDs(backlog))
}

// Delivered and undelivered notifications interleave: a reload receives
// exactly the undelivered ones, none of the delivered ones.
func TestNotificationStore_ReloadGetsExactlyUndelivered(t *testing.T) {
	ns := NewNotificationStore()
	n1 := ns.Push(Notification{Title: "1"})
	n2 := ns.Push(Notification{Title: "2"})
	n3 := ns.Push(Notification{Title: "3"})
	n4 := ns.Push(Notification{Title: "4"})
	ns.Ack([]Notification{n1, n3}) // written somewhere; 2 and 4 never were

	backlog, _ := ns.Connect()
	assert.Equal(t, []int64{n2.ID, n4.ID}, notificationIDs(backlog))
}

func TestNotificationStore_Expiry(t *testing.T) {
	now := time.Now()
	ns := NewNotificationStore()
	ns.now = func() time.Time { return now }
	_, cursor := ns.Connect()
	ns.Push(Notification{Title: "old"})
	now = now.Add(6 * time.Minute)
	fresh := ns.Push(Notification{Title: "fresh"})
	got, _ := ns.Since(cursor)
	assert.Equal(t, []int64{fresh.ID}, notificationIDs(got))
	ns.mu.Lock()
	assert.Len(t, ns.items, 1, "expired items are dropped")
	ns.mu.Unlock()
}

// Two real /ws/coral connections both receive a notification pushed once.
func TestWSCoral_NotificationReachesEveryDashboard(t *testing.T) {
	server, handler := setupTestServer(t)
	ns := NewNotificationStore()
	handler.SetNotificationStore(ns)
	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var conns []*websocket.Conn
	for i := 0; i < 2; i++ {
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		require.NoError(t, err)
		defer conn.CloseNow()
		var first map[string]json.RawMessage
		require.NoError(t, wsjson.Read(ctx, conn, &first)) // initial coral_update
		conns = append(conns, conn)
	}

	pushed := ns.Push(Notification{Title: "build finished", Level: "success"})
	for i, conn := range conns {
		found := false
		for !found {
			var msg struct {
				Type          string         `json:"type"`
				Notifications []Notification `json:"notifications"`
			}
			require.NoError(t, wsjson.Read(ctx, conn, &msg), "dashboard %d", i)
			for _, n := range msg.Notifications {
				if n.ID == pushed.ID {
					found = true
				}
			}
		}
	}
}

// A hub feed (?feed=hub) gets no notifications and does not consume them: a
// notification pushed while only the feed is connected still reaches the next
// dashboard.
func TestWSCoral_HubFeedDoesNotConsumeNotifications(t *testing.T) {
	server, handler := setupTestServer(t)
	ns := NewNotificationStore()
	handler.SetNotificationStore(ns)
	base := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	feed, _, err := websocket.Dial(ctx, base+"?feed=hub", nil)
	require.NoError(t, err)
	defer feed.CloseNow()
	var first map[string]json.RawMessage
	require.NoError(t, wsjson.Read(ctx, feed, &first))
	pushed := ns.Push(Notification{Title: "for the people"})

	// Let the feed tick many times; it must never carry the notification.
	readCtx, readCancel := context.WithTimeout(ctx, testWSQuietWindow)
	for {
		var msg struct {
			Notifications []Notification `json:"notifications"`
		}
		if err := wsjson.Read(readCtx, feed, &msg); err != nil {
			break // read deadline: no more messages in the window
		}
		assert.Empty(t, msg.Notifications, "a hub feed never gets notifications")
	}
	readCancel()

	dash, _, err := websocket.Dial(ctx, base, nil)
	require.NoError(t, err)
	defer dash.CloseNow()
	for {
		var msg struct {
			Notifications []Notification `json:"notifications"`
		}
		require.NoError(t, wsjson.Read(ctx, dash, &msg))
		if len(msg.Notifications) > 0 {
			assert.Equal(t, []int64{pushed.ID}, notificationIDs(msg.Notifications), "still waiting for a dashboard")
			return
		}
	}
}

// #3622 F2: an agent's notification link can only go to docs://, a web address, or a dashboard path.
func TestCheckNotificationLink(t *testing.T) {
	for _, u := range []string{
		"docs://workflow-quickstart", "https://example.test/a?b=1#c", "http://localhost:3000/", "HTTPS://Example.test",
		"/agents", "/board/my-team", "/docs/auth", "  /jobs  ",
	} {
		assert.NoError(t, checkNotificationLink(&NotificationLink{Label: "x", URL: u}), u)
	}
	for _, u := range []string{
		"", "javascript:alert(1)", "JavaScript:alert(1)", " javascript:alert(1)", "data:text/html,x", "vbscript:x", "file:///etc/passwd",
		"//evil.test/x", "/\\evil.test", "\\\\host\\share", "/api/system/api-key", "/api", "https://", "https://user:pw@example.test/",
		"ftp://example.test", "docs://", "docs://../x", "docs://a/b", "/a\nb", "/a\tb", "relative/path", "#hash",
		"https://" + strings.Repeat("a", 2100) + ".test",
	} {
		assert.Error(t, checkNotificationLink(&NotificationLink{Label: "x", URL: u}), "%q", u)
	}
	assert.Error(t, checkNotificationLink(&NotificationLink{Label: strings.Repeat("é", 201), URL: "/agents"}), "label over 200 characters")
	assert.NoError(t, checkNotificationLink(&NotificationLink{Label: strings.Repeat("é", 200), URL: "/agents"}))
}

// #5011: the notifications are the Operator's. An agent's token on /ws/coral gets the feed but never a notification, and it does not consume
// one: a notification pushed while only the agent is connected still reaches the dashboard that opens next.
func TestWSCoral_AnAgentTokenGetsNoNotificationsAndConsumesNone(t *testing.T) {
	_, handler := setupTestServer(t)
	ns := NewNotificationStore()
	handler.SetNotificationStore(ns)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if sid := req.Header.Get("X-Test-Agent"); sid != "" { // as the identity layer tags a verified agent session
				req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid, SessionName: "agent-" + sid}))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/ws/coral", handler.WSCoral)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	base := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	agent, _, err := websocket.Dial(ctx, base, &websocket.DialOptions{HTTPHeader: http.Header{"X-Test-Agent": {"00000000-0000-4000-8000-000000005011"}}})
	require.NoError(t, err)
	defer agent.CloseNow()
	var first map[string]json.RawMessage
	require.NoError(t, wsjson.Read(ctx, agent, &first), "the agent gets the feed")
	pushed := ns.Push(Notification{Title: "agent needs you", Level: "warning"})

	readCtx, readCancel := context.WithTimeout(ctx, testWSQuietWindow)
	for {
		var msg struct {
			Notifications []Notification `json:"notifications"`
		}
		if err := wsjson.Read(readCtx, agent, &msg); err != nil {
			break
		}
		assert.Empty(t, msg.Notifications, "an agent's socket never carries a notification")
	}
	readCancel()

	dash, _, err := websocket.Dial(ctx, base, nil)
	require.NoError(t, err)
	defer dash.CloseNow()
	for {
		var msg struct {
			Notifications []Notification `json:"notifications"`
		}
		require.NoError(t, wsjson.Read(ctx, dash, &msg))
		if len(msg.Notifications) > 0 {
			assert.Equal(t, []int64{pushed.ID}, notificationIDs(msg.Notifications), "the dashboard still gets what the agent's socket did not take")
			return
		}
	}
}
