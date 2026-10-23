package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/store"
)

// The outcomes the PostExternalMessage oracle must reach, each counted from the answer, with a floor.
var externalMessageOutcomes = map[string]int{
	"invalid JSON":                          5,
	"no content":                            10,
	"content too large":                     5,
	"no sender board or subscriber":         10,
	"its own board":                         5,
	"hub Operator without a hub user":       5,
	"sender board unknown":                  5,
	"sender not an Orchestrator":            10,
	"session doesn't match":                 10,
	"target unknown":                        5,
	"messaging off on a board":              10,
	"no Orchestrator to receive":            5,
	"named recipient unknown":               5,
	"rate limited":                          10,
	"artifacts off":                         5,
	"an attachment refused":                 5,
	"delivered":                             60,
	"delivered to the one named":            10,
	"delivered by the Operator":             10,
	"delivered by the hub Operator, marked": 5,
	"delivered with artifacts, recorded":    10,
	"line breaks unescaped":                 10,
}

// #4838: PostExternalMessage split into externalMessage.refusal and hubOperatorPrefix, crossTeamSenderRole,
// crossTeamRecipients, crossTeamAttachments and recordCrossTeamArtifacts, in the same order. Two handlers, each with
// its own boards and artifact store, take the same random cross-team sends in lockstep: the old handler and the new
// one answer the same, post the same messages on every board, write the same artifact records, notify the same
// boards and spend the same quota.
func TestPostExternalMessageAgreesWithBefore4838(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	counts := map[string]int{}
	for seed := int64(1); seed <= 40; seed++ {
		runExternalMessageOracle(t, seed, counts)
	}
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range externalMessageOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

type externalSide struct {
	h        *BoardHandler
	coralDir string
	mux      http.Handler
	notified *[]string
}

func runExternalMessageOracle(t *testing.T, seed int64, counts map[string]int) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	artifactsOff := map[string]bool{"alpha": rng.Intn(4) == 0, "beta": rng.Intn(4) == 0} // each side of a send can refuse files
	clock := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	const deltaQA = "dddd5555-0000-4000-8000-000000000005"
	var sides [2]externalSide
	var uploads []string
	for i := range sides {
		bs, err := boardtest.Open(t.TempDir() + "/board.db")
		require.NoError(t, err)
		t.Cleanup(func() { bs.Close() })
		h := NewBoardHandler(bs)
		coralDir := t.TempDir()
		h.SetTaskArtifactWriter(coralDir, nil)
		h.crossTeam.now = func() time.Time { return clock }
		var notified []string
		var mu sync.Mutex
		h.notifyFn = func(b string) { mu.Lock(); notified = append(notified, b); mu.Unlock() }
		mux := chi.NewRouter()
		mux.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if sid := r.Header.Get("X-Test-Session"); sid != "" {
					r = r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid, SessionName: "claude-" + sid}))
					r = withActor(r, Actor{Kind: ActorToken, Name: r.Header.Get("X-Test-Name"), Session: "claude-" + sid})
				}
				if r.Header.Get("X-Test-Operator") != "" {
					r = withActor(r, Actor{Kind: ActorOperator, Name: "Operator"})
				}
				if r.Header.Get("X-Test-Tunnel") != "" {
					stamp(r)
					if u := r.Header.Get("X-Test-Hub-User"); u != "" {
						r = withHubOperator(r, u)
					}
				}
				next.ServeHTTP(w, r)
			})
		})
		if i == 0 {
			mux.Post("/api/board/{project}/external-messages", h.postExternalMessageBefore4838)
		} else {
			mux.Post("/api/board/{project}/external-messages", h.PostExternalMessage)
		}
		for _, s := range []struct{ board, id, role, sid string }{
			{"alpha", "Orchestrator", "Orchestrator", alphaOrchSID},
			{"alpha", "Dev", "Backend Dev", alphaDevSID},
			{"beta", "Orchestrator", "Orchestrator", betaOrchSID},
			{"beta", "QA", "QA Engineer", betaQASID},
			{"gamma", "Orchestrator", "Orchestrator", "cccc4444-0000-4000-8000-000000000004"},
			{"delta", "QA", "QA Engineer", deltaQA},
			{"epsilon", "Orchestrator", "Orchestrator", "eeee6666-0000-4000-8000-000000000006"},
		} {
			_, err := bs.Subscribe(ctx, s.board, s.id, s.role, "claude-"+s.sid, nil, "mentions")
			require.NoError(t, err)
		}
		for _, b := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
			_, err := bs.UpdateBoardSettings(ctx, b, board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(b != "gamma"), CrossTeamArtifacts: ptrBool(!artifactsOff[b])})
			require.NoError(t, err)
		}
		uploads = []string{
			writeUpload(t, coralDir, alphaOrchSID, fmt.Sprintf("# report %d\nnumbers\n", seed), "report.md", afterJoin()),
			writeUpload(t, coralDir, alphaOrchSID, "# second\nmore lines\n", "second.md", afterJoin()),
			writeUpload(t, coralDir, betaQASID, "# beta's own\n", "beta.md", afterJoin()), // another team's upload
		}
		sides[i] = externalSide{h, coralDir, mux, &notified}
	}

	for n := 0; n < 60; n++ {
		if rng.Intn(3) == 0 {
			clock = clock.Add(time.Duration(rng.Intn(15)) * time.Second)
		}
		// A send from alpha's Orchestrator to beta, then one or two of its parts changed.
		target := []string{"beta", "beta", "epsilon"}[rng.Intn(3)] // two receiving boards: the quota is per target
		body := map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "please run the check"}
		who := "orch"
		for k := rng.Intn(3); k > 0; k-- {
			switch rng.Intn(10) {
			case 0:
				target = []string{"alpha", "gamma", "delta", "ghost", "beta"}[rng.Intn(5)]
			case 1:
				body["from_board"] = []string{"", "nope", "beta", "gamma", " alpha "}[rng.Intn(5)]
			case 2:
				body["subscriber_id"], who = []string{"Dev", "Operator", "Ghost", ""}[rng.Intn(4)], "none"
				if body["subscriber_id"] == "Operator" {
					who = []string{"operator", "tunnel-user", "tunnel-none"}[rng.Intn(3)]
				}
			case 3:
				body["session_name"] = []string{"", "claude-" + alphaDevSID, "claude-elsewhere"}[rng.Intn(3)]
			case 4:
				body["content"] = []string{"", "   ", strings.Repeat("z", maxCrossTeamContent+1), "line one\\nline two\\nline three", "token=ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R"}[rng.Intn(5)]
			case 5:
				body["to"] = []string{"Orchestrator", "QA", "Nobody", " Orchestrator "}[rng.Intn(4)]
			case 6, 7:
				body["attach"] = [][]string{{uploads[0]}, {uploads[0], uploads[1]}, {uploads[0], uploads[0]}, {"0000"}, {uploads[2]}}[rng.Intn(5)]
				for j, id := range body["attach"].([]string) {
					body["attach"].([]string)[j] = "coral://artifacts/" + id
				}
			case 8: // the Operator names a board that doesn't exist
				body["from_board"], body["subscriber_id"], body["session_name"], who = "nope", "Operator", "", "operator"
			default:
				who = []string{"orch", "dev", "none"}[rng.Intn(3)]
			}
		}
		raw, _ := json.Marshal(body)
		if rng.Intn(40) == 0 {
			raw = []byte("{nope")
		}
		type result struct {
			code     int
			body     string
			msgs     []string
			records  string
			notified []string
		}
		var results [2]result
		for i, s := range sides {
			*s.notified = nil
			req := httptest.NewRequest(http.MethodPost, "/api/board/"+target+"/external-messages", bytes.NewReader(raw))
			switch who {
			case "orch":
				req.Header.Set("X-Test-Session", alphaOrchSID)
				req.Header.Set("X-Test-Name", "Orchestrator")
			case "dev":
				req.Header.Set("X-Test-Session", alphaDevSID)
				req.Header.Set("X-Test-Name", "Dev")
			case "operator":
				req.Header.Set("X-Test-Operator", "1")
			case "tunnel-user":
				req.Header.Set("X-Test-Tunnel", "1")
				req.Header.Set("X-Test-Hub-User", "hub-user-0000abcd")
			case "tunnel-none":
				req.Header.Set("X-Test-Tunnel", "1")
			}
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, req)
			s.h.WaitBackground()
			var msgs []string
			for _, b := range []string{"alpha", "beta", "gamma", "delta", "epsilon"} {
				all, err := s.h.bs.ListMessages(ctx, b, 1000, 0, 0)
				require.NoError(t, err)
				for _, m := range all {
					msgs = append(msgs, fmt.Sprintf("%s %d %s: %s", b, m.ID, m.SubscriberID, m.Content))
				}
			}
			var rb strings.Builder
			for _, kind := range []string{"shared", "received"} {
				for _, b := range []string{"alpha", "beta"} {
					j, _ := json.Marshal(s.h.crossTeamRecords(kind, b))
					fmt.Fprintf(&rb, "%s/%s %s;", kind, b, oracleStamp.ReplaceAllString(string(j), "<t>"))
				}
			}
			results[i] = result{rec.Code, oracleStamp.ReplaceAllString(oracleCrossTeamID.ReplaceAllString(rec.Body.String(), ""), "<t>"), msgs, rb.String(), append([]string(nil), *s.notified...)}
		}
		label := fmt.Sprintf("seed %d step %d: to %s %s who %s artifacts off %v", seed, n, target, raw, who, artifactsOff)
		require.Equal(t, results[0], results[1], label)

		r := results[1]
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(r.body), &refusal)
		switch {
		case strings.Contains(r.body, "invalid JSON"):
			counts["invalid JSON"]++
		case strings.Contains(r.body, "content required"):
			counts["no content"]++
		case strings.Contains(r.body, "content must be at most"):
			counts["content too large"]++
		case strings.Contains(r.body, "from_board and subscriber_id are required"):
			counts["no sender board or subscriber"]++
		case strings.Contains(r.body, "message your own board"):
			counts["its own board"]++
		case strings.Contains(r.body, "act as the Operator here"):
			counts["hub Operator without a hub user"]++
		case strings.Contains(r.body, "only an active Orchestrator"):
			counts["sender not an Orchestrator"]++
		case strings.Contains(r.body, "session_name does not match"):
			counts["session doesn't match"]++
		case strings.Contains(r.body, "Cross-team messaging is off on"):
			counts["messaging off on a board"]++
		case strings.Contains(r.body, "no active Orchestrator to receive"):
			counts["no Orchestrator to receive"]++
		case strings.Contains(r.body, "too many messages"):
			counts["rate limited"]++
		case strings.Contains(r.body, "artifacts_off"):
			counts["artifacts off"]++
		case r.code == http.StatusNotFound && refusal.Error == fmt.Sprintf("board %q not found", strings.TrimSpace(fmt.Sprint(body["from_board"]))):
			counts["sender board unknown"]++
		case r.code == http.StatusNotFound && refusal.Error == fmt.Sprintf("board %q not found", target):
			counts["target unknown"]++
		case r.code == http.StatusNotFound && body["to"] != nil:
			counts["named recipient unknown"]++
		case r.code >= 400 && body["attach"] != nil:
			counts["an attachment refused"]++
		}
		if r.code != http.StatusOK {
			continue
		}
		counts["delivered"]++
		var out struct {
			DeliveredTo []string `json:"delivered_to"`
			Attached    []string `json:"attached"`
			Message     struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		require.NoError(t, json.Unmarshal([]byte(r.body), &out))
		if to, _ := body["to"].(string); strings.TrimSpace(to) != "" && len(out.DeliveredTo) == 1 {
			counts["delivered to the one named"]++
		}
		if who == "operator" {
			counts["delivered by the Operator"]++
		}
		if who == "tunnel-user" && strings.Contains(out.Message.Content, "hub-user-0000abcd") {
			counts["delivered by the hub Operator, marked"]++
		}
		if len(out.Attached) > 0 && strings.Contains(r.records, `"id"`) {
			counts["delivered with artifacts, recorded"]++
		}
		if strings.Contains(out.Message.Content, "line one\nline two\nline three") {
			counts["line breaks unescaped"]++
		}
	}
}

