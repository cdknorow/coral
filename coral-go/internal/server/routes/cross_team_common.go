package routes

import (
	"context"
	"math"
	"net/http"
	"strings"
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
