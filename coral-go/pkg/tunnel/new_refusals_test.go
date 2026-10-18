package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/secrets"
)

// newRefusal is one refusal Coral v1.4.91 and v1.4.92 added (#4602 and its follow-ups), as the server writes it: its route, the tier the
// tunnel gives that route, and the status, headers and JSON body the Coral server sends. src names the code that writes it.
type newRefusal struct {
	name         string
	method, path string
	tier         Tier // what the tunnel's table says for the route; TierNever: the tunnel never carries it
	status       int
	hdr          http.Header
	body         map[string]any
	src          string
}

func actionLimit(retryAfter string) http.Header {
	h := http.Header{httputil.ActionLimitHeader: {"action"}}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return h
}

func newRefusals() []newRefusal {
	longPost := "a board post is at most 256 KiB (this one is 300 KiB): put the long text in an artifact (coral-agent artifact upload) and post its link"
	tooLarge := func(what string, max int) string {
		return what + " is at most " + itoa(max) + " bytes (this one is " + itoa(max+1) + "): shorten it; long text belongs in an artifact (coral-agent artifact upload) with its link in the text"
	}
	disk := "The disk Coral stores its data on is almost full, so this was not saved. Free some space (Settings > Storage shows what Coral uses) and try again."
	return []newRefusal{
		{"launch-team over 32 agents", "POST", "/api/sessions/launch-team", TierLaunch, 400, nil,
			map[string]any{"error": "a team can have at most 32 agents per launch", "code": "too_many_agents", "max": 32}, "sessions_launch.go LaunchTeam"},
		{"resize outside 10-1000 columns", "POST", "/api/sessions/live/a/resize", TierInteract, 400, nil,
			map[string]any{"error": "columns must be from 10 to 1000"}, "sessions_input.go Resize"},
		{"board post over 256 KiB", "POST", "/api/board/b/messages", TierInteract, 413, nil,
			map[string]any{"error": longPost, "message": longPost, "code": "content_too_large", "max_bytes": 262144, "not_sent": true}, "board.go PostMessage"},
		{"board post, disk nearly full", "POST", "/api/board/b/messages", TierInteract, 507, nil,
			map[string]any{"error": disk, "message": disk, "code": "disk_low", "free_bytes": 123456, "not_sent": true}, "helpers.go refuseIfDiskLow"},
		{"task title over 512 bytes", "POST", "/api/board/b/tasks", TierInteract, 413, nil,
			map[string]any{"error": tooLarge("a task title", 512), "message": tooLarge("a task title", 512), "code": "title_too_large", "max_bytes": 512, "not_saved": true}, "board.go refuseTextTooLarge"},
		{"task body over 256 KiB", "POST", "/api/board/b/tasks", TierInteract, 413, nil,
			map[string]any{"error": tooLarge("a task body", 262144), "message": tooLarge("a task body", 262144), "code": "body_too_large", "max_bytes": 262144, "not_saved": true}, "board.go refuseTextTooLarge"},
		{"500 open tasks per agent", "POST", "/api/board/b/tasks", TierInteract, 409, nil,
			map[string]any{"error": "an agent can have at most 500 open tasks it created on a board (it has 500): complete or cancel some first", "message": "an agent can have at most 500 open tasks it created on a board (it has 500): complete or cancel some first", "code": "too_many_open_tasks", "max": 500, "not_saved": true}, "board.go CreateTask"},
		{"retitle over 512 bytes", "PATCH", "/api/board/b/tasks/1", TierInteract, 413, nil,
			map[string]any{"error": tooLarge("a task title", 512), "message": tooLarge("a task title", 512), "code": "title_too_large", "max_bytes": 512, "not_saved": true}, "board.go UpdateTask"},
		{"amend body over 256 KiB", "PATCH", "/api/board/b/tasks/1/amend", TierInteract, 413, nil,
			map[string]any{"error": tooLarge("a task body", 262144), "message": tooLarge("a task body", 262144), "code": "body_too_large", "max_bytes": 262144, "not_saved": true}, "board.go AmendTask"},
		{"wait reason over 4 KiB", "POST", "/api/board/b/waits", TierNever, 413, nil,
			map[string]any{"error": tooLarge("a wait's reason", 4096), "message": tooLarge("a wait's reason", 4096), "code": "reason_too_large", "max_bytes": 4096, "not_saved": true}, "board.go RegisterWait"},
		{"50 active waits", "POST", "/api/board/b/waits", TierNever, 409, nil,
			map[string]any{"error": "a subscriber can have at most 50 active waits (one per board): cancel one first", "message": "a subscriber can have at most 50 active waits (one per board): cancel one first", "code": "too_many_waits", "max": 50, "not_saved": true}, "board.go RegisterWait"},
		{"20 UI panels", "PUT", "/api/agent/ui/x", TierNever, 409, nil,
			map[string]any{"error": "a session can have at most 20 panels: remove one first", "message": "a session can have at most 20 panels: remove one first", "code": "panel_limit"}, "agent_ui.go"},
		{"32 MiB of panel HTML", "PUT", "/api/agent/ui/x", TierNever, 409, nil,
			map[string]any{"error": "a session's panels can hold at most 32 MiB of HTML in all: remove one or make this one smaller", "message": "a session's panels can hold at most 32 MiB of HTML in all: remove one or make this one smaller", "code": "panel_html_limit"}, "agent_ui.go"},
		{"agent upload, store full", "POST", "/api/agent/artifacts", TierNever, 413, nil,
			map[string]any{"error": "the artifact store is full (20.0 GiB of 20.0 GiB): ask the Operator to delete old artifacts or raise the limit (Settings > Storage; artifact_store_max_bytes, 0 for none)", "message": "the artifact store is full (20.0 GiB of 20.0 GiB): ask the Operator to delete old artifacts or raise the limit (Settings > Storage; artifact_store_max_bytes, 0 for none)", "code": "artifact_quota", "limit": "store"}, "artifacts.go artifactRefusal"},
		{"agent upload, disk nearly full", "POST", "/api/agent/artifacts", TierNever, 507, nil,
			map[string]any{"error": disk, "message": disk, "code": "disk_low", "free_bytes": 1, "not_sent": true}, "artifacts.go uploadAgentArtifact"},
		{"cross-team file, disk nearly full", "POST", "/api/board/b/external-artifacts", TierInteract, 507, nil,
			map[string]any{"ok": false, "error": "disk_low", "message": disk, "limit": "disk"}, "cross_team_artifacts.go receivedRefuse"},
		{"agent write budget", "POST", "/api/board/b/messages", TierInteract, 429, actionLimit("7"),
			map[string]any{"error": "This agent is writing faster than 240 a minute: slow down and retry in 7 s.", "message": "This agent is writing faster than 240 a minute: slow down and retry in 7 s.", "code": "rate_limited", "not_sent": true}, "agent_write_limit.go"},
		{"agent launch ceiling", "POST", "/api/sessions/launch", TierLaunch, 429, actionLimit(""),
			map[string]any{"error": "Coral has 64 live agents and an agent may not start more past 64 (this launch adds 1): stop some agents, or the Operator can raise the agent_launch_limit setting (PUT /api/settings; 0 for no ceiling)", "message": "Coral has 64 live agents and an agent may not start more past 64 (this launch adds 1): stop some agents, or the Operator can raise the agent_launch_limit setting (PUT /api/settings; 0 for no ceiling)", "code": "agent_limit", "live": 64, "max": 64, "not_started": true}, "agent_launch_limit.go"},
		{"team generation busy", "POST", "/api/teams/generate", TierLaunch, 429, actionLimit("10"),
			map[string]any{"error": "Teams are already being generated: try again in a moment.", "code": "busy"}, "system.go GenerateTeam"},
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// Every refusal the server added in v1.4.91 and v1.4.92 reaches a caller through the tunnel as it was sent (status, code, the
// X-Coral-Limit marker, Retry-After, the message through the connector's real redaction), a route the tunnel does not carry stays refused
// there (the refusal never leaves the computer), and a computer whose mode is below a route's tier refuses it in the connector, before the
// server could answer (#4707). The viewer's own tier does NOT gate routes in the connector: it only decides redaction (hubtier.go), the hub
// enforces what each member may call; TestNewRefusalsReachAViewerAsTheServerSentThem pins that, so a change shows.
func TestNewRefusalsReachATunnelCallerIntact(t *testing.T) {
	rows := newRefusals()
	byRoute := map[string][]newRefusal{}
	for _, r := range rows {
		byRoute[r.method+" "+r.path] = append(byRoute[r.method+" "+r.path], r)
	}
	var mu sync.Mutex
	var reached []string
	var current atomic.Value // the row the fake server answers as: the tunnel does not carry a test header to it
	current.Store("")
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = append(reached, r.Method+" "+r.URL.Path)
		mu.Unlock()
		want, _ := current.Load().(string)
		for _, row := range rows {
			if row.name != want {
				continue
			}
			for k, vs := range row.hdr {
				w.Header()[k] = vs
			}
			b, _ := json.Marshal(row.body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(row.status)
			_, _ = w.Write(b)
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.Redactor = secrets.Redactor()
		l.MaxMode = func() Mode { return TierLaunch }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return TierLaunch })
	})
	do := func(row newRefusal, tier string) (*http.Response, []byte, error) {
		ctx := WithHubTier(context.Background(), tier)
		req, _ := http.NewRequestWithContext(ctx, row.method, "http://x"+row.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		current.Store(row.name)
		resp, err := sess.RoundTrip(req)
		if err != nil {
			return nil, nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b, nil
	}
	tierName := map[Tier]string{TierView: "view", TierInteract: "interact", TierLaunch: "launch"}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, tier, _ := RuleFor(row.method, row.path)
			require.Equal(t, row.tier, tier, "%s: the tunnel's tier for %s %s changed (the refusal is written by %s)", row.name, row.method, row.path, row.src)
			if row.tier == TierNever {
				mu.Lock()
				before := len(reached)
				mu.Unlock()
				_, _, err := do(row, "launch")
				require.Error(t, err, "a route the tunnel does not carry")
				assert.Contains(t, err.Error(), "path_not_allowed")
				mu.Lock()
				assert.Equal(t, before, len(reached), "the refusal was never produced: the request did not reach the server")
				mu.Unlock()
				return
			}
			resp, b, err := do(row, tierName[row.tier])
			require.NoError(t, err)
			assert.Equal(t, row.status, resp.StatusCode, "status")
			for _, k := range []string{httputil.ActionLimitHeader, "Retry-After"} {
				assert.Equal(t, row.hdr.Get(k), resp.Header.Get(k), "header %s", k)
			}
			assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json"), "content type")
			var got map[string]any
			require.NoError(t, json.Unmarshal(b, &got), "the reply is still JSON: %s", b)
			want, _ := json.Marshal(row.body)
			var wantMap map[string]any
			require.NoError(t, json.Unmarshal(want, &wantMap))
			assert.Equal(t, wantMap, got, "code, message and fields unchanged")

		})
	}
}

