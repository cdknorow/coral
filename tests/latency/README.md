# Dashboard latency-budget guard (#3713 / #3740)

Opt-in, **epoch-only** harness that measures the dashboard's hot read endpoints under a
realistic seeded dataset, idle and under a CPU hog, and fails if any endpoint's p95 under the
hog exceeds its budget. It is **not** part of the normal test suite: it needs a large seeded
dataset, real tmux sessions and a CPU hog, so it is only meaningful on the epoch box.

## Run (on epoch)

```bash
# against the checked-out ~/coral (Linux/epoch only; opt-in)
QA_LATENCY_RUN=1 bash tests/latency/run_all.sh

# or against an isolated worktree at a specific commit (recommended, leaves ~/coral alone):
QA_LATENCY_RUN=1 GO=$HOME/go-install/go/bin/go REPO=/path/to/worktree bash tests/latency/run_all.sh
# On epoch the data lands under /sd2/$USER/qa-tmp and is removed on exit (tests/EPOCH.md); KEEP_TEST_DATA=1 keeps it.
```

It builds `coral` + the seeder (`seed/`, copied into the module as `cmd/qa3713seed` only for the
build, then removed), seeds an isolated `CORAL_DATA_DIR`, starts the server with `--cloud-url
off`, measures `idle` then `hog`, prints a combined table, checks the budgets, and tears
everything down (killing only the PIDs it started). Exit code is non-zero if any p95 is over
budget. It refuses to run unless `uname -s` is Linux and `QA_LATENCY_RUN=1` is set, so it never runs on
the operator's Mac or in the normal suite. Env overrides: `GO, REPO, WORK, PORT, N, NSESS, NBIG,
BIGMB, FILESN, HOG` (defaults seed 24 sessions, 4×80 MB transcripts, 5000 file dirs, N=100
samples, HOG = 2×`nproc`). Each hog process carries a 900 s `timeout`, and the trap removes the
server, the hogs, the tmux socket and the seeder copy.

## Budgets (p95 ms, under hog)

| endpoint | budget | note |
|---|---|---|
| team_availability | 50 | |
| board_status | 50 | |
| sleep_status | 10 | |
| chat | 15 | #3745 made trimChatPage cheaper; the #3851 baseline measured chat under-hog p95 = 8.2 ms (~2x headroom) |
| poll | 30 | |
| files | 45 | |

Set from the #3740 after-numbers run on main `296439a2` with headroom. Update the `BUD` map in
`run_all.sh` (and this table) when the budgets change.
