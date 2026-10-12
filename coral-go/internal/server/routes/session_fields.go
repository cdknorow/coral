package routes

import (
	"encoding/json"

	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/store"
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
	branch = git.Branch
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
