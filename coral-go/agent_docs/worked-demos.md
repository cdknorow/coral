# Worked Demos

Three multi-agent scenarios run end to end against the shipped v1.0.8 build on a real
repository. Commands and output are real. Where a demo did not behave as the docs
promised, the result is reported as it happened rather than staged.

Setup shared by all three: a small Go package at `/tmp/coral-demo` (`Reverse`, plus
`Capitalize` from the [quickstart](quickstart.md)), and a server started with

```bash
coral --home /tmp/coral-t1 --host 127.0.0.1 --port 8452 --no-browser
```

---

## Demo 1 — Two different agent CLIs on one repository

**What works:** Claude Code and Codex running simultaneously on one repo under one
dashboard, each producing a competent implementation.

**What does not:** they are not isolated from each other, and they will overwrite each
other's work.

```console
$ curl -s -X POST http://127.0.0.1:8452/api/sessions/launch-team \
   -H "Content-Type: application/json" -d '{
   "board_name":"demo-worktrees","working_dir":"/tmp/coral-demo",
   "worktree":true,"base_branch":"main",
   "agents":[
     {"name":"claude-impl","agent_type":"claude","prompt":"Add Truncate(s string, n int) string ..."},
     {"name":"codex-impl","agent_type":"codex","prompt":"Add Truncate(s string, n int) string ..."}
   ]}'
{"agents":[
  {"name":"claude-impl","worktree_path":"/tmp/coral-t1/worktrees/demo-worktrees"},
  {"name":"codex-impl", "worktree_path":"/tmp/coral-t1/worktrees/demo-worktrees"}
 ],"board":"demo-worktrees","ok":true,"team_id":1}
```

Both agents received the **same** `worktree_path`.

```console
$ git -C /tmp/coral-demo worktree list
/private/tmp/coral-demo                         0392df2 [main]
/private/tmp/coral-t1/worktrees/demo-worktrees  0392df2 [coral-team/demo-worktrees]
```

One worktree, named after the *board*, on branch `coral-team/<board>`.

### Worktrees are per team, not per agent

Given both agents the same function to write, deliberately:

```console
$ grep -n 'func Truncate' stringutil.go
25:func Truncate(s string, n int) string {
38:func Truncate(s string, n int) string {

$ go build ./...
./stringutil.go:38:6: Truncate redeclared in this block
        ./stringutil.go:25:6: other declaration of Truncate

$ go test ./...
FAIL    example.com/stringutil [build failed]
```

Two complete, individually correct implementations stacked in one file. They differ in
edge-case handling (`n <= 0` vs `n < 0`), so this is two genuine authors colliding, not a
trivial duplicate. The repository no longer compiles.

### And worktrees are off by default

Launching a team **without** the `worktree` flag — the default in the dashboard, where the
checkbox has no `checked` attribute — creates no worktree at all:

```console
$ curl -s -X POST .../api/sessions/launch-team -d '{"board_name":"demo-default",
    "working_dir":"/tmp/coral-demo","agents":[{"name":"a1",...},{"name":"a2",...}]}'
{"agents":[{"name":"a1",...},{"name":"a2",...}],"ok":true}   # no worktree_path

$ curl -s .../api/sessions/live
  a1 -> /private/tmp/coral-demo | branch: main
  a2 -> /private/tmp/coral-demo | branch: main
```

Both agents in your real checkout, on your real branch.

### What is actually true

Coral has **two different worktree behaviors**, and only one of them isolates. Always say
which path you mean.

| Path | Worktree | Default |
|---|---|---|
| **Team launch**, default | none — agents run in your working directory on your current branch | off |
| **Team launch**, `worktree: true` | **one**, `coral-team/<board>`, shared by the whole team | off |
| **Scheduled job / task run** | **one per run**, `<repo>_task_run_<runID>` — genuinely isolated | on |

Either way, each agent does get its own tmux session.

Jobs are the path *designed* to behave the way the README described teams as behaving —
`internal/background/scheduler.go:551` keys the worktree on `runID` and `create_worktree`
defaults to `true`.

