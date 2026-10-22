package startup

import (
	"context"
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/gitutil"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/store"
)

// reconcileOrphanedSessions checks all non-sleeping live sessions against
// the runtime to see if their processes are actually running. Any session
// that exists in the DB but has no running process is marked as sleeping
// so the user can wake it from the UI.
// ptyRun: on pty, an awake row with no running agent was running when Coral last stopped
// without its shutdown (a crash), so it is marked coral_stopped for the auto-wake (#3636).
func reconcileOrphanedSessions(ctx context.Context, db *store.DB, agentRT background.AgentRuntime, ptyRun bool) {
	ss := store.NewSessionStore(db)

	liveSessions, err := ss.GetAllLiveSessions(ctx)
	if err != nil {
		log.Printf("[startup] failed to read live sessions for reconciliation: %v", err)
		return
	}

	// Ask the runtime which sessions are actually running
	agents, err := agentRT.ListAgents(ctx)
	if err != nil {
		log.Printf("[startup] failed to list running agents for reconciliation: %v", err)
		return
	}
	alive := make(map[string]bool, len(agents))
	for _, a := range agents {
		alive[a.SessionID] = true
	}

	orphaned := 0
	recovered := 0
	for _, ls := range liveSessions {
		if ls.IsSleeping == 1 {
			// Check if a sleeping session is actually alive (e.g. tmux still running
			// after a server restart). If so, wake it.
			if alive[ls.SessionID] {
				if err := ss.SetSessionSleeping(ctx, ls.SessionID, false); err != nil {
					log.Printf("[startup] failed to wake recovered session %s: %v", ls.SessionID[:8], err)
					continue
				}
				recovered++
			}
			continue
		}
		if alive[ls.SessionID] {
			continue
		}
		slog.Warn("detected crashed agent, marking as sleeping", "session_id", ls.SessionID, "agent_name", ls.AgentName)
		reason := ""
		if ptyRun {
			reason = store.SleepReasonCoralStopped
		}
		if err := ss.SetSessionSleepingFor(ctx, ls.SessionID, reason); err != nil {
			log.Printf("[startup] failed to mark orphaned session %s as sleeping: %v", ls.SessionID[:8], err)
			continue
		}
		orphaned++
	}
	if orphaned > 0 {
		log.Printf("[startup] Marked %d orphaned session(s) as sleeping", orphaned)
	}
	if recovered > 0 {
		log.Printf("[startup] Recovered %d sleeping session(s) that are still alive", recovered)
	}
}

// reconcileOrphanedTeams checks for teams in 'running' status whose
// member sessions no longer exist. These are teams orphaned by a server
// crash. They are marked as stopped and any worktree is cleaned up.
func reconcileOrphanedTeams(ctx context.Context, db *store.DB) {
	ts := store.NewTeamStore(db)
	ss := store.NewSessionStore(db)

	teams, err := ts.ListTeams(ctx, "running")
	if err != nil {
		log.Printf("[startup] failed to list running teams for reconciliation: %v", err)
		return
	}

	liveSessions, err := ss.GetAllLiveSessions(ctx)
	if err != nil {
		log.Printf("[startup] failed to read live sessions for team reconciliation: %v", err)
		return
	}
	liveSet := make(map[string]bool, len(liveSessions))
	for _, ls := range liveSessions {
		liveSet[ls.SessionID] = true
	}

	orphaned := 0
	for _, team := range teams {
		members, err := ts.GetActiveMembers(ctx, team.ID)
		if err != nil {
			log.Printf("[startup] failed to get members for team %s: %v", team.Name, err)
			continue
		}

		// Check if any member still has a live session
		hasLive := false
		for _, m := range members {
			if m.SessionID != nil && liveSet[*m.SessionID] {
				hasLive = true
				break
			}
		}
		if hasLive {
			continue
		}

		slog.Warn("detected orphaned team, marking as stopped", "team", team.Name, "id", team.ID)

		// Mark members and team as stopped
		if err := ts.SetAllMembersStatus(ctx, team.ID, "active", "stopped"); err != nil {
			log.Printf("[startup] failed to stop members of team %s: %v", team.Name, err)
		}
		if err := ts.UpdateTeamStatus(ctx, team.ID, "stopped"); err != nil {
			log.Printf("[startup] failed to stop team %s: %v", team.Name, err)
		}

		// Clean up the team's worktree, but never its work (#3595): an orphaned team is usually
		// one whose server stopped mid-work, so its worktree may hold uncommitted changes. Git
		// removes it only when it's clean (no --force), and when git can't say (a timeout, git
		// missing, a broken .git file), the folder is left where it is and logged.
		if team.IsWorktree == 1 && team.WorkingDir != "" {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			repoOut, repoErr := gitutil.Command(cleanCtx, team.WorkingDir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
			if repoErr == nil {
				repoPath := filepath.Dir(strings.TrimSpace(string(repoOut)))
				cmd := gitutil.Command(cleanCtx, repoPath, "worktree", "remove", team.WorkingDir)
				if out, err := cmd.CombinedOutput(); err != nil {
					log.Printf("[startup] left the orphaned worktree %s in place (git won't remove it, e.g. it has uncommitted work): %s", logsafe.Dir(team.WorkingDir), strings.TrimSpace(string(out)))
				} else {
					log.Printf("[startup] cleaned up orphaned worktree: %s", logsafe.Dir(team.WorkingDir))
				}
			} else {
				log.Printf("[startup] left the orphaned worktree %s in place: git can't find its repository (%v)", logsafe.Dir(team.WorkingDir), repoErr)
			}
			cancel()
		}

		orphaned++
	}
	if orphaned > 0 {
		log.Printf("[startup] Marked %d orphaned team(s) as stopped", orphaned)
	}
}
