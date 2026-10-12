package routes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func notificationIDs(ns []Notification) []int64 {
	var ids []int64
	for _, n := range ns {
		ids = append(ids, n.ID)
	}
	return ids
}

func TestNotificationStore_EveryDashboardGetsEachNotification(t *testing.T) {
	ns := NewNotificationStore()
	a, b := ns.StartCursor(), ns.StartCursor()
	first := ns.Push(Notification{Title: "one"})

	got, a := ns.Since(a)
	assert.Equal(t, []int64{first.ID}, notificationIDs(got))
	got, b = ns.Since(b)
	assert.Equal(t, []int64{first.ID}, notificationIDs(got), "a second dashboard gets it too")

	got, a = ns.Since(a)
	assert.Empty(t, got, "each dashboard gets a notification once")
	second := ns.Push(Notification{Title: "two"})
	got, _ = ns.Since(b)
	assert.Equal(t, []int64{second.ID}, notificationIDs(got))
	got, _ = ns.Since(a)
	assert.Equal(t, []int64{second.ID}, notificationIDs(got))
}

func TestNotificationStore_PendingWaitsForNextDashboardWithoutReplay(t *testing.T) {
	ns := NewNotificationStore()
	early := ns.Push(Notification{Title: "pushed with no dashboard open"})

	first := ns.StartCursor()
	got, _ := ns.Since(first)
	assert.Equal(t, []int64{early.ID}, notificationIDs(got), "queued until a dashboard connects")

	late := ns.Push(Notification{Title: "pushed after delivery"})
	reload := ns.StartCursor() // e.g. a page reload or another tab
	got, _ = ns.Since(reload)
	assert.Equal(t, []int64{late.ID}, notificationIDs(got), "undelivered ones still arrive; already-shown ones are not replayed")
}

func TestNotificationStore_Expiry(t *testing.T) {
	now := time.Now()
	ns := NewNotificationStore()
	ns.now = func() time.Time { return now }
	cursor := ns.StartCursor()
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
