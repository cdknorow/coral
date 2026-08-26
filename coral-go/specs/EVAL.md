# Running agent evaluations through the Coral API

This guide shows how to run an agent or agent team against a repository at an exact commit, require it to open a pull request, and retrieve token, cost, and turn metrics afterward.

The examples assume Coral is running on `localhost:8420`, `jq` is installed, the agent CLI is authenticated, and `gh auth status` succeeds. Localhost requests need no authentication. For a remote Coral server, add `-H "Authorization: Bearer $CORAL_API_KEY"` to every request.

```bash
export CORAL_URL=http://localhost:8420
export REPO=/absolute/path/to/repository
export BASE_COMMIT=0123456789abcdef0123456789abcdef01234567
export EVAL_NAME=eval-login-fix-001
```

Use a unique `EVAL_NAME` for every evaluation. Prefer a full commit SHA, and ensure the commit exists locally before launch (for example, run `git -C "$REPO" fetch --all`).

## Single-agent evaluation

The one-shot Tasks API is the simplest choice for one agent. Coral creates an isolated worktree from `base_branch`; it may be an exact commit SHA. Leave cleanup disabled so the checkout remains available for inspection.

```bash
RUN=$(curl -fsS -X POST "$CORAL_URL/api/tasks/run" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg repo "$REPO" \
    --arg commit "$BASE_COMMIT" \
    --arg name "$EVAL_NAME" \
    --arg prompt 'Implement the requested change and run the relevant tests. Create a new branch from the current checkout, commit all intended changes, push the branch, and open a GitHub pull request with gh pr create. Put the PR URL in your final response. Do not finish without either creating the PR or clearly reporting why it could not be created.' \
    '{repo_path: $repo, base_branch: $commit, agent_type: "codex",
      display_name: $name, prompt: $prompt, create_worktree: true,
      cleanup_worktree: false, max_duration_s: 3600, auto_accept: true,
      max_auto_accepts: 20, flags: "--model gpt-5.6-codex"}')")

RUN_ID=$(jq -r '.run_id' <<<"$RUN")
echo "run_id=$RUN_ID"
```

`agent_type` can be `claude`, `codex`, or `gemini`. `flags` is a string passed to that CLI and can carry model/vendor-specific configuration. `auto_accept` enables the agent-specific unattended mode; use it only in an isolated evaluation repository.

Poll until the run reaches a terminal state:

```bash
while true; do
  STATUS=$(curl -fsS "$CORAL_URL/api/tasks/runs/$RUN_ID")
  echo "$STATUS" | jq '{status, exit_reason, session_id, worktree_path, error_msg}'
  STATE=$(jq -r '.status' <<<"$STATUS")
  case "$STATE" in completed|failed|killed) break ;; esac
  sleep 10
done

SESSION_ID=$(jq -r '.session_id' <<<"$STATUS")
```

The run response also contains proxy fields when proxy accounting is available: `proxy_cost_usd`, `proxy_request_count`, `proxy_input_tokens`, and `proxy_output_tokens`.

## Team evaluation

For a team, use `launch-team`. Coral creates one shared worktree and a branch named `coral-team/<board_name>` from the exact `base_branch` commit. All members share the checkout and Coral message board. The launch prompts below contain only durable role instructions; the evaluation objective is submitted separately as a tracked board task.

```bash
TEAM=$(curl -fsS -X POST "$CORAL_URL/api/sessions/launch-team" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg repo "$REPO" --arg commit "$BASE_COMMIT" --arg board "$EVAL_NAME" \
    '{board_name: $board, working_dir: $repo, worktree: true,
      base_branch: $commit, agent_type: "codex", flags: ["--full-auto"],
      agents: [
        {name: "Lead", agent_type: "codex", model: "gpt-5.6-codex",
         capabilities: {allow: ["file_read", "file_write", "shell", "git_write", "agent_spawn"]},
         prompt: "You are the team orchestrator. Claim tasks assigned to Lead from the Coral board, coordinate other members through the board, and ensure every claimed task is completed or cancelled in Coral."},
        {name: "Reviewer", agent_type: "claude", model: "sonnet",
         capabilities: {allow: ["file_read", "file_write", "shell"]},
         prompt: "You are the team reviewer. Assist Lead through the Coral board, review changes, run relevant tests, fix problems you find, and report results to Lead."}
      ]}')")

echo "$TEAM" | jq
TEAM_ID=$(jq -r '.team_id' <<<"$TEAM")
```

Per-agent `agent_type`, `model`, `capabilities`, `tools`, `mcpServers`, and `hooks` may be supplied. Team-wide `flags` apply to every member, so CLI-specific flags generally belong in a homogeneous team. The launch response contains each member's `session_id`.

After the team is live, create the evaluation as a board task assigned to the orchestrator. `assigned_to` must exactly match the orchestrator's agent name (`Lead` here). Creating an assigned task posts an `@Lead` board notification and nudges its terminal to run `coral-board task claim`.

