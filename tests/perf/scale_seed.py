#!/usr/bin/env python3
# scale_seed.py BASE KEYFILE BOARD NMSG NTASKS OUT.json
#
# #4669: the long-lived board of the scale runs. Through the real API (so the FTS index, the numbered
# names and the task rows are what a Coral that has run for weeks holds) it posts NMSG messages and creates
# NTASKS tasks on BOARD as the Operator, then cancels about two thirds of the tasks (done tasks outnumber
# open ones, as on an old board). Message and task text is varied in length and shape (short chat, long
# pastes, @mentions, links, code) from a fixed seed, so two runs seed the same board. Stdlib only.
# Writes the seconds each phase took and the counts to OUT.json.
import json, random, sys, threading, time, urllib.parse, urllib.request

BASE, KEYFILE, BOARD, NMSG, NTASKS, OUT = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4]), int(sys.argv[5]), sys.argv[6]
KEY = open(KEYFILE).read().strip()
H = {"Origin": BASE, "Referer": BASE + "/", "Sec-Fetch-Site": "same-origin", "Authorization": "Bearer " + KEY, "Content-Type": "application/json"}
WORKERS = 8
rnd = random.Random(4669)
WORDS = ("build test merge review deploy rollback flake lint vet cache index schema migrate token session board task "
         "agent transcript window checkpoint redact budget deadline limit ceiling oracle mutation floor release gate").split()
NAMES = ["Orchestrator", "Lead Developer", "Backend Dev", "Frontend Dev", "QA Engineer", "Security Reviewer", "DevOps Engineer", "UX Engineer"]


def sentence(n):
    return " ".join(rnd.choice(WORDS) for _ in range(n)).capitalize() + "."


def message_text(i):
    k = rnd.random()
    if k < 0.55:
        t = sentence(rnd.randint(4, 30))
    elif k < 0.8:
        t = " ".join(sentence(rnd.randint(6, 20)) for _ in range(rnd.randint(2, 6)))
    elif k < 0.9:
        t = "@%s %s" % (rnd.choice(NAMES), sentence(rnd.randint(5, 25)))
    elif k < 0.96:
        t = "Result of #%d: %s\n```\n%s\n```" % (rnd.randint(1000, 4700), sentence(8), "\n".join("ok  \tpkg/%s\t%.3fs" % (rnd.choice(WORDS), rnd.random() * 9) for _ in range(rnd.randint(3, 25))))
    else:
        t = "\n".join(sentence(rnd.randint(8, 24)) for _ in range(rnd.randint(20, 80)))  # a long paste, a few KB
    return "%s (seed %d)" % (t, i)


out = {"board": BOARD, "messages": 0, "tasks": 0, "cancelled": 0}


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    for attempt in range(5):
        try:
            with urllib.request.urlopen(urllib.request.Request(BASE + path, data=data, headers=H, method=method), timeout=60) as f:
                return json.loads(f.read() or b"null")
        except urllib.error.HTTPError as e:
            if e.code == 429:
                time.sleep(0.5 * (attempt + 1)); continue
            out.setdefault("refused", {}).setdefault("%s %s" % (method, path.split("?")[0].rsplit("/", 1)[-1]), "%d %s" % (e.code, e.read()[:160].decode("utf-8", "replace")))
            return None
        except Exception:
            time.sleep(0.2)
    return None


def run(jobs, fn):
    q = list(jobs); lock = threading.Lock(); res = []

    def worker():
        while True:
            with lock:
                if not q: return
                j = q.pop()
            r = fn(j)
            with lock:
                res.append(r)
    ths = [threading.Thread(target=worker) for _ in range(WORKERS)]
    for t in ths: t.start()
    for t in ths: t.join()
    return res


t0 = time.time()
TEXTS = [message_text(i) for i in range(NMSG)]  # built here, on one thread, so the same seed gives the same board
TASKS = [{"title": "Task %d: %s" % (i, sentence(rnd.randint(3, 9))), "body": " ".join(sentence(rnd.randint(6, 20)) for _ in range(rnd.randint(1, 8))),
          "priority": rnd.choice(["low", "medium", "medium", "high"]), "subscriber_id": "operator", "created_by": "operator"} for i in range(NTASKS)]
msgs = run(range(NMSG), lambda i: call("POST", "/api/board/%s/messages" % urllib.parse.quote(BOARD),
                                       {"subscriber_id": "operator", "as": "Operator", "content": TEXTS[i]}) is not None)
out["messages"] = sum(1 for m in msgs if m); out["messages_s"] = time.time() - t0
t0 = time.time()


def make_task(i):
    r = call("POST", "/api/board/%s/tasks" % urllib.parse.quote(BOARD), TASKS[i])
    if not isinstance(r, dict): return None
    return r.get("id") or (r.get("task") or {}).get("id")


ids = [x for x in run(range(NTASKS), make_task) if x]
out["tasks"] = len(ids); out["tasks_s"] = time.time() - t0
t0 = time.time()
cancel = [i for n, i in enumerate(sorted(ids)) if n % 3 != 0]
done = run(cancel, lambda i: call("POST", "/api/board/%s/tasks/%s/cancel" % (urllib.parse.quote(BOARD), i), {"subscriber_id": "Operator", "message": "seeded as done"}) is not None)
out["cancelled"] = sum(1 for d in done if d); out["cancel_s"] = time.time() - t0
json.dump(out, open(OUT, "w"), indent=1)
print(json.dumps(out))
