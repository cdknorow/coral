package routes

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
)

// WSCoral's per-tick decision, moved into coralFeed (frame, then sent once written) (#4713), builds what
// coralFrameBefore4713 (the loop's block at v1.4.92, below) built: the first coral_update, each coral_diff (rows
// changed whole or field by field, gone locally or on a remote, servers, runs, machine health, notifications), the
// heartbeat after a silence, or nothing; and it leaves the connection's view in the same state. Each seed is a
// connection fed a run of random ticks. The rows changed and gone are compared as sets: both build them by ranging
// over a map. The select loop around it (ticks, kicks, remote debounce) moved unchanged. Each kind of frame and
// field has a floor.

type oldCoralState struct {
	fieldDiff       bool
	prevSessions    map[string]string
	prevByKey       map[string]map[string]any
	prevRunsJSON    string
	prevHealth      map[string]string
	firstMessage    bool
	lastSent        time.Time
	prevServersJSON string
}

func randomCoralTick(rng *rand.Rand, keys []string) *wsTick {
	t := &wsTick{current: map[string]string{}, byKey: map[string]map[string]any{}}
	for _, k := range keys {
		if rng.Intn(5) == 0 {
			continue // gone this tick
		}
		row := map[string]any{"session_id": k, "name": "agent " + k, "agent_type": "claude", "status": []string{"Working", "Idle"}[rng.Intn(2)]}
		if rng.Intn(2) == 0 {
			row["board_unread"] = float64(rng.Intn(3))
		}
		if srv, _, ok := splitRemoteKey(k); ok {
			row["server"] = srv
		}
		b, _ := json.Marshal(row)
		t.current[k], t.byKey[k] = string(b), row
		t.sessions = append(t.sessions, row)
	}
	t.hasRemotes = rng.Intn(2) == 0
	if t.hasRemotes {
		t.servers = []background.RemoteServerStatus{{ID: "srv-1", Label: "Box", Status: []string{"online", "offline"}[rng.Intn(2)]}}
	}
	b, _ := json.Marshal(t.servers)
	t.serversJSON = string(b)
	runs := []string{"[]", `[{"id":1}]`, `[{"id":2}]`}[rng.Intn(3)]
	t.runsJSON = runs
	json.Unmarshal([]byte(runs), &t.activeRuns)
	return t
}

// canonCoral is a frame with its row lists sorted (both sides build them from a map).
func canonCoral(m map[string]any) string {
	if m == nil {
		return "nil"
	}
	c := map[string]any{}
	for k, v := range m {
		c[k] = v
	}
	for _, k := range []string{"changed", "removed", "removed_remote"} {
		if v, ok := c[k]; ok {
			b, _ := json.Marshal(v)
			var items []any
			json.Unmarshal(b, &items)
			strs := make([]string, len(items))
			for i, it := range items {
				ib, _ := json.Marshal(it)
				strs[i] = string(ib)
			}
			sort.Strings(strs)
			c[k] = strs
		}
	}
	b, _ := json.Marshal(c)
	return string(b)
}

