package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/board/boardtest"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// The outcomes the PostRelayMessage oracle must reach, each counted from the condition happening, with a floor.
var relayOutcomes = map[string]int{
	"tunneled (local only)":              10,
	"invalid JSON":                       10,
	"no sender or text":                  20,
	"text too large":                     10,
	"address names an agent":             10,
	"address malformed":                  10,
	"message_id malformed":               10,
	"sender refused":                     20,
	"secret in the text":                 10,
	"reply to a relayed message (hops)":  20,
	"reply to a local message":           10,
	"reply chain too long":               10,
	"not linked (no relay)":              10,
	"attachment refused before sending":  10,
	"text and links too large":           5,
	"rate limited":                       10,
	"quota probe limited":                20,
	"attachment refused by the hub":      10,
	"files not relayed (quota refunded)": 10,
	"hub unreachable or not linked":      20,
	"hub refused (code kept)":            10,
	"hub refused (code replaced)":        10,
	"hub refused, odd status (502)":      5,
	"sent, mirror stored":                40,
	"sent with files, shared recorded":   10,
	"duplicate, first mirror kept":       10,
	"duplicate, mirror stored now":       5,
}

var oracleUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// relayStep is what the fake Coral Hub answers this step, the same on both sides.
type relayStep struct {
	msg     func() (tunnel.RelayResponse, error)
	failAt  int  // the file the hub refuses (-1: none)
	noFiles bool // the hub doesn't relay files at all (refundable: nothing reached it)
	calls   []string
	callsMu sync.Mutex
}

// #4756: PostRelayMessage split into relayRequest.refusal, relayHops, relayAttachments, sendRelay and
// relayDelivered. Two handlers, each with its own board, artifact store and fake Coral Hub, take the same random
// sends in lockstep: the old handler and the new one answer the same, send Coral Hub the same files and message,
// store the same mirror and shared records, and spend the same quota.
func TestPostRelayMessageAgreesWithBefore4756(t *testing.T) {
	prevEnforced := identityActorEnforced()
	t.Cleanup(func() { setIdentityActorEnforced(prevEnforced) })
	counts := map[string]int{}
	for seed := int64(1); seed <= 60; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runRelayOracle(t, seed, counts) })
	}
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range relayOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

