#!/usr/bin/env python3
# scale_load.py BASE KEYFILE PID PPROF_PORT TABS SECONDS OUT.json [POST_PER_SEC]
#
# #4598: the load side of the scalability runs (scale.sh). Against a running Coral with many mock
# agents it simulates TABS dashboard tabs (the /ws/coral feed, read frame by frame, and the real
# pollers at the real intervals), posts to the first board at POST_PER_SEC (a busy board), and
# samples the server's process (CPU, RSS, threads from /proc; goroutines from the loopback pprof).
# It writes p50/p95/p99/max per request, WebSocket frames and bytes per second, and the process
# series to OUT.json. Stdlib only; the API key is read from KEYFILE.
import base64, collections, json, os, socket, struct, sys, threading, time, urllib.parse, urllib.request

BASE, KEYFILE, PID, PPORT, NTABS, DUR, OUT = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], int(sys.argv[5]), float(sys.argv[6]), sys.argv[7]
POST_RATE = float(sys.argv[8]) if len(sys.argv) > 8 else 2.0
KEY = open(KEYFILE).read().strip()
host = urllib.parse.urlparse(BASE).hostname or "127.0.0.1"
port = urllib.parse.urlparse(BASE).port or 80
H = {"Origin": BASE, "Referer": BASE + "/", "Sec-Fetch-Site": "same-origin", "Authorization": "Bearer " + KEY}
lat = collections.defaultdict(list)   # name -> [seconds]
errs = collections.Counter()
nbytes = collections.Counter()
lock = threading.Lock()
stop = threading.Event()
ws = {"frames": 0, "bytes": 0, "connected": 0, "failed": 0, "per_sec": collections.Counter()}

def call(method, path, body=None, name=None):
    name = name or path
    t = time.time()
    try:
        data = json.dumps(body).encode() if body is not None else None
        h = dict(H)
        if data: h["Content-Type"] = "application/json"
        with urllib.request.urlopen(urllib.request.Request(BASE + path, data=data, headers=h, method=method), timeout=30) as f:
            b = f.read()
        ok = True
    except Exception:
        b, ok = b"", False
    dt = time.time() - t
    with lock:
        lat[name].append(dt); nbytes[name] += len(b)
        if not ok: errs[name] += 1
    return b if ok else None

def every(period, fn):
    nxt = time.time() + (hash(fn) % 1000) / 1000.0 * period  # spread the tabs' first ticks
    while not stop.is_set():
        d = nxt - time.time()
        if d > 0 and stop.wait(d): break
        fn(); nxt += period
        if nxt < time.time(): nxt = time.time()

def read_frame(s, buf):
    """One websocket frame (server frames are unmasked): (payload_len, rest of buf) or None."""
    while len(buf) < 2:
        d = s.recv(65536)
        if not d: return None
        buf += d
    ln = buf[1] & 0x7F; hdr = 2
    if ln == 126:
        while len(buf) < 4: 
            d = s.recv(65536)
            if not d: return None
            buf += d
        ln = struct.unpack(">H", buf[2:4])[0]; hdr = 4
    elif ln == 127:
        while len(buf) < 10:
            d = s.recv(65536)
            if not d: return None
            buf += d
        ln = struct.unpack(">Q", buf[2:10])[0]; hdr = 10
    while len(buf) < hdr + ln:
        d = s.recv(1 << 20)
        if not d: return None
        buf += d
    return ln, buf[hdr + ln:]

def ws_tab():
    try:
        s = socket.create_connection((host, port), timeout=10)
        key = base64.b64encode(os.urandom(16)).decode()
        s.sendall(("GET /ws/coral?diff=fields HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
                   "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nOrigin: %s\r\nAuthorization: Bearer %s\r\n\r\n"
                   % (host, port, key, BASE, KEY)).encode())
        buf = b""
        while b"\r\n\r\n" not in buf: buf += s.recv(4096)
        buf = buf.split(b"\r\n\r\n", 1)[1]
    except Exception:
        with lock: ws["failed"] += 1
        return
    with lock: ws["connected"] += 1
    s.settimeout(2)
    t0 = time.time()
    while not stop.is_set():
        try:
            r = read_frame(s, buf)
        except socket.timeout:
            buf = b""; continue
        except Exception:
            break
        if r is None: break
        ln, buf = r
        with lock:
            ws["frames"] += 1; ws["bytes"] += ln; ws["per_sec"][int(time.time() - t0)] += 1

