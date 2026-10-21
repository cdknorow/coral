package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The guard's outcomes the oracle must reach, each counted from the condition happening (not from a request
// being sent), with a floor.
var tunnelGuardOutcomes = map[string]int{
	"skipped (not a tunneled board write, a chat post)": 40,
	"body unreadable":                                                20,
	"receive_mode all":                                               40,
	"orchestrator by name":                                           40,
	"orchestrator by its subscription":                               40,
	"Operator refused, named":                                        40,
	"Operator refused, no actor named":                               20,
	"existing member or role":                                        40,
	"made-up name":                                                   40,
	"hub user absent":                                                40,
	"hub user malformed or repeated":                                 40,
	"hub user is a member":                                           20,
	"served as the Operator, its handler 200":                        40,
	"served as the Operator, its handler 404":                        20,
	"served as the hub user with its own subscription":               10,
	"served with a reminder's subscriber_id (a target)":              10,
	"served with the body put back for the handler":                  40,
	"a job_title naming a peeking member (member, not orchestrator)": 10,
}

type erroringBody struct{}

func (erroringBody) Read([]byte) (int, error) { return 0, errors.New("the connection dropped") }
func (erroringBody) Close() error             { return nil }

// #4756: TunnelIdentityGuard split into tunnelIdentityValues, tunnelOrchestratorRules, tunnelOtherIdentities and
// serveAsHubOperator. On random board writes (tunneled or not, any method and route, identity names in the query
// and the body, receive_mode, hub user headers, the Operator opt-out on or off) the old guard and the new one
// answer the same, call the handler the same (with the same hub user and body), record the same Operator action,
// and ask operatorAllowed as often.
func TestTunnelIdentityGuardAgreesWithBefore4756(t *testing.T) {
	_, handler := setupBoardTestServer(t)
	bs := handler.bs
	ctx := context.Background()
	for _, m := range []struct {
		id, title string
		peek      bool
	}{
		{"Lead", "Developer", true}, {"Dev", "Developer", false}, {"Tess", "QA Engineer", false},
		{"Orch", "Orchestrator", false}, {"hub-user-0000beef", "Developer", false}, {"hub-user-0000cafe", hubSubscriberTitle, false},
	} {
		_, err := bs.Subscribe(ctx, "p", m.id, m.title, "s-"+m.id, nil, "", m.peek)
		require.NoError(t, err)
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	type seen struct {
		passed  bool
		hubUser string
		body    string
	}
	var got seen
	var allowedCalls int
	allow := true
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.passed, got.hubUser = true, hubOperatorUser(r)
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			got.body = string(b)
		}
		if strings.HasSuffix(r.URL.Path, "/fails") {
			w.WriteHeader(http.StatusNotFound)
		}
	})
	allowed := func(*http.Request) bool { allowedCalls++; return allow }
	guards := []http.Handler{tunnelIdentityGuardBefore4756(bs, allowed)(next), TunnelIdentityGuard(bs, allowed)(next)}

	names := []string{"", "  ", "Operator", " operator ", "OPERATOR", "orchestrator", "my-Orchestrator", "Lead", "lead",
		"Dev", " Dev ", "D\u0435v", "Developer", "QA Engineer", "Orch", "Ghost", "hub-user-0000beef", "hub-user-0000cafe", "Tess"}
	paths := []string{"/api/board/p/tasks", "/api/board/p/tasks/7/claim", "/api/board/p/reminder", "/api/board/p/messages",
		"/api/board/p/pause", "/api/board/q/resume", "/api/board/p/external-messages", "/api/board/projects/x",
		"/api/board/p/tasks/fails", "/api/other/p"}
	methods := []string{"POST", "POST", "POST", "DELETE", "PATCH", "PUT", "GET"}
	hubUsers := [][]string{nil, {"hub-user-0000abcd"}, {"hub-user-0000abcd"}, {"hub-user-0000abcd", "hub-user-0000abce"},
		{"bad"}, {"HUB-USER-0000ABCD"}, {"hub-user-0000beef"}, {"hub-user-0000cafe"}, {""}}

	counts := map[string]int{}
	type outcome struct {
		code   int
		body   string
		seen   seen
		calls  int
		record *board.HubOperatorAction
	}
	lastID := func() int64 {
		a, err := bs.RecentHubOperatorActions(ctx, 1)
		require.NoError(t, err)
		if len(a) == 0 {
			return 0
		}
		return a[0].ID
	}
	for seed := int64(1); seed <= 80; seed++ {
		rng := rand.New(rand.NewSource(seed))
		for step := 0; step < 150; step++ {
			allow = rng.Intn(4) != 0
			tunneled := rng.Intn(7) != 0
			method := methods[rng.Intn(len(methods))]
			path := paths[rng.Intn(len(paths))]
			q := url.Values{}
			body := map[string]any{}
			fields := append(append([]string(nil), tunnelOperatorFields...), "receive_mode")
			for n := rng.Intn(4); n > 0; n-- {
				f := fields[rng.Intn(len(fields))]
				v := names[rng.Intn(len(names))]
				if f == "receive_mode" {
					v = []string{"all", " ALL ", "mentions", ""}[rng.Intn(4)]
				}
				switch rng.Intn(5) {
				case 0:
					q.Add(f, v)
				case 1:
					body[f] = 7 // not a string: not read as an identity
					if f != "receive_mode" && strings.EqualFold(strings.TrimSpace(v), operatorIdentity) {
						body[f] = []string{v}
					}
				default:
					body[f] = v
				}
			}
			if strings.HasSuffix(path, "/reminder") && rng.Intn(2) == 0 {
				body["subscriber_id"] = names[rng.Intn(len(names))] // the reminded subscriber
			}
			named := false // the request names the Operator in some identity field
			for _, f := range tunnelOperatorFields {
				for _, v := range q[f] {
					named = named || strings.EqualFold(strings.TrimSpace(v), operatorIdentity)
				}
				if s, ok := body[f].(string); ok {
					named = named || strings.EqualFold(strings.TrimSpace(s), operatorIdentity)
				}
			}
			raw, _ := json.Marshal(body)
			bodyKind := rng.Intn(10)
			headers := hubUsers[rng.Intn(len(hubUsers))]
			target := path
			if len(q) > 0 {
				target += "?" + q.Encode()
			}
			var outs [2]outcome
			for i, g := range guards {
				var rd io.Reader
				switch {
				case bodyKind == 0:
					rd = nil
				case bodyKind == 1:
					rd = strings.NewReader("not json")
				default:
					rd = bytes.NewReader(raw)
				}
				r := httptest.NewRequest(method, target, rd)
				if bodyKind == 2 {
					r.Body = erroringBody{}
				}
				if tunneled {
					stamp(r)
				}
				for _, u := range headers {
					r.Header.Add(tunnel.HubUserHeader, u)
				}
				got, allowedCalls = seen{}, 0
				before := lastID()
				rec := httptest.NewRecorder()
				g.ServeHTTP(rec, r)
				o := outcome{code: rec.Code, body: rec.Body.String(), seen: got, calls: allowedCalls}
				if a, _ := bs.RecentHubOperatorActions(ctx, 1); len(a) == 1 && a[0].ID > before {
					a[0].ID, a[0].At = 0, ""
					o.record = &a[0]
				}
				outs[i] = o
			}
			label := fmt.Sprintf("seed %d step %d: %s %s body %d hub %q allow %v tunneled %v", seed, step, method, target, bodyKind, headers, allow, tunneled)
			require.Equal(t, outs[0], outs[1], label+" (body %s)", raw)

			o := outs[1]
			var resp map[string]any
			_ = json.Unmarshal([]byte(o.body), &resp)
			code, _ := resp["code"].(string)
			msg, _ := resp["error"].(string)
			switch {
			case o.seen.passed && o.record == nil:
				counts["skipped (not a tunneled board write, a chat post)"]++
			case strings.Contains(o.body, "could not read the request body"):
				counts["body unreadable"]++
			case strings.Contains(msg, "receive every message"):
				counts["receive_mode all"]++
			case strings.Contains(msg, "act as an orchestrator ("):
				counts["orchestrator by name"]++
			case strings.Contains(msg, "this board's orchestrator"):
				counts["orchestrator by its subscription"]++
			case code == "operator_identity_required" && named:
				counts["Operator refused, named"]++
			case code == "operator_identity_required":
				counts["Operator refused, no actor named"]++
			case code == "member_identity":
				counts["existing member or role"]++
				for _, v := range q["job_title"] {
					if board.SameIdentity("Lead", strings.TrimSpace(v)) {
						counts["a job_title naming a peeking member (member, not orchestrator)"]++
					}
				}
				if s, ok := body["job_title"].(string); ok && board.SameIdentity("Lead", strings.TrimSpace(s)) {
					counts["a job_title naming a peeking member (member, not orchestrator)"]++
				}
			case code == "hub_identity_required":
				counts["made-up name"]++
			case code == "hub_user_required" && o.code == http.StatusForbidden:
				counts["hub user absent"]++
			case code == "hub_user_required" && o.code == http.StatusBadRequest:
				counts["hub user malformed or repeated"]++
			case code == "hub_user_required" && o.code == http.StatusConflict:
				counts["hub user is a member"]++
			}
			if o.record != nil {
				require.True(t, o.seen.passed, label)
				counts[map[int]string{200: "served as the Operator, its handler 200", 404: "served as the Operator, its handler 404"}[o.record.Status]]++
				if o.record.HubUser == "hub-user-0000cafe" {
					counts["served as the hub user with its own subscription"]++
				}
				if strings.HasSuffix(path, "/reminder") && (method == "POST" || method == "DELETE") {
					sub := q.Get("subscriber_id")
					if s, ok := body["subscriber_id"].(string); ok && bodyKind > 2 {
						sub = s
					}
					if v := strings.TrimSpace(sub); v != "" && !strings.EqualFold(v, operatorIdentity) {
						counts["served with a reminder's subscriber_id (a target)"]++
					}
				}
				if bodyKind > 2 && o.seen.body == string(raw) && len(body) > 0 {
					counts["served with the body put back for the handler"]++
				}
			}
		}
	}
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range tunnelGuardOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