func runRelayOracle(t *testing.T, seed int64, counts map[string]int) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(seed))
	var step *relayStep
	clock := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	type side struct {
		h        *BoardHandler
		bs       *board.Store
		coralDir string
		mux      http.Handler
	}
	var sides [2]side
	var notified [2][]string // the boards each side asked to notify
	for i := range sides {
		bs, err := boardtest.Open(t.TempDir() + "/board.db")
		require.NoError(t, err)
		t.Cleanup(func() { bs.Close() })
		h := NewBoardHandler(bs)
		coralDir := t.TempDir()
		h.SetTaskArtifactWriter(coralDir, nil)
		h.SetRelay(func(_ context.Context, path string, raw []byte) (tunnel.RelayResponse, error) {
			step.callsMu.Lock()
			step.calls = append(step.calls, path+" "+oracleUUID.ReplaceAllString(string(raw), "<id>"))
			step.callsMu.Unlock()
			return step.msg()
		})
		h.SetRelayArtifact(func(_ context.Context, n int64, body io.Reader) (tunnel.RelayResponse, error) {
			raw, _ := io.ReadAll(body)
			step.callsMu.Lock()
			i := 0
			for _, c := range step.calls {
				if strings.HasPrefix(c, "file ") {
					i++
				}
			}
			step.calls = append(step.calls, fmt.Sprintf("file %d %s", n, oracleUUID.ReplaceAllString(string(raw), "<id>")))
			step.callsMu.Unlock()
			if step.noFiles {
				if n%2 == 0 {
					return tunnel.RelayResponse{}, cloud.ErrNotLinked
				}
				return tunnel.RelayResponse{}, tunnel.ErrRelayArtifactUnsupported
			}
			if i == step.failAt {
				return tunnel.RelayResponse{Status: 403, Body: []byte(`{"ok":false,"error":"artifact_refused"}`)}, nil
			}
			return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true}`)}, nil
		})
		h.crossTeam.now = func() time.Time { return clock }
		h.notifyFn = func(project string) { notified[i] = append(notified[i], project) }
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
				next.ServeHTTP(w, r)
			})
		})
		if i == 0 {
			mux.Post("/api/board/{project}/relay-messages", h.postRelayMessageBefore4756)
		} else {
			mux.Post("/api/board/{project}/relay-messages", h.PostRelayMessage)
		}
		for _, s := range []struct{ board, id, role, sid string }{
			{"alpha", "Orchestrator", "Orchestrator", alphaOrchSID},
			{"alpha", "Dev", "Backend Dev", betaQASID},
			{"beta", "Orchestrator", "Orchestrator", betaOrchSID},
		} {
			_, err := bs.Subscribe(ctx, s.board, s.id, s.role, "claude-"+s.sid, nil, "mentions")
			require.NoError(t, err)
		}
		for _, b := range []string{"alpha", "beta"} {
			_, err := bs.UpdateBoardSettings(ctx, b, board.BoardSettingsUpdate{CrossTeamMessaging: ptrBool(b == "alpha" || rng.Intn(1) == 0), CrossTeamArtifacts: ptrBool(true)})
			require.NoError(t, err)
		}
		sides[i] = side{h, bs, coralDir, mux}
	}
	// The same artifacts (ids are digests) and the same relayed messages to reply to, on both sides.
	var artifacts []string
	for k := 0; k < 3; k++ {
		content := fmt.Sprintf("# report %d\nnumbers %d\n", k, seed)
		if k == 2 {
			content = "# big\n" + strings.Repeat("y", 64) + "\n"
		}
		var id string
		for _, s := range sides {
			id = writeUpload(t, s.coralDir, alphaOrchSID, content, fmt.Sprintf("file-%d.md", k), afterJoin())
		}
		artifacts = append(artifacts, id)
	}
	var relayed, local []int64
	for hops := 0; hops <= 3; hops++ {
		var id, lid int64
		for _, s := range sides {
			in, _, err := s.bs.PostRelayedCrossTeamMessage(ctx, board.CrossTeamPost{
				Origin: board.MessageOrigin{ServerID: "0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00", Board: "qa", Sender: "Orchestrator", SenderRole: "Orchestrator",
					TargetServerID: "local", TargetBoard: "alpha", RelayMessageID: fmt.Sprintf("in-%d", hops), Hops: hops},
				Mentions: []string{"Orchestrator"}, Content: "can you check?",
			})
			require.NoError(t, err)
			id = in.ID
			m, err := s.bs.PostMessage(ctx, "alpha", "Dev", "a local note", nil)
			require.NoError(t, err)
			lid = m.ID
		}
		relayed, local = append(relayed, id), append(local, lid)
	}

	usedIDs := []string{}
	for n := 0; n < 70; n++ {
		if rng.Intn(3) == 0 && n < 65 {
			clock = clock.Add(time.Duration(rng.Intn(20)) * time.Second)
		}
		enforced := rng.Intn(5) == 0
		// A valid send from the orchestrator, then one or two of its parts changed.
		who := 0 // 0 the orchestrator's token, 1 Dev's token, 2 the Operator, 3 nothing verified
		body := map[string]any{"subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID,
			"content": "please run the check", "to_address": "homelab/qa"}
		for k := rng.Intn(3); k > 0; k-- {
			switch rng.Intn(5) {
			case 0:
				who = rng.Intn(4)
			case 1:
				body["subscriber_id"] = []string{"Dev", "Operator", "", "Ghost"}[rng.Intn(4)]
			case 2:
				body["session_name"] = []string{"claude-" + betaQASID, "", "claude-elsewhere"}[rng.Intn(3)]
			case 3:
				body["content"] = []string{"  ", "", strings.Repeat("z", maxRelayedContent+1), strings.Repeat("z", maxRelayedContent-40),
					"deploy with token=ghp_1a2B3c4D5e6F7g8H9i0J1k2L3m4N5o6P7q8R", "line one\\nline two"}[rng.Intn(6)]
			default:
				body["to_address"] = []string{" homelab/qa ", "homelab/qa/agent", "nope", "id-0123456789ab/qa"}[rng.Intn(4)]
			}
		}
		if rng.Intn(3) == 0 {
			switch rng.Intn(4) {
			case 0:
				body["reply_to"] = relayed[rng.Intn(len(relayed))]
			case 1:
				body["reply_to"] = local[rng.Intn(len(local))]
			case 2:
				body["reply_to"] = 999999
			default:
				body["reply_to"] = relayed[rng.Intn(len(relayed))]
			}
		}
		switch rng.Intn(5) {
		case 0:
			if len(usedIDs) > 0 {
				body["message_id"] = usedIDs[rng.Intn(len(usedIDs))] // a retry
			}
		case 1:
			id := fmt.Sprintf("msg-%d-%d", seed, n)
			usedIDs = append(usedIDs, id)
			body["message_id"] = id
		case 2:
			body["message_id"] = "bad id!"
		}
		if rng.Intn(4) == 0 {
			var attach []string
			for k := rng.Intn(3); k >= 0; k-- {
				attach = append(attach, "coral://artifacts/"+artifacts[rng.Intn(len(artifacts))])
			}
			if rng.Intn(6) == 0 {
				attach = append(attach, "coral://artifacts/0000")
			}
			body["attach"] = attach
		}
		raw, _ := json.Marshal(body)
		if rng.Intn(30) == 0 {
			raw = []byte("{nope")
		}
		tunneled := rng.Intn(25) == 0
		noRelay := rng.Intn(25) == 0
		failAt, noFiles := -1, rng.Intn(6) == 0
		if rng.Intn(5) == 0 {
			failAt = rng.Intn(2)
		}
		hubKind := rng.Intn(12)
		if id, ok := body["message_id"].(string); ok && strings.HasPrefix(id, "msg-") && rng.Intn(2) == 0 {
			hubKind = 6 // a retry: Coral Hub has it already
		}
		probe := n >= 65 // the seed ends with plain sends on a stopped clock: the quota left decides when they are limited
		if probe {
			body = map[string]any{"subscriber_id": "Orchestrator", "session_name": "claude-" + alphaOrchSID, "content": "please run the check", "to_address": "homelab/qa"}
			raw, _ = json.Marshal(body)
			who, enforced, tunneled, noRelay, failAt, noFiles, hubKind = 0, false, false, false, -1, false, 8
		}
		target := fmt.Sprint(100 + n)
		answer := func() (tunnel.RelayResponse, error) {
			switch hubKind {
			case 0:
				return tunnel.RelayResponse{}, cloud.ErrNotLinked
			case 1:
				return tunnel.RelayResponse{}, tunnel.ErrRelayUnsupported
			case 2:
				return tunnel.RelayResponse{}, errors.New("dial: connection refused")
			case 3:
				return tunnel.RelayResponse{Status: 429, RetryAfter: 7, Body: []byte(`{"ok":false,"error":"rate_limited","reason":"target_busy"}`)}, nil
			case 4:
				return tunnel.RelayResponse{Status: 403, Body: []byte(`{"ok":false,"error":"Not Allowed: free text","reason":"x"}`)}, nil
			case 5:
				return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":false,"error":"denied"}`)}, nil
			case 6, 7:
				return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"duplicate":true,"target_message_id":"` + target + `"}`)}, nil
			default:
				return tunnel.RelayResponse{Status: 200, Body: []byte(`{"ok":true,"target_message_id":` + target + `}`)}, nil
			}
		}

		type result struct {
			code   int
			retry  string
			body   string
			calls  []string
			msgs   []string
			shared string
			notify []string
		}
		var results [2]result
		for i, s := range sides {
			step = &relayStep{msg: answer, failAt: failAt, noFiles: noFiles}
			setIdentityActorEnforced(enforced)
			if noRelay {
				s.h.SetRelay(nil)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/board/alpha/relay-messages", bytes.NewReader(raw))
			switch who {
			case 0:
				req.Header.Set("X-Test-Session", alphaOrchSID)
				req.Header.Set("X-Test-Name", "Orchestrator")
			case 1:
				req.Header.Set("X-Test-Session", betaQASID)
				req.Header.Set("X-Test-Name", "Dev")
			case 2:
				req.Header.Set("X-Test-Operator", "1")
			}
			if tunneled {
				stamp(req)
			}
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, req)
			s.h.WaitBackground()
			if noRelay {
				st := step
				s.h.SetRelay(func(_ context.Context, path string, raw []byte) (tunnel.RelayResponse, error) {
					_ = st
					step.callsMu.Lock()
					step.calls = append(step.calls, path+" "+oracleUUID.ReplaceAllString(string(raw), "<id>"))
					step.callsMu.Unlock()
					return step.msg()
				})
			}
			msgs, err := s.bs.ListMessages(ctx, "alpha", 1000, 0, 0)
			require.NoError(t, err)
			var texts []string
			for _, m := range msgs {
				texts = append(texts, fmt.Sprintf("%d %s: %s", m.ID, m.SubscriberID, m.Content))
			}
			sh, _ := json.Marshal(s.h.crossTeamRecords("shared", "alpha"))
			results[i] = result{rec.Code, rec.Header().Get("Retry-After"),
				oracleStamp.ReplaceAllString(oracleUUID.ReplaceAllString(rec.Body.String(), "<id>"), "<t>"),
				step.calls, texts, oracleStamp.ReplaceAllString(string(sh), "<t>"), notified[i]}
		}
		label := fmt.Sprintf("seed %d step %d: %s who %d enforced %v tunneled %v norelay %v hub %d failAt %d nofiles %v", seed, n, raw, who, enforced, tunneled, noRelay, hubKind, failAt, noFiles)
		require.Equal(t, results[0], results[1], label)

		r := results[1]
		var resp map[string]any
		_ = json.Unmarshal([]byte(r.body), &resp)
		code, _ := resp["error"].(string)
		msg, _ := resp["message"].(string)
		if msg == "" {
			msg, _ = resp["detail"].(string)
		}
		files := 0
		for _, c := range r.calls {
			if strings.HasPrefix(c, "file ") {
				files++
			}
		}
		sentMsg := false
		for _, c := range r.calls {
			sentMsg = sentMsg || strings.HasPrefix(c, tunnel.RelayMessagePath)
		}
		_, hasReply := body["reply_to"]
		switch {
		case code == "local_only":
			counts["tunneled (local only)"]++
		case r.code == 400 && strings.Contains(r.body, "invalid JSON"):
			counts["invalid JSON"]++
		case strings.Contains(r.body, "subscriber_id and content are required"):
			counts["no sender or text"]++
		case code == "too_large" && strings.Contains(r.body, "attachment links"):
			counts["text and links too large"]++
		case code == "too_large":
			counts["text too large"]++
		case strings.Contains(r.body, "addressing an agent"):
			counts["address names an agent"]++
		case code == "bad_address":
			counts["address malformed"]++
		case strings.Contains(r.body, "malformed message_id"):
			counts["message_id malformed"]++
		case code == "message_secret":
			counts["secret in the text"]++
		case strings.Contains(r.body, "is not a message from another Coral"):
			counts["reply to a local message"]++
		case code == "too_many_hops":
			counts["reply chain too long"]++
		case code == "not_linked" && noRelay:
			counts["not linked (no relay)"]++
		case code == "rate_limited" && !sentMsg && files == 0:
			counts["rate limited"]++
		case (code == "not_linked" || code == "relay_unsupported" || code == "hub_unreachable") && sentMsg:
			counts["hub unreachable or not linked"]++
		case r.code >= 400 && files > 0 && !sentMsg && noFiles:
			counts["files not relayed (quota refunded)"]++
		case r.code >= 400 && files > 0 && !sentMsg:
			counts["attachment refused by the hub"]++
		case r.code >= 400 && files == 0 && !sentMsg && body["attach"] != nil && strings.Contains(strings.ToLower(r.body), "attach"):
			counts["attachment refused before sending"]++
		case sentMsg && r.code == 502 && hubKind == 5:
			counts["hub refused, odd status (502)"]++
		case sentMsg && (code == "rate_limited:target_busy"):
			counts["hub refused (code kept)"]++
		case sentMsg && strings.HasPrefix(code, "hub_error"):
			counts["hub refused (code replaced)"]++
		case r.code >= 400 && !sentMsg && files == 0:
			counts["sender refused"]++
		case r.code == 200 && resp["mirror_id"] != nil:
			counts["duplicate, first mirror kept"]++
		case r.code == 200 && resp["duplicate"] == true:
			counts["duplicate, mirror stored now"]++
		case r.code == 200 && files > 0 && r.shared != "null" && r.shared != "[]":
			counts["sent with files, shared recorded"]++
		case r.code == 200:
			counts["sent, mirror stored"]++
		}
		if probe && code == "rate_limited" {
			counts["quota probe limited"]++
		}
		if r.code == 200 && hasReply {
			counts["reply to a relayed message (hops)"]++
		}
	}
}

// postRelayMessageBefore4756 is PostRelayMessage as it was before #4756.
func (h *BoardHandler) postRelayMessageBefore4756(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		relayError(w, http.StatusForbidden, "local_only", "messages to another Coral are sent from this computer only", 0)
		return
	}
	project := urlParam(r, "project")
	var body struct {
		SubscriberID string `json:"subscriber_id"`
		SessionName  string `json:"session_name"`
		Content      string `json:"content"`
		ToAddress    string `json:"to_address"`
		ReplyTo      int64  `json:"reply_to"`
		MessageID    string `json:"message_id"`
		// Attach are coral://artifacts links sent as files with the message (cross-team artifacts, C2).
		Attach []string `json:"attach"`
	}
	if err := decodeJSON(r, &body); err != nil {
		relayError(w, http.StatusBadRequest, "bad_request", "invalid JSON", 0)
		return
	}
	attrWho, attrOK := attributedTo(w, r, body.SubscriberID)
	if !attrOK {
		return
	}
	body.SubscriberID = attrWho
	if identityActorEnforced() { // an agent sends from its own session: the token's, not the one it names
		if a := callerActor(r); a.Kind == ActorToken {
			body.SessionName = a.Session
		}
	}
	body.Content = strings.TrimSpace(unescapeLineBreaks(body.Content))
	body.ToAddress = strings.TrimSpace(body.ToAddress)
	switch {
	case body.Content == "" || body.SubscriberID == "":
		relayError(w, http.StatusBadRequest, "bad_request", "subscriber_id and content are required", 0)
		return
	case len(body.Content) > maxRelayedContent:
		relayError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content must be at most %d bytes", maxRelayedContent), 0)
		return
	case strings.Count(body.ToAddress, "/") >= 2:
		relayError(w, http.StatusBadRequest, "bad_address", "an address is <server>/<team>; addressing an agent isn't supported", 0)
		return
	case !relayAddressRe.MatchString(body.ToAddress):
		relayError(w, http.StatusBadRequest, "bad_address", "an address is <server>/<team>, e.g. homelab/qa (see coral-board targets)", 0)
		return
	case body.MessageID != "" && !relayIDRe.MatchString(body.MessageID):
		relayError(w, http.StatusBadRequest, "bad_request", "malformed message_id", 0)
		return
	}
	ctx := r.Context()
	role, ok := h.relaySenderRole(w, r, project, body.SubscriberID, body.SessionName)
	if !ok {
		return
	}
	// The text goes to another computer and Coral Hub does not redact it: the same scan as an attachment's
	// (with the re-encoded views), before anything is sent or counted (#4449). Local posts and messages
	// received from another Coral are not scanned here.
	found, why := scanMessageTextWhy([]byte(body.Content)) // the reference refuses as before; the refusal now says the line and the rule (#5212)
	switch found {
	case scanFound:
		relayError(w, http.StatusUnprocessableEntity, "message_secret", "the message looks like it contains a secret (a token or a key)"+why.where()+", so it was not sent; remove it and send again", 0)
		return
	case scanUnchecked:
		relayError(w, http.StatusUnprocessableEntity, "message_unchecked", "the message could not be checked for secrets in time, so it was not sent; send a shorter message", 0)
		return
	}
	// A reply to a message relayed from another Coral counts one more hop.
	hops := 0
	if body.ReplyTo > 0 {
		o, ok, err := h.bs.RelayedOrigin(ctx, project, body.ReplyTo)
		if err != nil {
			errInternalServer(w, err.Error())
			return
		}
		if !ok {
			relayError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("message %d on %q is not a message from another Coral", body.ReplyTo, project), 0)
			return
		}
		hops = o.Hops + 1
		if hops > maxRelayHops {
			relayError(w, http.StatusBadRequest, "too_many_hops", fmt.Sprintf("a relayed reply chain is limited to %d hops", maxRelayHops), 0)
			return
		}
	}
	relay := h.relaySender()
	if relay == nil {
		relayUnavailable(w, cloud.ErrNotLinked)
		return
	}
	messageID := body.MessageID
	if messageID == "" {
		messageID = uuid.NewString()
	}
	// Files first, checked and held before anything is sent (a refusal here sends nothing), their links
	// appended to the text; the message goes only after every file is with Coral Hub.
	var prepared *attachPrepared
	if len(body.Attach) > 0 {
		var ok bool
		if prepared, ok = h.prepareRelayAttachments(w, r, project, body.SubscriberID, body.SessionName, body.Attach); !ok {
			return
		}
		body.Content += attachmentLines(prepared.atts)
		if len(body.Content) > maxRelayedContent {
			relayError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("content and the attachment links must be at most %d bytes", maxRelayedContent), 0)
			return
		}
	}
	payload, err := json.Marshal(map[string]any{
		"message_id": messageID,
		"hops":       hops,
		"to_address": body.ToAddress,
		"body": map[string]string{
			"from_board": project, "subscriber_id": body.SubscriberID, "session_name": body.SessionName,
			"content": body.Content, "to": "",
		},
	})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// The sender's quota counts only requests that reach Coral Hub: a send that couldn't
	// (not linked, or the relay not offered) gets its token back.
	quotaKey := "relay-out\x00" + project + "\x00" + body.SubscriberID + "\x00" + body.ToAddress
	if secs, limited := h.crossTeamWait(quotaKey); limited {
		relayError(w, http.StatusTooManyRequests, "rate_limited", fmt.Sprintf("retry in %ds", secs), secs)
		return
	}
	if prepared != nil {
		if ok, refund := h.sendRelayAttachments(w, ctx, prepared, messageID, body.ToAddress, project); !ok {
			if refund {
				h.crossTeam.refund(quotaKey)
			}
			return
		}
	}
	resp, err := relay(ctx, tunnel.RelayMessagePath, payload)
	if err != nil {
		if errors.Is(err, cloud.ErrNotLinked) || errors.Is(err, tunnel.ErrRelayUnsupported) {
			h.crossTeam.refund(quotaKey)
		}
		relayUnavailable(w, err)
		return
	}
	var ans hubAnswer
	_ = json.Unmarshal(resp.Body, &ans)
	if resp.Status != http.StatusOK || !ans.OK {
		code := ans.Error
		if !hubErrorCodeRe.MatchString(code) {
			code = "hub_error"
		}
		if ans.Reason != "" && hubErrorCodeRe.MatchString(ans.Reason) {
			code = code + ":" + ans.Reason
		}
		status := resp.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		relayError(w, status, code, "Coral Hub refused the message; retry with the same message_id if it is temporary", resp.RetryAfter)
		return
	}
	var target int64
	if n, err := strconv.ParseInt(strings.Trim(string(ans.TargetMessageID), `"`), 10, 64); err == nil {
		target = n
	}
	// A retry Coral Hub answers "duplicate" was delivered before: keep the copy stored then
	// (one is stored now only if that first attempt never got to store it).
	if ans.Duplicate {
		if id, err := h.bs.RelayMirrorID(ctx, project, messageID); err != nil {
			errInternalServer(w, err.Error())
			return
		} else if id > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true, "message_id": messageID, "target_message_id": target, "mirror_id": id})
			return
		}
	}
	server, team, _ := strings.Cut(body.ToAddress, "/")
	mirror, err := h.bs.PostRelayMirror(ctx, board.MessageOrigin{
		ServerID: store.LocalServerID, Board: project, Sender: body.SubscriberID, SenderRole: role,
		TargetServerID: server, TargetBoard: team, TargetMessageID: target,
		RelayMessageID: messageID, Hops: hops,
	}, body.Content)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if h.notifyFn != nil {
		h.notifyFn(project)
	}
	out := map[string]any{"ok": true, "duplicate": ans.Duplicate, "message_id": messageID, "target_message_id": target, "mirror": mirror}
	if prepared != nil {
		// The shared rows only now: the hub has the files and the message.
		h.recordSharedAttachments(prepared.atts, project, server, team, body.SubscriberID, mirror.ID, time.Now().UTC().Format(time.RFC3339))
		var ids []string
		for _, a := range prepared.atts {
			ids = append(ids, "coral://artifacts/"+a.ID)
		}
		out["attached"] = ids
	}
	writeJSON(w, http.StatusOK, out)
}