def tab(i, sessions, board):
    me, other = sessions[i % len(sessions)], sessions[(i + 1) % len(sessions)]
    q = urllib.parse.urlencode({"agent_type": "claude", "session_id": me["session_id"], "working_directory": me.get("working_dir", ""), "after": "0", "limit": "30", "trim": "2048"})
    specs = [
        (1.0, "chat poll 1s", "/api/sessions/live/%s/chat?%s" % (me["name"], q)),
        (2.0, "poll 2s", "/api/sessions/live/%s/poll?agent_type=claude&session_id=%s" % (other["name"], other["session_id"])),
        (3.0, "board chat 3s", "/api/board/%s/messages/all?limit=50&format=dashboard" % board),
        (5.0, "board status 5s", "/api/board/%s/status" % board),
        (5.0, "sleep status 5s", "/api/sessions/live/team/%s/sleep-status" % board),
        (10.0, "board tasks 10s", "/api/board/%s/tasks" % board),
        (10.0, "live list 10s", "/api/sessions/live"),
        (15.0, "token usage 15s", "/api/token-usage/summary"),
    ]
    ths = [threading.Thread(target=ws_tab, daemon=True)]
    for period, name, path in specs:
        ths.append(threading.Thread(target=every, args=(period, (lambda n, p: (lambda: call("GET", p, name=n)))(name, path)), daemon=True))
    for t in ths: t.start()
    return ths

def poster(board):
    n = [0]
    def post():
        n[0] += 1
        call("POST", "/api/board/%s/messages" % board, {"subscriber_id": "operator", "as": "Operator", "content": "scale message %d" % n[0]}, name="board post")
    if POST_RATE > 0: every(1.0 / POST_RATE, post)

def proc_sample():
    tck = os.sysconf("SC_CLK_TCK") if hasattr(os, "sysconf") else 100
    out = {"t": [], "cpu_pct": [], "rss_mb": [], "threads": [], "goroutines": []}
    last = None
    while not stop.is_set():
        try:
            st = open("/proc/%d/stat" % PID).read().rsplit(")", 1)[1].split()
            cpu = (int(st[11]) + int(st[12])) / tck; now = time.time()
            rss = threads = 0
            for l in open("/proc/%d/status" % PID):
                if l.startswith("VmRSS:"): rss = int(l.split()[1]) / 1024
                elif l.startswith("Threads:"): threads = int(l.split()[1])
            g = 0
            try:
                with urllib.request.urlopen("http://127.0.0.1:%s/debug/pprof/goroutine?debug=1" % PPORT, timeout=10) as f:
                    first = f.readline().decode()
                g = int(first.split("total")[1])
            except Exception: pass
            if last: out["cpu_pct"].append(100 * (cpu - last[0]) / (now - last[1]))
            else: out["cpu_pct"].append(0.0)
            last = (cpu, now)
            out["t"].append(now); out["rss_mb"].append(rss); out["threads"].append(threads); out["goroutines"].append(g)
        except Exception:
            pass
        stop.wait(2.0)
    return out

def pct(v, p):
    if not v: return 0.0
    v = sorted(v); return v[min(len(v) - 1, int(round(p / 100.0 * (len(v) - 1))))]

live = json.loads(call("GET", "/api/sessions/live", name="setup") or b"[]")
sessions = [s for s in (live if isinstance(live, list) else live.get("sessions", [])) if s.get("session_id") and s.get("name")]
if not sessions: sys.exit("no live sessions")
boards = sorted({s.get("board_project") for s in sessions if s.get("board_project")})
board = boards[0] if boards else ""
lat.pop("setup", None)
samp = {}
th = threading.Thread(target=lambda: samp.update(proc_sample()), daemon=True); th.start()
tabs = []
for i in range(NTABS): tabs += tab(i, sessions, board)
if board and POST_RATE > 0:
    threading.Thread(target=poster, args=(board,), daemon=True).start()
time.sleep(DUR)
stop.set(); time.sleep(2.5); th.join(5)
res = {"agents": len(sessions), "tabs": NTABS, "seconds": DUR, "board": board, "requests": {}, "ws": {}, "process": {}}
for k, v in sorted(lat.items()):
    res["requests"][k] = {"n": len(v), "p50_ms": pct(v, 50) * 1000, "p95_ms": pct(v, 95) * 1000, "p99_ms": pct(v, 99) * 1000,
                          "max_ms": max(v) * 1000, "kb_per_req": nbytes[k] / max(1, len(v)) / 1024, "errors": errs[k]}
ps = ws["per_sec"]
res["ws"] = {"connected": ws["connected"], "failed": ws["failed"], "frames": ws["frames"], "bytes": ws["bytes"],
             "frames_per_sec_per_tab": ws["frames"] / max(1.0, DUR) / max(1, ws["connected"]),
             "kb_per_sec_per_tab": ws["bytes"] / 1024.0 / max(1.0, DUR) / max(1, ws["connected"])}
if samp.get("cpu_pct"):
    c, g = samp["cpu_pct"][1:] or [0], [x for x in samp["goroutines"] if x] or [0]
    res["process"] = {"cpu_pct_avg": sum(c) / len(c), "cpu_pct_max": max(c), "rss_mb_max": max(samp["rss_mb"]), "rss_mb_end": samp["rss_mb"][-1],
                      "threads_max": max(samp["threads"]), "goroutines_first": g[0], "goroutines_max": max(g), "goroutines_end": g[-1]}
json.dump(res, open(OUT, "w"), indent=1)
print(json.dumps(res["process"]), json.dumps(res["ws"]))
