#!/usr/bin/env python3
# load.py BASE_URL TARGETS_JSON N_TABS SECONDS
#
# Simulates N dashboard tabs against a running Coral: each tab opens the /ws/coral feed
# (a raw websocket client) and runs the same pollers the real frontend does, at the real
# intervals. Prints a per-request table (n, avg ms, KB/req, errors). Based on the #3833
# tab simulator (Security Reviewer); config comes from argv, nothing is hardcoded.
import json, socket, threading, time, base64, os, sys, urllib.request, urllib.parse, collections

BASE = sys.argv[1]
T = json.load(open(sys.argv[2]))
NTABS = int(sys.argv[3])
DUR = float(sys.argv[4])
BOARD = T["board_id"]
host = urllib.parse.urlparse(BASE).hostname or "127.0.0.1"
port = urllib.parse.urlparse(BASE).port or 80
H = {"Origin": BASE, "Referer": BASE + "/", "Sec-Fetch-Site": "same-origin"}
# Strict local auth is the default (#3936): the test server's API key, from run_perf.sh.
KEY = os.environ.get("CORAL_API_KEY", "")
if KEY:
    H["Authorization"] = "Bearer " + KEY
stats = collections.defaultdict(lambda: [0, 0.0, 0, 0])  # n, total_s, bytes, errors
lock = threading.Lock()
stop = threading.Event()

def get(name, path):
    t = time.time()
    try:
        r = urllib.request.Request(BASE + path, headers=H)
        with urllib.request.urlopen(r, timeout=30) as f:
            body = f.read(); err = 0
    except Exception:
        body = b""; err = 1
    dt = time.time() - t
    with lock:
        s = stats[name]; s[0] += 1; s[1] += dt; s[2] += len(body); s[3] += err

def every(period, fn):
    nxt = time.time()
    while not stop.is_set():
        fn(); nxt += period
        d = nxt - time.time()
        if d > 0: stop.wait(d)
        else: nxt = time.time()

def ws_tab():
    try:
        s = socket.create_connection((host, port), timeout=10)
    except Exception:
        return
    key = base64.b64encode(os.urandom(16)).decode()
    s.sendall(("GET /ws/coral HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
               "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nOrigin: %s\r\n%s\r\n"
               % (host, port, key, BASE, ("Authorization: Bearer %s\r\n" % KEY) if KEY else "")).encode())
    s.settimeout(5); buf = b""
    try:
        while b"\r\n\r\n" not in buf:
            buf += s.recv(4096)
    except Exception:
        return
    n = 0; nbytes = 0
    while not stop.is_set():
        try:
            d = s.recv(65536)
            if not d: break
            n += 1; nbytes += len(d)
        except socket.timeout:
            continue
        except Exception:
            break
    with lock:
        st = stats["WS /ws/coral (recv chunks)"]; st[0] += n; st[2] += nbytes

def tab(i):
    chat = T["chat"] if i == 0 else T["poll"]
    q = urllib.parse.urlencode({"agent_type": "claude", "session_id": chat["id"],
                                "working_directory": chat.get("working_dir", ""),
                                "after": "0", "limit": "30", "trim": "2048"})
    specs = [
        (1.0, "chat poll 1s",         "/api/sessions/live/%s/chat?%s" % (chat["name"], q)),
        (2.0, "poll 2s",              "/api/sessions/live/%s/poll?agent_type=claude&session_id=%s" % (T["poll"]["name"], T["poll"]["id"])),
        (3.0, "board chat 3s",        "/api/board/%s/messages/all?limit=50&format=dashboard" % BOARD),
        (5.0, "team availability 5s", "/api/teams/detail/%s/availability" % BOARD),
        (5.0, "board status 5s",      "/api/board/%s/status" % BOARD),
        (5.0, "sleep status 5s",      "/api/sessions/live/team/%s/sleep-status" % BOARD),
        (10.0, "board tasks 10s",     "/api/board/%s/tasks" % BOARD),
        (15.0, "files list 15s",      "/api/filesystem/list?path=" + urllib.parse.quote(T["files_path"])),
    ]
    ths = [threading.Thread(target=ws_tab, daemon=True)]
    for period, name, path in specs:
        ths.append(threading.Thread(target=every, args=(period, (lambda n, p: (lambda: get(n, p)))(name, path)), daemon=True))
    for t in ths:
        t.start()
    return ths

allt = []
for i in range(NTABS):
    allt += tab(i)
time.sleep(DUR)
stop.set()
time.sleep(1.5)
print("%-28s %8s %9s %9s %5s" % ("request (all tabs)", "n", "avg ms", "KB/req", "err"))
for k, (n, tot, b, e) in sorted(stats.items()):
    print("%-28s %8d %9.1f %9.1f %5d" % (k, n, (tot / n * 1000 if n else 0), (b / n / 1024 if n else 0), e))