// tunnelIdentityGuardBefore4756 is TunnelIdentityGuard as it was before #4756.
func tunnelIdentityGuardBefore4756(bs *board.Store, operatorAllowed func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			project, ok := tunnelBoardProject(r)
			// A cross-team message Coral Hub relayed from another Coral (#3489) is a
			// server's, not a Coral Hub user's: PostExternalMessage checks its source
			// server against the board's allowlist. Only an authentic relay skips this
			// guard; a forged X-Coral-Hub-Server still goes through it (and is refused).
			// Coral Hub's own notice about a team link (#3489) is the hub's, not a user's:
			// PostHubNotice accepts only that, with its fixed template.
			if !ok || bs == nil || isBoardChatPost(r) || isAuthenticRelay(r) || isAuthenticHubNotice(r) {
				next.ServeHTTP(w, r)
				return
			}
			values := map[string][]string{}
			for _, f := range tunnelOperatorFields {
				values[f] = append(values[f], r.URL.Query()[f]...)
			}
			receiveAll := false
			if r.Body != nil && r.Body != http.NoBody {
				raw, err := io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil {
					errBadRequest(w, "could not read the request body")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var body map[string]json.RawMessage
				if json.Unmarshal(raw, &body) == nil {
					for _, f := range tunnelOperatorFields {
						var s string
						if v, ok := body[f]; ok && json.Unmarshal(v, &s) == nil {
							values[f] = append(values[f], s)
						}
					}
					var mode string
					if v, ok := body["receive_mode"]; ok && json.Unmarshal(v, &mode) == nil {
						receiveAll = strings.EqualFold(strings.TrimSpace(mode), "all")
					}
				}
			}
			refuse := func(msg string) {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": msg, "code": "orchestrator_identity"})
			}
			if receiveAll {
				refuse("Through Coral Hub a subscriber can't receive every message (receive_mode all is the orchestrator's)")
				return
			}
			targetRoute := isBoardReminderWrite(r)
			var subs []board.Subscriber
			if got, err := bs.ListSubscribers(r.Context(), project); err == nil {
				subs = got
			}
			namesMemberOrRole := func(v string) bool {
				for _, sub := range subs {
					if board.SameIdentity(sub.SubscriberID, v) || board.SameIdentity(strings.TrimSpace(sub.JobTitle), v) {
						return true
					}
				}
				return false
			}
			asOperator, anyActor := false, false
			// First every orchestrator and Operator rule, over all the fields...
			for _, f := range tunnelOperatorFields {
				for _, v := range values[f] {
					v = strings.TrimSpace(v)
					if v == "" {
						continue
					}
					if strings.EqualFold(v, operatorIdentity) {
						if operatorAllowed == nil || !operatorAllowed(r) {
							writeJSON(w, http.StatusForbidden, map[string]any{"code": "operator_identity_required",
								"error": "Through Coral Hub you can't act as the Operator: 'Don't let Coral Hub act as the Operator' is on in Settings on this computer"})
							return
						}
						asOperator = true
						anyActor = anyActor || f != "assignee" && f != "assigned_to"
						continue
					}
					if f == "assignee" || f == "assigned_to" {
						continue // who a task is for: anyone but the Operator rule above
					}
					if board.IsOrchestrator(v, "", 0) {
						refuse("Through Coral Hub you can't act as an orchestrator (" + f + ")")
						return
					}
					if f == "job_title" {
						continue
					}
					if sub, err := bs.GetProjectSubscription(r.Context(), project, v); err == nil && sub != nil && board.IsOrchestrator(sub.SubscriberID, sub.JobTitle, sub.CanPeek) {
						refuse("Through Coral Hub you can't act as this board's orchestrator (" + f + ")")
						return
					}
				}
			}
			// ...then any other identity: an existing member or role (409), or a made-up name (403).
			for _, f := range tunnelActorFields {
				for _, v := range values[f] {
					v = strings.TrimSpace(v)
					if v == "" || strings.EqualFold(v, operatorIdentity) {
						continue
					}
					if f == "subscriber_id" && targetRoute {
						continue // the reminded subscriber: a target, not an actor
					}
					anyActor = true
					if namesMemberOrRole(v) {
						slog.Warn("[cloud] Coral Hub write refused: it named an existing member or role", "field", f, "method", r.Method, "path", r.URL.Path)
						writeJSON(w, http.StatusConflict, map[string]any{"code": "member_identity",
							"error": "That name is already a member or role on this board: through Coral Hub a write can't be made as an existing member (" + f + ")"})
						return
					}
					writeJSON(w, http.StatusForbidden, map[string]any{"code": "hub_identity_required",
						"error": "Through Coral Hub a board write is made as the Operator (or a chat post as the Coral Hub user); this name is neither (" + f + ")"})
					return
				}
			}
			if !anyActor {
				// A write that names no actor is the Operator's (pause, resume, a reminder).
				if operatorAllowed == nil || !operatorAllowed(r) {
					writeJSON(w, http.StatusForbidden, map[string]any{"code": "operator_identity_required",
						"error": "Through Coral Hub you can't act as the Operator: 'Don't let Coral Hub act as the Operator' is on in Settings on this computer"})
					return
				}
				asOperator = true
			}
			if asOperator {
				user, present, status, msg := tunnelHubUser(r, bs, project)
				if !present {
					writeJSON(w, http.StatusForbidden, map[string]any{"code": "hub_user_required",
						"error": "Acting as the Operator through Coral Hub needs the Coral Hub user (" + tunnel.HubUserHeader + "); this hub does not send it"})
					return
				}
				if status != 0 {
					writeJSON(w, status, map[string]any{"error": msg, "code": "hub_user_required"})
					return
				}
				rec := &statusRecorder{ResponseWriter: w}
				next.ServeHTTP(rec, withHubOperator(r, user))
				if rec.status == 0 {
					rec.status = http.StatusOK
				}
				recordHubOperatorAction(r.Context(), bs, user, r.Method, r.URL.Path, project, rec.status)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
