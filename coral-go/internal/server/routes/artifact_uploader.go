package routes

// Who made an artifact, recorded when it is uploaded (#5575, the owner's Artifacts tab in Coral Hub): the upload record of a session names the board the agent was on, the agent's name and its
// role at that moment. Older records have none of these (the list says "unknown"); the name and role can change later, so what was true at upload is kept, not looked up again.

import (
	"context"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/store"
)

// uploaderLookup says which board, name and role a session has; nil (a test, or a server with no store) records none. It is set once at startup (SetUploaderLookup).
var uploaderLookup func(ctx context.Context, sessionID string) (boardID, name, role string)

// SetUploaderLookup gives the upload paths the lookup of a session's board, name and role.
func SetUploaderLookup(fn func(ctx context.Context, sessionID string) (boardID, name, role string)) {
	uploaderLookup = fn
}

// NewUploaderLookup is the server's lookup: the live session's display name (else its agent name) and board, and the role of its subscription on that board.
func NewUploaderLookup(ss *store.SessionStore, bs *board.Store) func(ctx context.Context, sessionID string) (string, string, string) {
	return func(ctx context.Context, sessionID string) (boardID, name, role string) {
		if ss == nil {
			return "", "", ""
		}
		ls, err := ss.GetLiveSession(ctx, sessionID)
		if err != nil || ls == nil {
			return "", "", ""
		}
		name = ls.AgentName
		if ls.DisplayName != nil && *ls.DisplayName != "" {
			name = *ls.DisplayName
		}
		if ls.BoardName != nil {
			boardID = *ls.BoardName
		}
		if bs != nil {
			if sub, err := bs.GetSubscriptionBySessionName(ctx, naming.SessionName(ls.AgentType, sessionID)); err == nil && sub != nil {
				role = sub.JobTitle
				if boardID == "" {
					boardID = sub.Project
				}
			}
		}
		return boardID, name, role
	}
}

// uploaderLabelMax bounds each recorded label; one that is longer or holds a control or formatting character is dropped, never stored.
const uploaderLabelMax = 160

// uploaderOf is what the record of an upload by sessionID says about who made it. It never waits long and never fails the upload: no answer is no fields.
func uploaderOf(sessionID string) (boardID, name, role string) {
	if uploaderLookup == nil || sessionID == "" {
		return "", "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b, n, r := uploaderLookup(ctx, sessionID)
	clean := func(s string) string {
		if !board.ValidLabel(s, uploaderLabelMax) {
			return ""
		}
		return s
	}
	return clean(b), clean(n), clean(r)
}

// newUploadRecord is the record of an upload by sessionID of the object described by meta, made now.
func newUploadRecord(meta agentArtifactMeta, sessionID, uploadedBy, source string) agentUploadRecord {
	b, n, r := uploaderOf(sessionID)
	return agentUploadRecord{agentArtifactMeta: meta, CreatedAt: time.Now().UTC().Format(time.RFC3339), UploadedBy: uploadedBy, Source: source, Board: b, Uploader: n, Role: r}
}
