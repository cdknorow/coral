#!/usr/bin/env python3
# bulk.py DATA_DIR TARGETS_JSON [N_MESSAGES] [N_EVENTS]
#
# Adds a realistic board/event backlog to an already-seeded isolated data dir (the
# tests/latency seeder makes the 40 sessions and writes TARGETS_JSON). Based on the
# #3833 load (Security Reviewer). Must run with the server STOPPED (it writes the
# SQLite files directly). Then sets board_subscribers.last_read_id near the head for
# most subscribers, and leaves a few stale: all-at-0 makes startup not serve for
# minutes, which is itself one of the things the harness measures when you want it.
import sqlite3, random, json, datetime, sys, os

data_dir = sys.argv[1]
targets = json.load(open(sys.argv[2]))
N = int(sys.argv[3]) if len(sys.argv) > 3 else 300000
M = int(sys.argv[4]) if len(sys.argv) > 4 else 250000
board = targets["board_id"]
random.seed(3833)

words = ("the board task agent reviewer commit patch tests passed failed redactor tunnel "
         "session token cache index poll chat history merge release notes verify epoch "
         "harness latency queue").split()

def text(n):
    out, L = [], 0
    while L < n:
        w = random.choice(words); out.append(w); L += len(w) + 1
    return " ".join(out)

now = datetime.datetime(2026, 10, 16, 10, 0, 0)

mb = sqlite3.connect(os.path.join(data_dir, "messageboard.db"))
subs = [r[0] for r in mb.execute("select subscriber_id from board_subscribers")]
if not subs:
    print("bulk.py: no board_subscribers found; seed the board first", file=sys.stderr); sys.exit(2)

rows = []
for i in range(N):
    r = random.random()
    n = random.randint(80, 500) if r < 0.8 else random.randint(1000, 2200) if r < 0.95 else random.randint(4000, 9000)
    s = random.choice(subs)
    ts = (now - datetime.timedelta(seconds=(N - i) * 4)).strftime("%Y-%m-%dT%H:%M:%SZ")
    rows.append((board, s, "[%s] %s" % (s, text(n)), ts, s))
    if len(rows) == 20000:
        mb.executemany("insert into board_messages(project,session_id,content,created_at,subscriber_id) values (?,?,?,?,?)", rows); rows = []
if rows:
    mb.executemany("insert into board_messages(project,session_id,content,created_at,subscriber_id) values (?,?,?,?,?)", rows)
mb.commit()
head = mb.execute("select max(id) from board_messages where project=?", (board,)).fetchone()[0] or 0

# Read cursors: most subscribers within 300 of the head, a few stale (60k unread).
stale = set(random.sample(subs, min(3, len(subs))))
for s in subs:
    last = max(0, head - 60000) if s in stale else max(0, head - random.randint(0, 300))
    mb.execute("update board_subscribers set last_read_id=? where subscriber_id=?", (last, s))
mb.commit()
print("messages", mb.execute("select count(*) from board_messages").fetchone()[0],
      "head", head, "stale_subs", len(stale))

sd = sqlite3.connect(os.path.join(data_dir, "sessions.db"))
sess = [r[0] for r in sd.execute("select session_id from live_sessions")]
if not sess:
    print("bulk.py: no live_sessions found; seed the sessions first", file=sys.stderr); sys.exit(2)
tools = ["Bash", "Read", "Edit", "Write", "Grep", "Glob"]
ev = []
for i in range(M):
    sid = random.choice(sess); t = random.choice(tools)
    ts = (now - datetime.timedelta(seconds=(M - i) * 5)).strftime("%Y-%m-%dT%H:%M:%SZ")
    if t == "Bash":
        cmd = "go test ./pkg/%s -run %s -count=1 && echo %s" % (random.choice(words), random.choice(words), text(60))
        summ = "Ran: " + cmd[:80]; det = json.dumps({"command": cmd})
    elif t in ("Edit", "Write", "Read"):
        fp = "/work/%s/%s.go" % (random.choice(words), random.choice(words))
        summ = ("Edited " if t == "Edit" else "Created " if t == "Write" else "Read ") + fp.split("/")[-1]
        det = json.dumps({"file_path": fp})
    else:
        summ = "Searched: " + random.choice(words); det = json.dumps({"pattern": random.choice(words)})
    ev.append(("agent-" + sid[:4], sid, "tool_use", t, summ, det, ts, random.randint(5, 4000)))
    if len(ev) == 25000:
        sd.executemany("insert into agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at,duration_ms) values (?,?,?,?,?,?,?,?)", ev); ev = []
if ev:
    sd.executemany("insert into agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at,duration_ms) values (?,?,?,?,?,?,?,?)", ev)
sd.commit()
print("events", sd.execute("select count(*) from agent_events").fetchone()[0], "sessions", len(sess))
