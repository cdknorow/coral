// Reminders: a task's repeating reminder and a subscriber's (moved out of board.go, #4998; no behaviour change).

package routes

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/safego"
)

func (h *BoardHandler) reminderKey(project string, taskID int64) string {
	return project + ":" + strconv.FormatInt(taskID, 10)
}

func (h *BoardHandler) stopReminder(project string, taskID int64) {
	h.reminderMu.Lock()
	if cancel := h.reminders[h.reminderKey(project, taskID)]; cancel != nil {
		cancel()
		delete(h.reminders, h.reminderKey(project, taskID))
	}
	h.reminderMu.Unlock()
}

// RemindSubscriber schedules a custom periodic instruction for a board agent.
// POST /api/board/{project}/reminder with subscriber_id, message and interval_seconds.
func (h *BoardHandler) RemindSubscriber(w http.ResponseWriter, r *http.Request) {
	project := urlParam(r, "project")
	var body struct {
		SubscriberID    string `json:"subscriber_id"`
		Message         string `json:"message"`
		IntervalSeconds int    `json:"interval_seconds"`
	}
	if r.Method == http.MethodDelete {
		if err := decodeJSON(r, &body); err != nil {
			errBadRequest(w, "invalid JSON")
			return
		}
		attrWho, attrOK := attributedSubject(w, r, body.SubscriberID)
		if !attrOK {
			return
		}
		body.SubscriberID = attrWho
		if strings.TrimSpace(body.SubscriberID) == "" {
			errBadRequest(w, "subscriber_id is required")
			return
		}
		unlock := h.lockSubscriberReminderOps(project, body.SubscriberID)
		defer unlock()
		// Delete the durable row first: if that fails, the active reminder
		// and its persisted state must both remain untouched. Only after a
		// successful delete is the timer stopped, and stop returns only once
		// any in-flight nudge has finished and no later one can start.
		deleteFn := h.deleteSubscriberReminder
		if deleteFn == nil {
			deleteFn = h.bs.DeleteSubscriberReminder
		}
		if err := deleteFn(r.Context(), project, body.SubscriberID); err != nil {
			errInternalServer(w, "could not remove reminder")
			return
		}
		h.stopSubscriberReminder(project, body.SubscriberID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": false})
		return
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	attrWho, attrOK := attributedSubject(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	if strings.TrimSpace(body.SubscriberID) == "" || strings.TrimSpace(body.Message) == "" {
		errBadRequest(w, "subscriber_id and message are required")
		return
	}
	if body.IntervalSeconds < 30 || body.IntervalSeconds > 86400 {
		errBadRequest(w, "interval_seconds must be between 30 and 86400")
		return
	}
	if sub, _ := h.bs.GetProjectSubscription(r.Context(), project, body.SubscriberID); sub == nil || sub.SessionName == "" {
		errBadRequest(w, "agent has no running session on this board")
		return
	}
	unlock := h.lockSubscriberReminderOps(project, body.SubscriberID)
	defer unlock()
	if err := h.bs.UpsertSubscriberReminder(r.Context(), board.SubscriberReminder{Project: project, SubscriberID: body.SubscriberID, Message: body.Message, IntervalSeconds: body.IntervalSeconds}); err != nil {
		errInternalServer(w, "could not persist reminder")
		return
	}
	h.startSubscriberReminder(project, body.SubscriberID, body.Message, time.Duration(body.IntervalSeconds)*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active": true, "subscriber_id": body.SubscriberID, "interval_seconds": body.IntervalSeconds})
}

// GetSubscriberReminder: GET /api/board/{project}/reminder?subscriber_id=X. The reminder in force for an agent: its instruction and its interval, for the Team view's "Edit reminder" to open with
// what is there (#6329). A person's alone: the text is the Operator's instruction to one agent, so an agent's session token is refused (it could read another agent's), and the route has no
// rule in the tunnel table (never through Coral Hub) so a Hub viewer never sees it. Nothing is changed. It is not part of the availability read, which is shared and cached for every caller.
func (h *BoardHandler) GetSubscriberReminder(w http.ResponseWriter, r *http.Request) {
	if !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can read a reminder's instruction")
		return
	}
	project := urlParam(r, "project")
	subscriber := strings.TrimSpace(r.URL.Query().Get("subscriber_id"))
	if subscriber == "" {
		errBadRequest(w, "subscriber_id is required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	seconds, active := h.SubscriberReminderInterval(project, subscriber)
	if !active {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	rows, err := h.bs.ListSubscriberReminders(r.Context())
	if err != nil {
		errInternalServer(w, "could not read the reminder")
		return
	}
	for _, row := range rows {
		if row.Project == project && row.SubscriberID == subscriber {
			writeJSON(w, http.StatusOK, map[string]any{"active": true, "subscriber_id": subscriber, "message": row.Message, "interval_seconds": seconds})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": false}) // running but not persisted any more (removed meanwhile)
}

// RestoreSubscriberReminders reloads durable reminders after a server restart.
func (h *BoardHandler) RestoreSubscriberReminders(ctx context.Context) error {
	if h.bs == nil {
		return nil // a board that could not be opened is left out (#4924); nothing to restore
	}
	rows, err := h.bs.ListSubscriberReminders(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if sub, _ := h.bs.GetProjectSubscription(ctx, r.Project, r.SubscriberID); sub == nil || sub.SessionName == "" {
			continue
		}
		unlock := h.lockSubscriberReminderOps(r.Project, r.SubscriberID)
		h.startSubscriberReminder(r.Project, r.SubscriberID, r.Message, time.Duration(r.IntervalSeconds)*time.Second)
		unlock()
	}
	return nil
}

func (h *BoardHandler) subscriberReminderKey(project, subscriber string) string {
	return "agent:" + project + ":" + subscriber
}

func (h *BoardHandler) HasSubscriberReminder(project, subscriber string) bool {
	h.reminderMu.Lock()
	defer h.reminderMu.Unlock()
	return h.reminders[h.subscriberReminderKey(project, subscriber)] != nil
}

func (h *BoardHandler) SubscriberReminderInterval(project, subscriber string) (int, bool) {
	h.reminderMu.Lock()
	defer h.reminderMu.Unlock()
	seconds, ok := h.subscriberReminderIntervals[h.subscriberReminderKey(project, subscriber)]
	return seconds, ok && h.reminders[h.subscriberReminderKey(project, subscriber)] != nil
}

// lockSubscriberReminderOps serializes reminder mutations (replace, remove,
// restore) for one team/subscriber and returns the unlock function.
func (h *BoardHandler) lockSubscriberReminderOps(project, subscriber string) func() {
	key := h.subscriberReminderKey(project, subscriber)
	h.reminderMu.Lock()
	if h.subscriberReminderOps == nil {
		h.subscriberReminderOps = make(map[string]*sync.Mutex)
	}
	m := h.subscriberReminderOps[key]
	if m == nil {
		m = &sync.Mutex{}
		h.subscriberReminderOps[key] = m
	}
	h.reminderMu.Unlock()
	m.Lock()
	return m.Unlock
}

// stopSubscriberReminder cancels the reminder and then waits for any nudge the
// timer goroutine is in the middle of sending. It is an ordering barrier: once
// it returns, no nudge from the stopped reminder is in flight or can start,
// even when the terminal ignores context cancellation. It must not be called
// from the reminder goroutine itself or while holding reminderMu.
func (h *BoardHandler) stopSubscriberReminder(project, subscriber string) {
	h.reminderMu.Lock()
	if h.subscriberReminderGenerations == nil {
		h.subscriberReminderGenerations = make(map[string]uint64)
	}
	key := h.subscriberReminderKey(project, subscriber)
	h.subscriberReminderGenerations[key]++
	var fire *sync.Mutex
	if c := h.reminders[key]; c != nil {
		c()
		delete(h.reminders, key)
		fire = h.subscriberReminderFire[key]
		delete(h.subscriberReminderFire, key)
	}
	delete(h.subscriberReminderIntervals, key)
	h.reminderMu.Unlock()
	if fire != nil {
		fire.Lock()
		fire.Unlock() //nolint:staticcheck // empty critical section is the barrier
	}
}

func (h *BoardHandler) startSubscriberReminder(project, subscriber, message string, interval time.Duration) {
	h.stopSubscriberReminder(project, subscriber)
	ctx, cancel := context.WithCancel(context.Background())
	key := h.subscriberReminderKey(project, subscriber)
	h.reminderMu.Lock()
	if h.reminders == nil {
		h.reminders = make(map[string]context.CancelFunc)
	}
	if h.subscriberReminderIntervals == nil {
		h.subscriberReminderIntervals = make(map[string]int)
	}
	if h.subscriberReminderGenerations == nil {
		h.subscriberReminderGenerations = make(map[string]uint64)
	}
	if h.subscriberReminderFire == nil {
		h.subscriberReminderFire = make(map[string]*sync.Mutex)
	}
	generation := h.subscriberReminderGenerations[key]
	fire := &sync.Mutex{}
	h.subscriberReminderFire[key] = fire
	h.reminders[key] = cancel
	h.subscriberReminderIntervals[key] = int(interval / time.Second)
	h.reminderMu.Unlock()
	go func() {
		defer safego.Recover("server/routes/board:340")
		// The first reminder comes 0.8-1.2 intervals in, the same for this subscriber (#3613):
		// reminders restored together at start don't all fire in the same second. Then every
		// interval.
		defer func() {
			if f := h.reminderExited.Load(); f != nil {
				(*f)()
			}
		}()
		ticker := time.NewTicker(ptymanager.JitterAround(interval, key))
		defer ticker.Stop()
		defer h.finishSubscriberReminder(project, subscriber, generation)
		first := true
		for {
			select {
			case <-ticker.C:
				if first {
					ticker.Reset(interval)
					first = false
				}
				// Cancellation and a ready ticker can be selected in either
				// order. Under the fire mutex, re-check cancellation and send:
				// stop cancels first and then takes the same mutex, so a
				// nudge either completes before stop returns or never starts.
				fire.Lock()
				if ctx.Err() != nil {
					fire.Unlock()
					return
				}
				err := h.sendTaskNudge(ctx, project, subscriber, "[Coral reminder] "+message)
				fire.Unlock()
				if err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// finishSubscriberReminder only clears the timer that owns generation. An
// older goroutine must not erase a replacement reminder installed for the same
// agent while its deferred cleanup runs.
func (h *BoardHandler) finishSubscriberReminder(project, subscriber string, generation uint64) {
	h.reminderMu.Lock()
	defer h.reminderMu.Unlock()
	key := h.subscriberReminderKey(project, subscriber)
	if h.subscriberReminderGenerations[key] != generation {
		return
	}
	delete(h.reminders, key)
	delete(h.subscriberReminderIntervals, key)
	delete(h.subscriberReminderFire, key)
}