func TestCoralFeedAgreesWithBefore4713(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	kinds := map[string]int{}
	base := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	heartbeat := 15 * time.Second
	for seed := int64(1); seed <= 500; seed++ {
		rng := rand.New(rand.NewSource(seed))
		fieldDiff := rng.Intn(2) == 0
		old := &oldCoralState{fieldDiff: fieldDiff, prevSessions: map[string]string{}, prevByKey: map[string]map[string]any{}, prevRunsJSON: "[]", prevHealth: map[string]string{}, firstMessage: true}
		feed := &coralFeed{fieldDiff: fieldDiff, prevSessions: map[string]string{}, prevByKey: map[string]map[string]any{}, prevRunsJSON: "[]", prevHealth: map[string]string{}, firstMessage: true}
		keys := []string{"s1", "s2", "s3", remoteKey("srv-1", "r1"), remoteKey("srv-1", "r2")}[:2+rng.Intn(4)]
		now := base
		var tick *wsTick
		for i := 0; i < 20; i++ {
			now = now.Add(time.Duration(rng.Intn(9)) * time.Second)
			quiet := tick != nil && rng.Intn(3) == 0 // the same tick again: nothing changed (a heartbeat when it's been a while)
			if !quiet {
				tick = randomCoralTick(rng, keys)
			}
			var pending []Notification
			if !quiet && rng.Intn(5) == 0 {
				pending = []Notification{{ID: int64(i + 1), Title: "Done", Message: "a task finished", Type: "toast", Level: "info"}}
			}
			var healthDelta map[string]any
			var healthNow map[string]string
			switch h := rng.Intn(5); {
			case quiet && h > 2: // quiet: unchanged health
				healthNow, healthDelta = map[string]string{"state": `"ok"`}, map[string]any{}
			case h == 0: // no machine health
			case h == 1 || h == 2: // unchanged
				healthNow, healthDelta = map[string]string{"state": `"ok"`}, map[string]any{}
			default: // changed
				healthNow, healthDelta = map[string]string{"state": `"busy"`}, map[string]any{"state": "busy"}
			}
			oldFrame, ack := coralFrameBefore4713(old, tick, pending, healthDelta, healthNow, now, heartbeat)
			newFrame, kind := feed.frame(tick, pending, healthDelta, healthNow, now, heartbeat)
			if newFrame != nil {
				feed.sent(kind, tick, healthDelta, healthNow, now)
			}
			require.Equal(t, canonCoral(oldFrame), canonCoral(newFrame), "seed %d tick %d", seed, i)
			require.Equal(t, ack, newFrame != nil && kind != coralHeartbeat, "seed %d tick %d: notifications counted as delivered", seed, i)
			got := fmt.Sprintf("%v %v %q %v %v %v %q", feed.prevSessions, feed.prevByKey, feed.prevRunsJSON, feed.prevHealth, feed.firstMessage, feed.lastSent, feed.prevServersJSON)
			want := fmt.Sprintf("%v %v %q %v %v %v %q", old.prevSessions, old.prevByKey, old.prevRunsJSON, old.prevHealth, old.firstMessage, old.lastSent, old.prevServersJSON)
			require.Equal(t, want, got, "seed %d tick %d: the connection's view", seed, i)
			for _, k := range coralKinds(newFrame) {
				kinds[k]++
			}
		}
	}
	t.Logf("compared, per kind: %v", kinds)
	for _, k := range []string{"coral_update", "coral_diff", "coral_heartbeat", "nothing", "changed (whole row)", "changed (fields)",
		"removed", "removed_remote", "servers", "active_runs", "machine_health", "notifications", "update with servers", "update with health"} {
		assert.GreaterOrEqual(t, kinds[k], 20, "%q went (almost) unexercised", k)
	}
}

func coralKinds(m map[string]any) []string {
	if m == nil {
		return []string{"nothing"}
	}
	out := []string{fmt.Sprint(m["type"])}
	if ch, ok := m["changed"].([]map[string]any); ok {
		for _, r := range ch {
			if r["_partial"] == true {
				out = append(out, "changed (fields)")
			} else {
				out = append(out, "changed (whole row)")
			}
		}
	}
	for _, k := range []string{"removed", "removed_remote", "servers", "active_runs", "machine_health", "notifications"} {
		if _, ok := m[k]; ok && m["type"] == "coral_diff" {
			out = append(out, k)
		}
	}
	if m["type"] == "coral_update" {
		if _, ok := m["servers"]; ok {
			out = append(out, "update with servers")
		}
		if _, ok := m["machine_health"]; ok {
			out = append(out, "update with health")
		}
	}
	return out
}