// The computer's own mode is the connector's gate: at View, every route above View is refused by the tunnel and never reaches the server,
// so none of the refusals above can come from the server to a caller on a View computer.
func TestNewRefusalsAreNotProducedOnAViewComputer(t *testing.T) {
	var mu sync.Mutex
	reached := 0
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached++
		mu.Unlock()
		w.WriteHeader(http.StatusTeapot)
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.Redactor = secrets.Redactor()
		l.MaxMode = func() Mode { return TierView }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return TierView })
	})
	for _, row := range newRefusals() {
		ctx := WithHubTier(context.Background(), "launch") // the hub's member could launch; the computer is at View
		req, _ := http.NewRequestWithContext(ctx, row.method, "http://x"+row.path, strings.NewReader(`{}`))
		resp, err := sess.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		require.Error(t, err, "%s: refused by the connector on a View computer", row.name)
		if row.tier == TierNever {
			assert.Contains(t, err.Error(), "path_not_allowed", row.name)
		} else {
			assert.Contains(t, err.Error(), "local_mode: view", "%s: the computer's mode is what refuses it", row.name)
		}
	}
	mu.Lock()
	assert.Zero(t, reached, "no request reached the server")
	mu.Unlock()
}

// A viewer below a route's tier is NOT refused by the connector (only the computer's mode is): it gets what the server sends, redacted. The
// hub decides each member's tier before it opens a stream; this records that the connector does not repeat the check.
func TestNewRefusalsReachAViewerAsTheServerSentThem(t *testing.T) {
	row := newRefusals()[2] // a board post over 256 KiB, an Interact route
	require.Equal(t, TierInteract, row.tier)
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(row.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(row.status)
		_, _ = w.Write(b)
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.Redactor = secrets.Redactor()
		l.MaxMode = func() Mode { return TierLaunch }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return TierLaunch })
	})
	req, _ := http.NewRequestWithContext(WithHubTier(context.Background(), "view"), row.method, "http://x"+row.path, strings.NewReader(`{}`))
	resp, err := sess.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, row.status, resp.StatusCode, "the viewer's tier gates redaction only; the hub gates the route")
}
