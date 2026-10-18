#!/usr/bin/env python3
# scale_transcripts.py PROJECTS_DIR LIVE.json OUT.json [TURNS_MEDIAN]
#
# #4669: real-shaped transcripts for the scale runs. For every live session in LIVE.json (the
# /api/sessions/live reply) it appends synthetic Claude Code turns to the session's JSONL file under
# PROJECTS_DIR (the isolated HOME's .claude/projects), chained from the file's last entry: a user prompt,
# assistant text and thinking, tool calls (Bash, Read, Edit, Grep) with results from a few bytes to tens of
# KB, and the turn_duration entry that ends the turn, as Claude Code writes them. The number of turns per
# session is log-normal around TURNS_MEDIAN (default 300, so a few MB a session, a handful of 20 MB ones),
# from a fixed seed, so two runs make the same transcripts. The files are only ever appended to, as live
# sessions' are. Writes the bytes and turns per session and the names of the largest to OUT.json.
# Stdlib only. Run it with HOME and CLAUDE_PROJECTS_DIR pointing into the isolated sandbox, never ~/.claude.
import datetime, json, math, os, pwd, random, re, sys, uuid

PROJECTS, LIVE, OUT = sys.argv[1], sys.argv[2], sys.argv[3]
MEDIAN = int(sys.argv[4]) if len(sys.argv) > 4 else 300
REAL = os.path.realpath(os.path.join(pwd.getpwuid(os.getuid()).pw_dir, ".claude"))  # the account's own, whatever HOME says
if (os.path.realpath(PROJECTS) + os.sep).startswith(REAL + os.sep):
    sys.exit("refusing to write into the real ~/.claude")
rnd = random.Random(4669)
WORDS = ("function handler request response error cache index session transcript window cursor offset budget limit "
         "refactor rename extract inline test fixture mock stub assert mutation oracle floor build vet lint format").split()
FILES = ["internal/server/routes/sessions_chat.go", "internal/jsonl/reader.go", "internal/board/store.go", "cmd/coral/main.go",
         "pkg/frontend/static/app.js", "internal/store/sessions.go", "internal/background/scheduler.go", "README.md", "go.mod"]


def enc(cwd):
    return re.sub(r"[^A-Za-z0-9-]", "-", cwd)


def text(lo, hi):
    return " ".join(rnd.choice(WORDS) for _ in range(rnd.randint(lo, hi)))


def blob(median_bytes):
    n = int(min(60000, max(8, rnd.lognormvariate(math.log(median_bytes), 1.2))))
    lines, size = [], 0
    while size < n:
        l = "%s %s:%d: %s" % (rnd.choice(FILES), rnd.choice(WORDS), rnd.randint(1, 900), text(3, 14))
        lines.append(l); size += len(l) + 1
    return "\n".join(lines)[:n]