// postExternalMessageBefore4838 is PostExternalMessage as it was before #4838.
func (h *BoardHandler) postExternalMessageBefore4838(w http.ResponseWriter, r *http.Request) {
	if src, present := relaySource(r); present {
		h.postRelayedExternalMessage(w, r, src)
		return
	}
	target := urlParam(r, "project")
	var body struct {
		FromBoard    string `json:"from_board"`
		SubscriberID string `json:"subscriber_id"`
		// SessionName is the caller's own session (coral-board sends
		// CORAL_SESSION_NAME). It must match the sender's subscription: a
		// speed bump against sending as another agent, not authentication;
		// local identity is trusted at the OS-user level.
		SessionName string `json:"session_name"`
		Content     string `json:"content"`
		To          string `json:"to"`
		// Attach are coral://artifacts links sent with the message (#4379): see resolveAttachments.
		Attach []string `json:"attach"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.FromBoard = strings.TrimSpace(body.FromBoard)
	switch {
	case body.Content == "":
		errBadRequest(w, "content required")
		return
	case len(body.Content) > maxCrossTeamContent:
		errBadRequest(w, fmt.Sprintf("content must be at most %d bytes", maxCrossTeamContent))
		return
	case body.FromBoard == "" || body.SubscriberID == "":
		errBadRequest(w, "from_board and subscriber_id are required")
		return
	case body.FromBoard == target:
		errBadRequest(w, "use a normal board post to message your own board")
		return
	}
	ctx := r.Context()

	// The Operator through Coral Hub (#3404): admitted by TunnelIdentityGuard with the
	// Coral Hub user, and the text says so, so the receiving Orchestrator can tell it
	// from the local Operator.
	if body.SubscriberID == crossTeamOperatorID && cloud.Tunneled(r) {
		hubUser := hubOperatorUser(r)
		if hubUser == "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"code": "operator_identity_required", "error": "Through Coral Hub you can't act as the Operator here"})
			return
		}
		body.Content = HubOperatorPrefix(hubUser) + body.Content
	}

	// Only Orchestrators and the Operator may send.
	role, senderSession, refusal, err := h.checkCrossTeamSender(ctx, body.FromBoard, body.SubscriberID, body.SessionName)
	switch {
	case err != nil:
		errInternalServer(w, err.Error())
		return
	case refusal == senderNoBoard:
		errNotFound(w, fmt.Sprintf("board %q not found", body.FromBoard))
		return
	case refusal == senderNotOrchestrator:
		errForbidden(w, fmt.Sprintf("only an active Orchestrator of %q or the Operator can send cross-team messages", body.FromBoard))
		return
	case refusal == senderSessionMismatch:
		errForbidden(w, fmt.Sprintf("session_name does not match %q's subscription on %q; send from that agent's own session", body.SubscriberID, body.FromBoard))
		return
	}

	// An unknown target is a typo, not a disabled board.
	if ok, err := h.bs.BoardExists(ctx, target); err != nil {
		errInternalServer(w, err.Error())
		return
	} else if !ok {
		errNotFound(w, fmt.Sprintf("board %q not found", target))
		return
	}

	// Both boards must have opted in.
	var disabled []string
	for _, b := range []string{body.FromBoard, target} {
		settings, err := h.bs.GetBoardSettings(ctx, b)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if !settings.CrossTeamMessaging {
			disabled = append(disabled, strconv.Quote(b))
		}
	}
	if len(disabled) > 0 {
		errForbidden(w, fmt.Sprintf("Cross-team messaging is off on %s. Turn it on in Team settings > Messaging.", strings.Join(disabled, " and ")))
		return
	}

	recipients, err := h.bs.TargetOrchestrators(ctx, target, strings.TrimSpace(body.To))
	if err != nil {
		if errors.Is(err, board.ErrNoTargetOrchestrator) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("board %q has no active Orchestrator to receive the message", target)})
			return
		}
		errNotFound(w, err.Error())
		return
	}

	if secs, limited := h.crossTeamWait(body.FromBoard + "\x00" + body.SubscriberID + "\x00" + target); limited {
		// A limit on this sender's posts, not a busy server (#3434).
		httputil.WriteActionLimit(w, secs, "rate_limited", fmt.Sprintf("too many messages to %q; retry in %ds", target, secs))
		return
	}

	// Attachments (#4379): both boards must accept artifacts, and every one is checked and read
	// through before anything is posted.
	var attached []attachedArtifact
	if len(body.Attach) > 0 {
		var off []string
		for _, b := range []string{body.FromBoard, target} {
			settings, err := h.bs.GetBoardSettings(ctx, b)
			if err != nil {
				errInternalServer(w, err.Error())
				return
			}
			if !settings.CrossTeamArtifacts {
				off = append(off, strconv.Quote(b))
			}
		}
		if len(off) > 0 {
			writeJSON(w, http.StatusForbidden, map[string]string{"code": "artifacts_off", "error": fmt.Sprintf("Sharing files with other teams is off on %s. Ask the Operator to turn on Share files with teams on other computers in Team settings > Messaging%s (the setting also covers files between teams on this computer). Nothing was sent.", strings.Join(off, " and "), eachIfMany(off))})
			return
		}
		sender := h.attachSenderFor(ctx, r, body.FromBoard, body.SubscriberID, senderSession)
		var refused *attachRefusal
		if attached, refused = h.resolveAttachments(body.Attach, sender); refused != nil {
			refused.write(w)
			return
		}
		body.Content += attachmentLines(attached)
	}

	inbound, outbound, err := h.bs.PostCrossTeamMessage(ctx, board.CrossTeamPost{
		Origin: board.MessageOrigin{
			ServerID: store.LocalServerID, Board: body.FromBoard, Sender: body.SubscriberID, SenderRole: role,
			TargetServerID: store.LocalServerID, TargetBoard: target,
		},
		Mentions: recipients,
		Content:  body.Content,
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.notifyFn != nil {
		// Both boards got a message: the target the inbound one, the sender's the outbound copy.
		h.notifyFn(target)
		h.notifyFn(body.FromBoard)
	}
	h.goBG(func() { h.resolveMessageWaits(target, inbound.SubscriberID, inbound.Content) })
	resp := map[string]any{"message": inbound, "mirror": outbound, "delivered_to": recipients}
	if len(attached) > 0 {
		now := time.Now().UTC().Format(time.RFC3339)
		var ids []string
		for _, a := range attached {
			base := crossTeamArtifactRecord{ID: a.ID, Name: a.Name, MediaType: a.MediaType, Size: a.Size, Server: "local", By: body.SubscriberID, At: now}
			shared, received := base, base
			shared.Board, shared.OtherBoard, shared.MessageID = body.FromBoard, target, outbound.ID
			received.Board, received.OtherBoard, received.MessageID = target, body.FromBoard, inbound.ID
			if err := h.writeCrossTeamRecord("shared", shared); err != nil {
				slog.Warn("cross-team artifact: could not record the shared row", "board", body.FromBoard, "id", a.ID, "error", err)
			}
			if err := h.writeCrossTeamRecord("received", received); err != nil {
				slog.Warn("cross-team artifact: could not record the received row", "board", target, "id", a.ID, "error", err)
			}
			ids = append(ids, "coral://artifacts/"+a.ID)
		}
		resp["attached"] = ids
	}
	writeJSON(w, http.StatusOK, resp)
}
