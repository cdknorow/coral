package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// The leak sweep (QA's #5041 review method, #5052): an agent's token is for its own board. Seed ANOTHER board's session and board with
// secrets (prompt, working directory, branch, goal, an event, a usage row, a board message, a task), mint a token for an agent of board A,
// call every GET of the agent-route census with it, and require none of the secrets in any answer. Roster fields (name, type, state,
// board, model) are allowed: the secrets are texts only the other board's work holds. The routes come from the census, so a route added
// later is swept without anyone remembering it. Found by hand in the v1.4.105 release QA: /api/goals/metrics (goal text) and
// /api/token-usage/summary's by_agent rows (session id, folder, working directory) answered another board's work to an agent.
//
// Mutation: give an agent token the Operator's answer on a route (drop a scope), and the route is named by the failure.
func TestAgentTokenReadsNoOtherBoardsSecrets(t *testing.T) {
	srv, ts := hubServer(t)
	ctx := context.Background()
	ss := store.NewSessionStore(srv.db)

	secrets := map[string]string{
		"prompt":      "LEAKSWEEP-PROMPT-7f3a91",
		"working dir": "/leaksweep-workdir-7f3a91/secret-repo",
		"branch":      "leaksweep-branch-7f3a91",
		"goal":        "LEAKSWEEP-GOAL-7f3a91",
		"event":       "LEAKSWEEP-EVENT-7f3a91",
		"commit":      "LEAKSWEEP-COMMIT-7f3a91",
		"message":     "LEAKSWEEP-MESSAGE-7f3a91",
		"task":        "LEAKSWEEP-TASK-7f3a91",
	}
	const sidA, sidMate, sidB = "aaaaaaaa-5052-4000-8000-0000000000a1", "aaaaaaaa-5052-4000-8000-0000000000a2", "bbbbbbbb-5052-4000-8000-0000000000b1"
	const boardA, boardB = "b-sweepa00001", "b-sweepb00001"
	str := func(s string) *string { return &s }

	for _, c := range []struct {
		sid, name, board, dir, prompt string
	}{
		{sidA, "agent-a", boardA, t.TempDir(), "A's own prompt"},
		{sidMate, "mate-a", boardA, t.TempDir(), "A's mate prompt"},
		{sidB, "agent-b", boardB, secrets["working dir"], secrets["prompt"]},
	} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: c.sid, AgentType: "claude", AgentName: c.name, WorkingDir: c.dir,
			Prompt: str(c.prompt), BoardName: str(c.board), DisplayName: str(c.name)}))
		_, err := srv.boardStore.Subscribe(ctx, c.board, c.name, "dev", "claude-"+c.sid, nil, "all")
		require.NoError(t, err, c.name)
	}
	_, err := srv.boardStore.PostMessage(ctx, boardB, "agent-b", secrets["message"], nil)
	require.NoError(t, err)
	_, err = srv.boardStore.CreateTask(ctx, boardB, secrets["task"], "details", "medium", "agent-b")
	require.NoError(t, err)
	sid := sidB
	_, err = store.NewTaskStore(srv.db).InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "agent-b", SessionID: &sid, EventType: "goal", Summary: secrets["goal"]})
	require.NoError(t, err)
	_, err = store.NewTaskStore(srv.db).InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "agent-b", SessionID: &sid, EventType: "tool_use", ToolName: str("Bash"), Summary: "Ran: " + secrets["event"]})
	require.NoError(t, err)
	require.NoError(t, store.NewGitStore(srv.db).UpsertGitSnapshot(ctx, &store.GitSnapshot{AgentName: "agent-b", AgentType: "claude", WorkingDirectory: secrets["working dir"],
		Branch: secrets["branch"], CommitHash: "0123456789abcdef0123456789abcdef01234567", CommitSubject: secrets["commit"], SessionID: str(sidB)}))
	require.NoError(t, store.NewGoalMetricsStore(srv.db).Record(ctx, &store.GoalGeneration{SessionID: sidB, AgentName: "agent-b", AgentType: "claude", Trigger: store.GoalTriggerFirst,
		Outcome: store.GoalOutcomeStored, Goal: secrets["goal"], DurationMs: 900}))
	require.NoError(t, store.NewTokenUsageStore(srv.db).RecordUsage(ctx, &store.TokenUsage{SessionID: sidB, AgentName: "agent-b", AgentType: "claude", BoardName: str(boardB),
		InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100, CostUSD: 0.5}))

	tok, err := srv.db.MintSessionToken(ctx, sidA, "claude-"+sidA)
	require.NoError(t, err)

	subs := map[string]string{"name": "agent-b", "sessionID": sidB, "id": sidB, "project": boardB, "boardName": boardB, "board": boardB, "taskID": "1", "messageID": "1"}
	param := regexp.MustCompile(`\{([^}]*)\}|\*`)
	get := func(path, token string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		require.NoError(t, err)
		if token != "" {
			req.Header.Set("X-Coral-Session-Token", token)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	var called int
	leaks := map[string][]string{}
	operatorSaw := map[string]bool{} // the same calls without a token: the seeds do reach routes, so an agent's clean answers mean something
	for route, group := range readAgentRoutes(t) {
		method, pattern, _ := strings.Cut(route, " ")
		if method != http.MethodGet || group == "b" || strings.HasPrefix(pattern, "/ws/") || !strings.HasPrefix(pattern, "/api/") {
			continue
		}
		path := param.ReplaceAllStringFunc(pattern, func(m string) string {
			if v, ok := subs[strings.Trim(m, "{}")]; ok {
				return v
			}
			return "x1"
		})
		if strings.Contains(path, "/sessions/live/") || strings.Contains(path, "/sessions/history/") {
			path += "?session_id=" + sidB // a name shared by two sessions needs its id
		}
		_, body := get(path, tok)
		called++
		if code, open := get(path, ""); code == http.StatusOK {
			for what, secret := range secrets {
				if strings.Contains(open, secret) {
					operatorSaw[what] = true
				}
			}
		}
		for what, secret := range secrets {
			if strings.Contains(body, secret) {
				leaks[route] = append(leaks[route], what)
			}
		}
	}
	require.Greater(t, called, 100, "the census lists the agent-readable routes")
	var lines []string
	for route, what := range leaks {
		sort.Strings(what)
		lines = append(lines, fmt.Sprintf("%s: %s", route, strings.Join(what, ", ")))
	}
	sort.Strings(lines)
	for what := range secrets {
		assert.True(t, operatorSaw[what], "no route answered the %s without a token: the seed does not reach any route, so the sweep proves nothing for it", what)
	}
	assert.Empty(t, lines, "routes that answered another board's secrets to an agent's token")
}