def iso(t):
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + "%03dZ" % (t.microsecond // 1000)


class Writer:
    def __init__(self, sid, cwd, last, t):
        self.sid, self.cwd, self.last, self.t, self.out, self.n = sid, cwd, last, t, [], 0

    def entry(self, kind, **kw):
        self.t += datetime.timedelta(seconds=rnd.uniform(0.2, 6))
        u = str(uuid.UUID(int=rnd.getrandbits(128), version=4))
        e = {"parentUuid": self.last, "isSidechain": False, "type": kind, "uuid": u, "timestamp": iso(self.t), "userType": "external",
             "entrypoint": "cli", "cwd": self.cwd, "sessionId": self.sid, "version": "2.1.301", "gitBranch": "main"}
        e.update(kw)
        self.last = u; self.n += 1
        self.out.append(json.dumps(e, separators=(",", ":")))
        return e

    def turn(self, i):
        self.entry("user", message={"role": "user", "content": text(6, 80)}, permissionMode="default")
        usage = {"input_tokens": 1000 + 7 * i, "cache_creation_input_tokens": rnd.choice([0, 0, 4000]), "cache_read_input_tokens": 20000 + 11 * i, "output_tokens": rnd.randint(40, 1800), "service_tier": "standard"}
        for _ in range(rnd.choice([0, 1, 1, 2, 3, 5])):
            tid = "toolu_" + uuid.UUID(int=rnd.getrandbits(128)).hex[:22]
            tool = rnd.choice(["Bash", "Read", "Edit", "Grep", "Read"])
            inp = {"command": "go test ./internal/%s" % rnd.choice(WORDS), "description": text(2, 6)} if tool == "Bash" else \
                  {"file_path": "/work/" + rnd.choice(FILES)} if tool == "Read" else \
                  {"file_path": "/work/" + rnd.choice(FILES), "old_string": text(3, 20), "new_string": text(3, 30)} if tool == "Edit" else \
                  {"pattern": rnd.choice(WORDS), "path": "/work"}
            content = [{"type": "thinking", "thinking": text(20, 200), "signature": "sig"}] if rnd.random() < 0.3 else []
            content.append({"type": "tool_use", "id": tid, "name": tool, "input": inp})
            self.entry("assistant", message={"model": "claude-opus-5-5", "id": "msg_" + tid[6:], "type": "message", "role": "assistant", "content": content,
                                             "stop_reason": "tool_use", "stop_sequence": None, "usage": usage}, requestId="req_" + tid[6:])
            res = blob(1200)
            self.entry("user", message={"role": "user", "content": [{"type": "tool_result", "tool_use_id": tid, "content": res, "is_error": False}]},
                       toolUseResult={"stdout": res, "stderr": "", "interrupted": False})
        self.entry("assistant", message={"model": "claude-opus-5-5", "id": "msg_" + uuid.UUID(int=rnd.getrandbits(128)).hex[:22], "type": "message", "role": "assistant",
                                         "content": [{"type": "text", "text": text(15, 400)}], "stop_reason": "end_turn", "stop_sequence": None, "usage": usage},
                   requestId="req_" + uuid.UUID(int=rnd.getrandbits(128)).hex[:22])
        self.entry("system", subtype="turn_duration", durationMs=rnd.randint(800, 90000), messageCount=self.n + 1, isMeta=False)


def last_uuid(path):
    try:
        with open(path, "rb") as f:
            f.seek(0, 2); size = f.tell(); f.seek(max(0, size - 65536))
            for line in reversed(f.read().splitlines()):
                try:
                    return json.loads(line).get("uuid")
                except Exception:
                    continue
    except OSError:
        pass
    return None


live = json.load(open(LIVE))
sessions = [s for s in (live if isinstance(live, list) else live.get("sessions", [])) if s.get("session_id")]
res = {}
for s in sorted(sessions, key=lambda s: s["session_id"]):
    cwd = s.get("working_dir") or s.get("working_directory") or ""
    path = os.path.join(PROJECTS, enc(cwd), s["session_id"] + ".jsonl")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    turns = int(min(8000, max(30, rnd.lognormvariate(math.log(MEDIAN), 0.9))))
    w = Writer(s["session_id"], cwd, last_uuid(path), datetime.datetime.utcnow() - datetime.timedelta(days=rnd.uniform(0.2, 6)))
    with open(path, "ab") as f:
        for i in range(turns):
            w.turn(i)
            if len(w.out) > 400:
                f.write(("\n".join(w.out) + "\n").encode()); w.out = []
        if w.out:
            f.write(("\n".join(w.out) + "\n").encode())
    res[s["session_id"]] = {"name": s.get("display_name") or s.get("name"), "turns": turns, "entries": w.n, "bytes": os.path.getsize(path)}
big = sorted(res, key=lambda k: -res[k]["bytes"])
json.dump({"sessions": res, "largest": big[:5], "total_bytes": sum(v["bytes"] for v in res.values())}, open(OUT, "w"), indent=1)
print("transcripts: %d sessions, %.1f MB, largest %s (%.1f MB)" % (len(res), sum(v["bytes"] for v in res.values()) / 1e6, big[0] if big else "-", res[big[0]]["bytes"] / 1e6 if big else 0))
