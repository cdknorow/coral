#!/usr/bin/env python3
# scale_table.py LABEL=OUT_DIR [LABEL=OUT_DIR ...]   (#4598)
# One table from several scale.sh result folders (for example "25=out25 50=out50 100=out100", or a
# before/after pair "before-100=a after-100=b"): process (CPU, RSS, threads, goroutines), WebSocket
# fan-out per tab, the p50/p99 of each poll endpoint, lock errors in the server log, and launch time.
import json, os, sys

runs = []
for a in sys.argv[1:]:
    label, d = a.split("=", 1)
    runs.append((label, d, json.load(open(os.path.join(d, "load.json")))))
if not runs:
    sys.exit(__doc__)

def num(path, default=""):
    try: return open(path).read().strip()
    except Exception: return default

cols = [r[0] for r in runs]
w = max(10, max(len(c) for c in cols) + 2)
def row(name, vals): print("%-30s" % name + "".join("%*s" % (w, v) for v in vals))
row("", cols)
row("live agents", [num(os.path.join(d, "live-count.txt")) for _, d, _ in runs])
row("launch time (s)", [num(os.path.join(d, "launch-seconds.txt")) for _, d, _ in runs])
for key, name, f in [("cpu_pct_avg", "server CPU avg (% of 1 core)", "%.0f"), ("cpu_pct_max", "server CPU max (%)", "%.0f"), ("rss_mb_max", "RSS max (MB)", "%.0f"),
                     ("threads_max", "threads max", "%d"), ("goroutines_first", "goroutines at start", "%d"), ("goroutines_end", "goroutines at end", "%d")]:
    row(name, [(f % r["process"][key]) if r["process"].get(key) is not None and r["process"] else "n/a" for _, _, r in runs])
row("ws connected tabs", [r["ws"]["connected"] for _, _, r in runs])
row("ws frames/s per tab", ["%.1f" % r["ws"]["frames_per_sec_per_tab"] for _, _, r in runs])
row("ws KB/s per tab", ["%.1f" % r["ws"]["kb_per_sec_per_tab"] for _, _, r in runs])
names = sorted({k for _, _, r in runs for k in r["requests"]})
for k in names:
    row(k + " p50/p99 ms", ["%.0f/%.0f" % (r["requests"][k]["p50_ms"], r["requests"][k]["p99_ms"]) if k in r["requests"] else "-" for _, _, r in runs])
row("request errors (all)", [sum(v["errors"] for v in r["requests"].values()) for _, _, r in runs])
row("lock/busy lines in log", [num(os.path.join(d, "lock-errors-count.txt"), "?") for _, d, _ in runs])