```bash
TASK=$(curl -fsS -X POST "$CORAL_URL/api/board/$EVAL_NAME/tasks" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n \
    --arg title "$EVAL_NAME" \
    --arg body 'Implement the requested change and coordinate the team as useful. Run the relevant tests. Commit all intended changes on the existing shared branch, push it, and open a GitHub pull request with gh pr create. Include the PR URL and test results in the task completion message. Do not complete the board task without either creating the PR or clearly reporting why it could not be created.' \
    '{title: $title, body: $body, priority: "high",
      created_by: "eval-harness", assigned_to: "Lead"}')")

TASK_ID=$(jq -r '.id' <<<"$TASK")
echo "$TASK" | jq
```

In a real harness, replace the example `body` with the evaluation-specific instructions. Wait until the team members appear in `/api/sessions/live` before creating the task; otherwise the task is still created, but the initial terminal nudge may arrive before the orchestrator subscription is ready.

Poll the team and its sessions:

```bash
curl -fsS "$CORAL_URL/api/teams/$EVAL_NAME" | jq
curl -fsS "$CORAL_URL/api/sessions/live" |
  jq --arg board "$EVAL_NAME" '[.[] | select(.board_project == $board)]'

# Poll the board task until it is completed or skipped.
while true; do
  TASK_STATE=$(curl -fsS "$CORAL_URL/api/board/$EVAL_NAME/tasks" |
    jq --argjson id "$TASK_ID" '.tasks[] | select(.id == $id)')
  echo "$TASK_STATE" | jq '{id, status, assigned_to, session_id, completion_message}'
  STATE=$(jq -r '.status' <<<"$TASK_STATE")
  case "$STATE" in completed|skipped) break ;; esac
  sleep 10
done
```

There is no separate Coral endpoint that creates a GitHub PR. The agent creates it with `git push` and `gh pr create`, so the Coral environment needs usable git credentials and GitHub CLI authentication.

When the team finishes, stop it:

```bash
curl -fsS -X POST "$CORAL_URL/api/sessions/live/team/$EVAL_NAME/kill" | jq
```

## Collect evaluation metrics

Enable token accounting before the run. Set `proxy_enabled` in Coral settings so launched agents use Coral's proxy; Coral may also ingest supported CLI JSONL usage. Verify it with `GET /api/settings` before starting.

For one session, retrieve aggregate tokens, total cost, and `num_turns`:

```bash
curl -fsS "$CORAL_URL/api/token-usage?session_id=$SESSION_ID" | jq
curl -fsS "$CORAL_URL/api/token-usage/session/$SESSION_ID/turns" | jq
```

The first response's `records` entry contains `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `total_tokens`, `cost_usd`, and `num_turns`. The second request returns the per-turn cost timeline.

For a team, query all member sessions by the returned `team_id`:

```bash
METRICS=$(curl -fsS "$CORAL_URL/api/token-usage?team_id=$TEAM_ID")

echo "$METRICS" | jq '{
  cost_usd: .totals.cost_usd,
  total_tokens: .totals.total_tokens,
  input_tokens: .totals.input_tokens,
  output_tokens: .totals.output_tokens,
  cache_read_tokens: .totals.cache_read_tokens,
  cache_write_tokens: .totals.cache_write_tokens,
  sessions: .totals.num_sessions,
  turns: ([.records[].num_turns] | add // 0),
  agents: [.records[] | {agent_name, agent_type, session_id, cost_usd, total_tokens, num_turns}]
}'
```

The completed board task also contains cost and token fields scoped to the interval from claim to completion. Retrieve the stored task result and the live task-cost view with:

```bash
curl -fsS "$CORAL_URL/api/board/$EVAL_NAME/tasks" |
  jq --argjson id "$TASK_ID" '.tasks[] | select(.id == $id) |
    {id, status, claimed_at, completed_at, session_id, cost_usd,
     input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
     completion_message}'

curl -fsS "$CORAL_URL/api/board/$EVAL_NAME/tasks/$TASK_ID/cost" | jq
```

Board-task cost is attributed to the session that claims the task—normally `Lead`—and covers its claim-to-completion window. Team-level metrics remain the correct measurement for total evaluation cost because work delegated to `Reviewer` uses a different session and is not included in the lead's task cost.

Board-name filtering is also available:

```bash
curl -fsS --get "$CORAL_URL/api/token-usage" \
  --data-urlencode "board_name=$EVAL_NAME" | jq
```

Query metrics after agents stop and Coral ingests final usage. If totals appear stale, wait for the next polling cycle and retry. A turn is an accounting record/API exchange observed by Coral; summing team-member `num_turns` gives total agent turns, not synchronized team rounds.

Finally, validate the external artifact independently. Searching by branch name is generally more reliable than searching for the evaluation label unless the prompt requires that label in the PR title:

```bash
gh pr list --repo "$(git -C "$REPO" remote get-url origin)" \
  --state all --json number,url,state,headRefName,title
```

For reproducible comparisons, retain the Coral version, exact base commit, complete launch JSON, agent CLI versions, model names, final run/team response, metrics JSON, PR URL, and test results.