// coralFrameBefore4713 is WSCoral's per-tick block before #4713 moved it into coralFeed (websocket.go at v1.4.92),
// kept to compare with: the same lines, with the connection's state in st, the write recorded as frame (and taken as
// written), sent() as ack, and the clock passed in.
func coralFrameBefore4713(st *oldCoralState, tick *wsTick, pending []Notification, healthDelta map[string]any, healthNow map[string]string, now time.Time, heartbeat time.Duration) (frame map[string]any, ack bool) {
	fieldDiff, prevSessions, prevByKey, prevRunsJSON, prevHealth, firstMessage, lastSent, prevServersJSON :=
		st.fieldDiff, st.prevSessions, st.prevByKey, st.prevRunsJSON, st.prevHealth, st.firstMessage, st.lastSent, st.prevServersJSON
	defer func() {
		st.prevSessions, st.prevByKey, st.prevRunsJSON, st.prevHealth, st.firstMessage, st.lastSent, st.prevServersJSON =
			prevSessions, prevByKey, prevRunsJSON, prevHealth, firstMessage, lastSent, prevServersJSON
	}()
	_ = fieldDiff
	// Shared with every connection: read, never modify.
	sessions := tick.sessions
	remoteServers := tick.servers
	hasRemotes := tick.hasRemotes
	activeRuns := tick.activeRuns
	currSessions := tick.current
	sessionByKey := tick.byKey
	serversJSON := tick.serversJSON
	currRunsStr := tick.runsJSON

	sent := func() { ack = true }

	if firstMessage {
		msg := map[string]any{
			"type":        "coral_update",
			"sessions":    sessions,
			"active_runs": activeRuns,
		}
		if healthNow != nil {
			msg["machine_health"] = healthDelta
			prevHealth = healthNow
		}
		if len(pending) > 0 {
			msg["notifications"] = pending
		}
		if hasRemotes {
			msg["servers"] = remoteServers
		}
		frame = msg
		sent()
		prevServersJSON = serversJSON
		prevSessions = currSessions
		prevByKey = sessionByKey
		prevRunsJSON = currRunsStr
		firstMessage = false
		lastSent = now
		return
	}

	// Calculate diff
	var changed []map[string]any
	for key, sJSON := range currSessions {
		if prevSessions[key] != sJSON {
			if prev, ok := prevByKey[key]; ok && fieldDiff {
				changed = append(changed, fieldDelta(prev, sessionByKey[key]))
			} else {
				changed = append(changed, sessionByKey[key])
			}
		}
	}

	var removed []string
	var removedRemote []map[string]string
	for key := range prevSessions {
		if _, exists := currSessions[key]; !exists {
			if srv, k, ok := splitRemoteKey(key); ok {
				removedRemote = append(removedRemote, map[string]string{"server": srv, "key": k})
			} else {
				removed = append(removed, key)
			}
		}
	}
	serversChanged := serversJSON != prevServersJSON

	runsChanged := currRunsStr != prevRunsJSON

	hasNotifications := len(pending) > 0
	healthChanged := len(healthDelta) > 0
	if len(changed) > 0 || len(removed) > 0 || len(removedRemote) > 0 || serversChanged || runsChanged || hasNotifications || healthChanged {
		payload := map[string]any{"type": "coral_diff"}
		if healthChanged {
			payload["machine_health"] = healthDelta
		}
		if len(changed) > 0 {
			payload["changed"] = changed
		}
		if len(removed) > 0 {
			payload["removed"] = removed
		}
		if len(removedRemote) > 0 {
			payload["removed_remote"] = removedRemote
		}
		if hasRemotes && serversChanged {
			payload["servers"] = remoteServers
		}
		if runsChanged {
			payload["active_runs"] = activeRuns
		}
		if hasNotifications {
			payload["notifications"] = pending
		}
		frame = payload
		sent()
		lastSent = now
		if healthChanged {
			prevHealth = healthNow
		}
		prevServersJSON = serversJSON
		prevSessions = currSessions
		prevByKey = sessionByKey
		prevRunsJSON = currRunsStr
	} else if now.Sub(lastSent) >= heartbeat {
		// Nothing changed for a while. Say so, so a hub holding this feed
		// can tell an idle server from one whose tick loop is stuck (it
		// falls back to polling the list when the feed goes quiet).
		// Dashboards ignore the type.
		frame = map[string]any{"type": "coral_heartbeat"}
		lastSent = now
	}
	return
}
