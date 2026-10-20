package routes

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"
)

// The checks a local cross-team message (PostExternalMessage) and a message to another Coral
// (PostRelayMessage, relaySenderRole, prepareRelayAttachments) both make, in one place: who may send
// from a board, the sender's rate limit, and who an attachment is sent as. Each caller still words its
// own answers (the local route answers with plain errors, the relay with the fixed codes Coral Hub maps).

// senderRefusal says which rule refused a sender.
type senderRefusal int

const (
	senderAllowed         senderRefusal = iota
	senderNoBoard                       // the Operator named a board that does not exist
	senderNotOrchestrator               // not an active Orchestrator of the board
	senderSessionMismatch               // session_name is not the Orchestrator's own session
)

// checkCrossTeamSender decides whether subscriberID may send from board: the Operator (the board
// must exist), or an active Orchestrator of it calling from its own session (session_name must be
// the one its subscription records: a speed bump against sending as another agent, not
// authentication). role is who is sending and session the Orchestrator's session name ("" for the
// Operator). err is a failure to read the store; refusal says which rule refused.
func (h *BoardHandler) checkCrossTeamSender(ctx context.Context, board, subscriberID, sessionName string) (role, session string, refusal senderRefusal, err error) {
	if subscriberID == crossTeamOperatorID {
		ok, err := h.bs.BoardExists(ctx, board)
		if err != nil {
			return "", "", senderAllowed, err
		}
		if !ok {
			return "", "", senderNoBoard, nil
		}
		return crossTeamOperatorID, "", senderAllowed, nil
	}
	sub, err := h.bs.GetProjectSubscription(ctx, board, subscriberID)
	if err != nil {
		return "", "", senderAllowed, err
	}
	// GetProjectSubscription selects only is_active = 1 rows, so IsActive == 0 cannot be true here today: the check mirrors that SQL filter
	// and keeps the rule if the lookup ever returns inactive rows.
	if sub == nil || sub.IsActive == 0 || !strings.EqualFold(strings.TrimSpace(sub.JobTitle), crossTeamSenderRole) {
		return "", "", senderNotOrchestrator, nil
	}
	if sn := strings.TrimSpace(sessionName); sn == "" || sub.SessionName == "" || sn != sub.SessionName {
		return "", "", senderSessionMismatch, nil
	}
	return crossTeamSenderRole, sub.SessionName, senderAllowed, nil
}

// crossTeamWait spends one of the sender's tokens for key. limited is whether there was none, and
// secs the whole seconds to wait for one.
func (h *BoardHandler) crossTeamWait(key string) (secs int, limited bool) {
	ok, wait := h.crossTeam.allow(key)
	if ok {
		return 0, false
	}
	return int(math.Ceil(wait.Seconds())), true
}

// attachSenderFor is who an attachment is sent as: the Operator when a person is acting as the
// Operator, else the sender's own session (its team's artifacts are then in reach) when the verified
// session is the one named, else the shared upload bucket. sessionName is the sending
// Orchestrator's session ("" for the Operator).
func (h *BoardHandler) attachSenderFor(ctx context.Context, r *http.Request, board, subscriberID, sessionName string) attachSender {
	sender := attachSender{person: subscriberID == crossTeamOperatorID && callerActor(r).IsOperator(), windowKey: "operator"}
	if !sender.person {
		sender.windowKey = sharedUploadBucket
		if owner, ok := VerifiedSession(ctx); ok && owner != nil && owner.SessionID != "" && sessionName != "" && owner.SessionName == sessionName {
			sender.sessionID, sender.windowKey = owner.SessionID, "session:"+owner.SessionID
			sender.team = h.teamSessions(ctx, board) // the sender is a subscriber: its own session is in it
		}
	}
	return sender
}

// The coarse relay limits (#5514). A relayed sender names its own board and subscriber strings, so the per-(source, sender board, sender, target) limit alone is dodged by varying them: a limit
// per source Coral and target board, and one per source Coral overall, which no string the sender chooses changes, bound what one Coral can push whatever it calls its senders. They are far
// above what a team sends (30 messages at once and one every 2 seconds to one board; 120 at once and one a second in all), so they only ever stop a sender that varies its names.
const (
	relayTargetBurst  = 30
	relayTargetRefill = 2 * time.Second
	relaySourceBurst  = 120
	relaySourceRefill = time.Second
)

// relayRateAllow takes one message from the relayed sender's limits: the coarse ones first (per source Coral and target board, per source Coral), then the fine one per (source, sender board,
// sender, target). It reports the longest wait when one refuses; a refusal at an earlier limit gives back nothing it took (each limit only counts what it admitted).
func (h *BoardHandler) relayRateAllow(src, fromBoard, subscriber, target string) (bool, time.Duration) {
	h.relayCoarse.once.Do(func() {
		clock := func() time.Time { // the handler's own clock, read each time (tests move it)
			if h.crossTeam.now != nil {
				return h.crossTeam.now()
			}
			return time.Now()
		}
		h.relayCoarse.perTarget = crossTeamLimiter{burst: relayTargetBurst, refill: relayTargetRefill, now: clock}
		h.relayCoarse.perSource = crossTeamLimiter{burst: relaySourceBurst, refill: relaySourceRefill, now: clock}
	})
	if ok, wait := h.relayCoarse.perSource.allow("src\x00" + src); !ok {
		return false, wait
	}
	if ok, wait := h.relayCoarse.perTarget.allow("src-target\x00" + src + "\x00" + target); !ok {
		return false, wait
	}
	return h.crossTeam.allow("relay\x00" + src + "\x00" + fromBoard + "\x00" + subscriber + "\x00" + target)
}
