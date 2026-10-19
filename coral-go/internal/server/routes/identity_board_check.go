package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
)

// checkBoard compares a board request's claimed identity with the token's session, and
// returns the first mismatch (nil when there is none). Every mismatch is recorded. An
// error means a claimed name couldn't be checked (the board's subscribers couldn't be
// read), and the caller refuses the request.
func (o *IdentityObserver) checkBoard(r *http.Request, owner *store.SessionTokenOwner) (first *IdentityMismatch, actorName string, err error) {
	project, tail, ok := boardRoute(r)
	if !ok || o.bs == nil {
		return nil, "", nil
	}
	switch project {
	case "", "projects", "tasks", "remotes":
		return nil, "", nil
	}
	// The board the request acts on, as the router will resolve it (#3536, Security's
	// review): a label naming one board is that board's id, so an agent may use its own
	// board's label, and the comparison below is on ids, exactly (a case variant of an id is
	// another key, not this board).
	project = resolvedBoardKey(r, o.bs, project)
	sub, lookupErr := o.bs.GetSubscriptionBySessionName(r.Context(), owner.SessionName)
	if lookupErr != nil {
		// Never a bypass: checked as an agent with no subscription (the Operator and
		// names other sessions hold are still refused; Security's #3512 review).
		o.logOnce("board-lookup:"+owner.SessionName, "[identity] subscription lookup failed; checked as unsubscribed",
			"session", owner.SessionName, "error", lookupErr)
		sub = nil
	}
	// The mismatch returned is the one that decides: an impersonation or Operator claim
	// before a cross-board one (all are refused; the first is the one reported).
	note := func(m IdentityMismatch) {
		if first == nil || (first.Kind == mismatchCrossBoard && m.Kind != mismatchCrossBoard) {
			first = &m
		}
	}
	claims, err := boardClaims(r)
	if err != nil {
		return nil, "", err
	}
	// A subscription is bound to the session that holds it: an agent can't subscribe (or
	// act) under another session's name (#3512).
	for _, v := range claims["session_name"] {
		if v = strings.TrimSpace(v); v != "" && v != owner.SessionName {
			note(o.mismatch(r, owner, mismatchImpersonate, owner.SessionName, v, "session_name"))
		}
	}
	delete(claims, "session_name")
	// An agent on more than one board acts on each as its subscription there.
	if sub != nil && project != sub.Project {
		there, err := o.bs.SessionSubscription(r.Context(), project, owner.SessionName)
		if err != nil {
			return nil, "", err
		}
		if there != nil {
			sub = there
		}
	}
	if sub == nil {
		if !acrossBoards(r.Method, tail) {
			note(o.mismatch(r, owner, mismatchCrossBoard, notSubscribed, project, "project"))
		}
		// Not on a board yet (it may be subscribing now): it can name itself anything new,
		// but never the Operator or a subscriber another session holds on this board
		// (#3512; before, such an agent skipped every check here and could act as the
		// Operator: post as it, change board settings).
		var held []board.Subscriber
		listed := false
		for f, vs := range claims {
			for _, v := range vs {
				v = strings.TrimSpace(v)
				switch {
				case v == "":
				case strings.EqualFold(v, "Operator"):
					note(o.mismatch(r, owner, mismatchOperator, notSubscribed, v, f))
				default:
					if !listed {
						if held, err = o.bs.ListSubscribers(r.Context(), project); err != nil {
							return nil, "", err
						}
						listed = true
					}
					for _, s := range held {
						if strings.EqualFold(s.SubscriberID, v) && s.SessionName != owner.SessionName {
							note(o.mismatch(r, owner, mismatchImpersonate, notSubscribed, v, f))
							break
						}
					}
				}
			}
		}
		return first, "", nil
	}
	if project != sub.Project && !acrossBoards(r.Method, tail) {
		note(o.mismatch(r, owner, mismatchCrossBoard, sub.Project, project, "project"))
	}
	for f, vs := range claims {
		for _, v := range vs {
			v = strings.TrimSpace(v)
			switch {
			case v == "" || v == sub.SubscriberID:
			case strings.EqualFold(v, "Operator"):
				note(o.mismatch(r, owner, mismatchOperator, sub.SubscriberID, v, f))
			case f == "as" && strings.EqualFold(v, sub.JobTitle):
				// 'as' is a display name for auto-subscribe: the session's own role is fine
			default:
				note(o.mismatch(r, owner, mismatchImpersonate, sub.SubscriberID, v, f))
			}
		}
	}
	return first, sub.SubscriberID, nil
}

// notSubscribed is the expected identity logged for an agent that holds a token but
// no board subscription yet.
const notSubscribed = "(not subscribed)"

// boardClaims collects the identities a board request names: subscriber_id, as,
// created_by and session_name, from the query and a JSON body (the body is put back for
// the handler).
func boardClaims(r *http.Request) (map[string][]string, error) {
	claims := map[string][]string{}
	for _, f := range []string{"subscriber_id", "as", "created_by", "session_name"} {
		claims[f] = append(claims[f], r.URL.Query()[f]...)
	}
	// A relayed file's body is a binary frame, not a request about a board: it names no identity and is never
	// read here (#4453; CanonicalIdentityKeys skips it too). The query's claims above are still collected.
	if r.Body != nil && r.Body != http.NoBody && !isRelayedFileRoute(r) {
		raw, err := io.ReadAll(r.Body)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errBoardBodyTooLarge
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body map[string]json.RawMessage
		if err == nil && json.Unmarshal(raw, &body) == nil {
			// Every spelling of a claim field counts, as encoding/json would match it (#4233): the
			// canonicalising middleware has already put them in one spelling, and this holds
			// when a request reaches here by another path.
			for key, v := range body {
				for f := range claims {
					var s string
					if strings.EqualFold(key, f) && json.Unmarshal(v, &s) == nil {
						claims[f] = append(claims[f], s)
					}
				}
			}
		}
	}
	return claims, nil
}

// isRelayedFileRoute: POST /api/board/{project}/external-artifacts, the file another Coral sends through
// Coral Hub. Its body is a binary frame of up to 8 MiB, not a JSON request about a board.
func isRelayedFileRoute(r *http.Request) bool {
	if r.Method != http.MethodPost || !strings.HasPrefix(routedPath(r), "/api/board/") || !strings.HasSuffix(routedPath(r), "/external-artifacts") {
		return false
	}
	project := strings.TrimSuffix(strings.TrimPrefix(routedPath(r), "/api/board/"), "/external-artifacts")
	return project != "" && !strings.Contains(project, "/")
}

// maxBoardBody caps a board request's body before the identity check reads it (a board post or
// a task with its body is far smaller).
const maxBoardBody = 4 << 20

// errBoardBodyTooLarge is a board request whose body is over maxBoardBody: 413.
var errBoardBodyTooLarge = errors.New("board request body too large")
