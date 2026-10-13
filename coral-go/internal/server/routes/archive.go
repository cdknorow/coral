package routes

import (
	"context"

	"github.com/cdknorow/coral/internal/logparse"
	"github.com/cdknorow/coral/internal/store"
)

// ArchivedSessionLookup returns the lookup BoardHandler.SetArchivedSessionLookup
// takes: whether a board session name (agentType-sessionID) belongs to an
// archived agent. A name that is not a session, or a lookup error, is "not
// archived": the board then behaves as before.
func ArchivedSessionLookup(ss *store.SessionStore) func(ctx context.Context, sessionName string) bool {
	return func(ctx context.Context, sessionName string) bool {
		_, sid := logparse.ParseSessionName(sessionName)
		if sid == "" {
			return false
		}
		archived, err := ss.IsSessionArchived(ctx, sid)
		return err == nil && archived
	}
}
