package routes

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/machinehealth"
)

// stubHealth is a MachineHealthSource the test changes.
type stubHealth struct {
	mu sync.Mutex
	h  machinehealth.Health
}

func (s *stubHealth) Current() (machinehealth.Health, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.h, true
}
func (s *stubHealth) set(f func(*machinehealth.Health)) { s.mu.Lock(); f(&s.h); s.mu.Unlock() }

// #3444: the machine's health is in full in the first coral_update, then only the changed fields
// in coral_diff (null for a field that went away), on the hub's feed too; nothing when unchanged.
func TestWSCoral_MachineHealth(t *testing.T) {
	for _, feed := range []string{"", "?feed=hub"} {
		t.Run("feed"+feed, func(t *testing.T) {
			server, handler := setupTestServer(t)
			src := &stubHealth{h: machinehealth.Health{
				Status: "busy", Reason: "cpu", ResponsivenessMS: 40,
				Coral:  machinehealth.Proc{CPUPercent: 3.5, RSSBytes: 100},
				Agents: machinehealth.Agents{Count: 2, CPUPercent: 50, RSSBytes: 900},
				Host:   machinehealth.Host{Load1PerCore: 1.4, CPUCount: 8, MemTotal: 16000, MemAvailable: 8000},
				Disk:   machinehealth.Disk{FreeBytes: 5000, TotalBytes: 9000},
			}}
			handler.SetMachineHealth(src)

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral"+feed, nil)
			require.NoError(t, err)
			defer conn.CloseNow()

			read := func() map[string]json.RawMessage {
				var m map[string]json.RawMessage
				require.NoError(t, wsjson.Read(ctx, conn, &m))
				return m
			}
			first := read()
			assert.JSONEq(t, `"coral_update"`, string(first["type"]))
			var full map[string]any
			require.NoError(t, json.Unmarshal(first["machine_health"], &full))
			assert.Equal(t, "busy", full["status"])
			assert.Equal(t, "cpu", full["reason"])
			assert.EqualValues(t, 8, full["host"].(map[string]any)["cpu_count"])
			assert.EqualValues(t, 2, full["agents"].(map[string]any)["count"])

			// Back to ok: status, reason (gone: null) and the host change; nothing else is sent.
			src.set(func(h *machinehealth.Health) { h.Status, h.Reason = "ok", ""; h.Host.Load1PerCore = 0.4 })
			var diff map[string]json.RawMessage
			for diff == nil {
				m := read()
				if string(m["type"]) == `"coral_diff"` && m["machine_health"] != nil {
					diff = m
				}
			}
			var delta map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(diff["machine_health"], &delta))
			keys := map[string]string{}
			for k, v := range delta {
				keys[k] = string(v)
			}
			assert.Equal(t, map[string]string{
				"status": `"ok"`,
				"reason": `null`,
				"host":   `{"load1_per_core":0.4,"cpu_count":8,"mem_total":16000,"mem_available":8000}`,
			}, keys, "only the changed fields")

			// Unchanged: no machine_health in anything that follows for a while.
			deadline := time.Now().Add(testWSQuietWindow)
			for time.Now().Before(deadline) {
				rctx, rcancel := context.WithDeadline(ctx, deadline)
				var m map[string]json.RawMessage
				err := wsjson.Read(rctx, conn, &m)
				rcancel()
				if err != nil {
					break
				}
				assert.Nil(t, m["machine_health"], "nothing changed: not sent again")
			}
		})
	}
}
