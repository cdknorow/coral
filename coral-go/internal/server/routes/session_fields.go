package routes

import (
	"encoding/json"
	"github.com/subgentic/coral/internal/clitext"
	"slices"

	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

// Session-list fields that /api/sessions/live and /ws/coral both send. Both
// builders call these, so a client (dashboard or Cloud Hub) gets identical
// values from either source.

// lookupGitState finds an agent's latest git snapshot: by session first, then
// by agent name (agents launched before snapshots carried a session id).
func lookupGitState(states map[string]*store.GitSnapshot, sessionID, agentName string) *store.GitSnapshot {
	var git *store.GitSnapshot
	if sessionID != "" {
		git = states[sessionID]
	}
	if git == nil {
		git = states[agentName]
	}
	return git
}

// gitListFields returns the branch, repo_name and git_state entry values.
// git_state is set only for worktrees or branches with a known base.
func gitListFields(git *store.GitSnapshot) (branch, repoName, gitState any) {
	if git == nil {
		return nil, nil, nil
	}
	branch = clitext.CleanName(git.Branch) // rows stored before the store cleaned them are cleaned as they are shown (#6832)
	if git.Worktree || git.BaseBranch != "" {
		gitState = map[string]any{
			"is_worktree": git.Worktree,
			"base_branch": git.BaseBranch,
			"ahead":       git.Ahead,
			"behind":      git.Behind,
			"dirty":       git.DirtyCount,
		}
	}
	if git.RemoteURL != nil {
		if rn := gitutil.ParseRepoName(*git.RemoteURL); rn != "" {
			repoName = rn
		}
	}
	return branch, repoName, gitState
}

// addCapabilities sets entry["capabilities"] to the stored JSON when present
// and valid; otherwise the key is left out.
func addCapabilities(entry map[string]any, raw *string) {
	if raw != nil && *raw != "" && json.Valid([]byte(*raw)) {
		entry["capabilities"] = json.RawMessage(*raw)
	}
}

// listTextFields returns status, summary, display_name and icon as both
// session lists send them: status and summary are null when empty,
// display_name is the stored name ("" when unset), and icon is null unless
// one is stored for the session.
func listTextFields(logInfo map[string]any, summary string, displayNames, icons map[string]string, sessionID string) (status, summ, displayName, icon any) {
	logStatus, _ := logInfo["status"].(string)
	status, summ, displayName = ptr.NullIfEmpty(logStatus), ptr.NullIfEmpty(summary), displayNames[sessionID]
	if ic, ok := icons[sessionID]; ok && sessionID != "" {
		icon = ic
	}
	return status, summ, displayName, icon
}

// archivedLast moves archived agents' rows to the end and keeps the order
// otherwise, so a reader that caps the list keeps live rows first (#3030).
func archivedLast(rows []map[string]any) []map[string]any {
	slices.SortStableFunc(rows, func(a, b map[string]any) int {
		aa, ba := a["archived"] == true, b["archived"] == true
		switch {
		case aa == ba:
			return 0
		case aa:
			return 1
		default:
			return -1
		}
	})
	return rows
}
