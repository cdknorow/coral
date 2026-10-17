package routes

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// Review auto-delegation (#4175 step 1): when a task enters review for the first time, Coral
// gives it to the reviewers it names (or the team's default reviewers) itself, instead of
// waiting for the orchestrator. Security's #4176: every line is a fixed template filled with
// board members' names and task numbers only, never a title or body (rule 1); the action is
// limited in the store (rule 5); the reviewers are chosen by board.AutoReviewers (rules 12
// and 13); the review round it opens needs all of them, as a delegation by the orchestrator
// does (rule 15).

// reviewDelegationLimits bound auto-delegation on one board.
var reviewDelegationLimits = board.HealthLimits{PerTargetPerHour: 2, TeamPerHour: healthTeamBudget}

// healthTeamBudget is how many automatic health actions a board allows per hour (#4176 rule 5).
const healthTeamBudget = 20

// healthNow is the clock of the health rules (a test seam).
var healthNow = time.Now

// autoDelegateReview delegates task id, just handed to orch for review, and reports whether it
// did. When it didn't, the orchestrator is told as before.
func (h *BoardHandler) autoDelegateReview(ctx context.Context, project string, id int64, orch string) bool {
	if hs := h.bs.GetHealthSettings(ctx, project); !hs.On || !hs.AutoDelegate.On {
		return false
	}
	task, err := h.bs.GetTask(ctx, project, id)
	if err != nil || task == nil {
		return false
	}
	names, source, err := h.bs.AutoReviewers(ctx, project, task, orch)
	if err != nil || len(names) == 0 {
		return false
	}
	why, err := h.bs.TakeHealthAction(ctx, project, "review-delegation", fmt.Sprintf("#%d", id), healthNow(), reviewDelegationLimits)
	if err != nil || why != "" {
		if why == board.HealthRefusedBudget {
			h.healthBudgetReached(ctx, project)
		}
		return false
	}
	_, reviews, err := h.bs.DelegateReview(ctx, project, id, orch, names)
	if err != nil {
		slog.Warn("review auto-delegation failed", "project", project, "task", id, "error", err)
		return false
	}
	list := make([]string, 0, len(reviews))
	for _, rt := range reviews {
		list = append(list, fmt.Sprintf("%s (#%d)", teamhealth.Name(*rt.AssignedTo), rt.ID))
	}
	h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(
		"[Task #%d delegated for review by Coral] to %s, %s. @%s change them with coral-board task review %d --to \"...\"",
		id, strings.Join(list, ", "), source, teamhealth.Name(orch), id), nil)
	for _, rt := range reviews {
		h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(
			"@%s [Task #%d assigned to you: the review of #%d] run 'coral-board task claim %d', then 'coral-board task detail %d'",
			teamhealth.Name(*rt.AssignedTo), rt.ID, id, rt.ID, id), nil)
		h.sendTaskNudge(ctx, project, *rt.AssignedTo, taskClaimNudge(rt.ID))
	}
	return true
}

// healthBudgetReached tells the orchestrator, once an hour, that Coral stopped acting on its own
// because the board's hourly budget is used up (#4176 rule 5).
func (h *BoardHandler) healthBudgetReached(ctx context.Context, project string) {
	why, err := h.bs.TakeHealthAction(ctx, project, "budget-reached", "board", healthNow(), board.HealthLimits{Cooldown: time.Hour})
	if err != nil || why != "" {
		return
	}
	orch, err := h.bs.TeamOrchestrator(ctx, project)
	if err != nil {
		return
	}
	h.bs.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(
		"@%s [Coral health actions paused] this board's budget of %d automatic actions an hour is used up; Coral acts again next hour", teamhealth.Name(orch), healthTeamBudget), nil)
}
