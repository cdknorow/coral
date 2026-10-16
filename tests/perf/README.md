# Performance baseline harness (#3851, supports #3845–#3847)

One reusable command that reproduces the #3833 measurement each time and prints the **same
table**, so the perf tasks have a stable baseline to compare against. It merges Security's
#3833 load setup (bulk dataset, dashboard-tab simulator, pprof, strace execve census) with
QA's #3713 latency harness (the seeder and `measure.sh` under `tests/latency/`).

**Measurement only — no product change.** Epoch-only and opt-in: it seeds a large dataset,
starts real tmux sessions, and runs pprof + strace, so it must never run on the operator's
Mac or in the normal suite. It never touches `~/.coral` or `:8420`; the server runs with an
isolated `CORAL_DATA_DIR`, `CORAL_CLOUD_URL` unset and `--cloud-url off`. It kills **only the
PIDs it started** (no pattern-kills) and removes its temp dirs on exit.

## Run (on epoch)

```bash
# against the checked-out ~/coral
QA_PERF_RUN=1 bash tests/perf/run_perf.sh

# or against an isolated worktree at a specific commit (recommended)
QA_PERF_RUN=1 GO=$HOME/go-install/go/bin/go REPO=/path/to/worktree WORK=$HOME/perf-run bash tests/perf/run_perf.sh
```

The script re-execs itself under `nice -n 10`. Env overrides: `GO, REPO, WORK, QA, PORT,
PPROF_PORT, N, NSESS, NBIG, BIGMB, SMALLKB, FILESN, NMSG, NEVENT, TABS, WARMUP_S, CPU_S,
LOAD_S, STRACE_S` (defaults match #3833: 40 sessions, 300k board messages, 250k events, 2
tabs, a 150 s warm-up, 60 s CPU profiles, a 180 s load phase, 120 s strace windows).

## What it prints (the table)

- **Startup-to-serving**: seconds from launching the server to the first `/api/health` 200,
  with realistic read cursors (most subscribers near the head; `bulk.py` seeds them, because
  all-at-0 makes startup not serve for minutes — itself a finding, #3833 item 4).
- **CPU per component** (idle, and under the tabs): the flat/cum% of the top functions from
  `go tool pprof -top` on the 60 s CPU profiles.
- **Latency per endpoint**: p50/p95/p99/max for the dashboard's hot reads, idle and under the
  tabs (reuses `tests/latency/measure.sh`).
- **Fork/exec rate**: execve per second idle and under load, from the server started under
  `strace -f -e trace=execve` (strace cannot attach to a running process on epoch), plus a
  per-program breakdown (tmux / git / ps / uname / …).

## Files

- `run_perf.sh` — the orchestrator (build, seed, phases A idle / B tabs / C strace, table, cleanup).
- `bulk.py DATA_DIR TARGETS_JSON [NMSG] [NEVENT]` — adds the board/event backlog and seeds read cursors (run with the server stopped).
- `load.py BASE TARGETS_JSON TABS SECONDS` — the dashboard-tab simulator (ws feed + the real pollers).
- `table.py OUT CORAL_BIN …` — assembles the one table from the collected artifacts.

It reuses `tests/latency/seed` (the dataset seeder, which also writes `targets.json`) and
`tests/latency/measure.sh` (the percentile sampler), so the two harnesses stay in step.

## Pitfalls baked in (from #3833)

- Never `pkill -f` a path over ssh (it matches the ssh command too); kill recorded PIDs only.
- `wait` on specific PIDs, not a bare `wait` (which also waits on the server you started).
- Long jobs start with `setsid … < /dev/null` so an ssh session doesn't stay attached.
- strace is **started with** the server, not attached (epoch's `ptrace_scope`).
- Record the machine load average with every table (epoch is shared with other agents' runs).