> **Correction — do not rely on this yet.** An earlier version of this page recommended
> scheduled jobs as the workaround for per-agent isolation. That recommendation was based on
> reading `scheduler.go`, not running it. **On the documented default configuration every job
> run fails**, because `git worktree add <dir> main` is rejected when `main` is checked out in
> your repo. The per-run worktree code is correct and never executes. See
> [Troubleshooting](troubleshooting.md#every-scheduled-job-run-fails). Jobs do work if
> `base_branch` names a branch that is not checked out anywhere — verified — but the run then
> ends in `killed` / `timeout` rather than `completed`.

**Practical guidance:** give agents non-overlapping files, or split work across separate
teams. Use the message board to sequence edits to shared files. Do not rely on
worktrees to prevent agents from clobbering each other — that isolation does not exist.

---

## Demo 2 — Implementation and review across two agents

**Result: works — after working around two bugs.** This is the strongest demonstrated
capability, but it did **not** work out of the box on a non-default port or data
directory (see [Two bugs had to be worked around](#two-bugs-had-to-be-worked-around)).

Two agents on one board: `implementer` writes the code, `reviewer` waits for the handoff
and reviews it.

```console
$ curl -s http://127.0.0.1:8452/api/board/demo-review/messages/all
[1] reviewer    06:38:06  @implementer Reviewer here. Standing by to review IsPalindrome
                          in stringutil.go once @implementer says it's ready.
[2] implementer 06:39:30  @reviewer IsPalindrome is committed, please review it
[3] reviewer    06:39:57  @implementer REVIEW: Logic is correct (rune-safe, case-insensitive,
                          empty/single-rune fine) and vet+tests pass — but it ships with zero
                          tests, unlike Reverse/Capitalize; add a table test before merge.
```

**27 seconds** from handoff to a posted verdict — after working around two bugs (below);
this did not work out of the box on a non-default port. The commit was real
(`7aefe09 add IsPalindrome`).

The review is substantive: the reviewer noticed the new function shipped without tests
*while the neighbouring functions have them*, inferring a project convention from
surrounding code. Nothing in its prompt asked it to check that.

### Two bugs had to be worked around

This did not work out of the box on a non-default data directory.

1. **Agents do not receive `CORAL_PORT` / `CORAL_URL`.** The generated settings file
   contains only `CORAL_SESSION_NAME`, `CORAL_SUBSCRIBER_ID`, and `PATH`, so `coral-board`
   falls back to `http://localhost:8420` regardless of the server's port
   (`cmd/coral-board/main.go:25-28`).
2. **Board subscription state is read from `~/.coral`, always.**
   `stateFilePath()` builds `$HOME/.coral/board_state_<session>.json`
   (`cmd/coral-board/main.go:41-44`) and never consults the server. With
   `--home /tmp/coral-t1` the server wrote the state file to the right place and the CLI
   looked in the wrong one:

   ```console
   $ ls /tmp/coral-t1/board_state_claude-12953c2f-*.json   # exists
   $ ls ~/.coral/board_state_claude-12953c2f-*.json        # no such file
   ```

   The agents reported `Not subscribed to any board` while the server showed both
   correctly subscribed.

The demo was completed by having the agents call the REST API directly. The board itself
worked first try — the CLI wrapper is what fails.

```bash
curl -s -X POST http://127.0.0.1:8452/api/board/demo-review/messages \
  -H "Content-Type: application/json" \
  -d '{"subscriber_id":"implementer","content":"@reviewer ready for review"}'

curl -s http://127.0.0.1:8452/api/board/demo-review/messages/all
```

Note the field is `content`, not `message`; `PostMessage` rejects empty content.

> On a default install (`~/.coral`, port 8420) neither bug is visible.

### An agent blocked on an unreachable board does not do its work

Before the workaround, both agents spent their opening turns troubleshooting the board and
then waited indefinitely:

```
claude: coral-board post fails with: Not subscribed to any board.
codex:  Introduction post was attempted, but Coral reported this process is not
        subscribed... and am waiting for Orchestrator notification.
```

Two agents that looked alive, consumed tokens, and produced nothing, with no error
surfaced in the UI. If a team stalls, check that agents can actually reach the board.

---

## Demo 3 — Sleep a team and wake it with history intact

**Result: works exactly as documented.** No caveats found.

```console
$ curl -s -X POST .../api/sessions/live/team/demo-sleepwake/sleep
{"board_paused":true,"ok":true,"sessions_affected":1,"sessions_killed":1,"sleeping":true}

$ tmux -S /tmp/coral-t1/tmux.sock ls
no server running on /tmp/coral-t1/tmux.sock
```

The process is genuinely terminated, not idled.

```console
$ curl -s -X POST .../api/sessions/live/team/demo-sleepwake/wake
{"board_paused":false,"ok":true,"sessions_relaunched":1,"sleeping":false}

$ tmux -S /tmp/coral-t1/tmux.sock ls
claude-6154d211-8150-8ca8-6fe7-0b5711729f13: 1 windows
```

The session returns under the same `session_id`, with prior scrollback restored. Asked to
recall a fact from before the sleep, without reading any files:

```
> Without re-reading any files, what MEMORY-TOKEN did you state earlier and
  what did you say the capital of the demo was?

  MEMORY-TOKEN-7731, and I said the capital of the demo was Coralville.
```

**Timings:** sleep under 1 s; wake API 1 s; agent answering from restored context within
about 15 s.

### Restarting Coral does not lose the context

Re-run with the server process killed between sleep and wake:

```console
$ POST .../team/demo-restart/sleep
{"board_paused":true,"ok":true,"sessions_killed":1,"sleeping":true}

$ kill <server pid>          # server and its tmux server both gone
$ curl .../                  # 000 — down

$ <start a new server process against the same --home>
$ GET .../team/demo-restart/sleep-status
{"sleeping":true}            # the fresh process knows, so state is on disk

$ POST .../team/demo-restart/wake
{"board_paused":false,"ok":true,"sessions_relaunched":1,"sleeping":false}
```

Asked to recall a fact from before the sleep, the woken agent answered it. The restored
scrollback also contains the pre-restart line, so check that the answer is a *new* reply
below your question rather than the replayed original — the first check here matched the
replay and had to be redone.

The board read cursor persists the same way: `last_read_id` was unchanged across a process
kill, already-read messages were not re-delivered, and unread ones were not skipped.

**Not tested:** a machine **reboot** (the tests above killed the server process, not the
host), large multi-agent teams, long intervals, and how much scrollback survives. Say
"quit Coral, restart it, and wake the team with context intact" — not "survives reboots".

> A killed session also reports `sleeping: true` in `/api/sessions/live`, so that field
> currently conflates "slept by the user" with "killed".

---

## Summary

| Demo | Result |
|---|---|
| Two agent CLIs on one repo | Works. **Per-agent isolation does not exist** — agents overwrite each other. |
| Implementation + review over the board | Works, and produces genuinely useful review. Needs a default data dir. |
| Sleep and wake with history | Works as documented, including across a server restart. |
