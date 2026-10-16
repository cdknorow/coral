#!/usr/bin/env python3
# table.py OUT_DIR CORAL_BIN STRACE_S TABS NSESS NMSG NEVENT "LOADAVG..."
#
# Assembles the one baseline table from the artifacts run_perf.sh collected: startup-to-serving,
# CPU per component (go tool pprof -top on the idle and load CPU profiles), p50/p95 per hot
# endpoint (idle and under load), and the fork/exec census (execve counts + a per-binary
# breakdown from the strace output). `go` is taken from $GO.
import csv, os, re, subprocess, sys, collections

OUT = sys.argv[1]; BIN = sys.argv[2]
STRACE_S = float(sys.argv[3]); TABS = sys.argv[4]
NSESS, NMSG, NEVENT = sys.argv[5], sys.argv[6], sys.argv[7]
LOADAVG = sys.argv[8] if len(sys.argv) > 8 else ""
GO = os.environ.get("GO", "go")

def read(p):
    try:
        return open(os.path.join(OUT, p)).read().strip()
    except Exception:
        return ""

def rule(title): print("\n" + title + "\n" + "-" * len(title))

print("=" * 70)
print("Coral performance baseline (#3851)")
print("=" * 70)
print(f"load: {NSESS} sessions, {NMSG} board messages, {NEVENT} agent events, {TABS} dashboard tabs")
print(f"machine load average during run: {LOADAVG}")

rule("Startup-to-serving (listener accepts /api/health)")
sa, sc = read("startup_A.txt"), read("startup_C.txt")
print(f"  seeded start (realistic read cursors): {sa or '?'} s")
if sc: print(f"  restart for the execve census:         {sc} s")

# ---- CPU per component (pprof top) ----
def pprof_top(profile, n=12):
    path = os.path.join(OUT, profile)
    if not os.path.exists(path): return None, []
    try:
        o = subprocess.run([GO, "tool", "pprof", "-top", f"-nodecount={n}", BIN, path],
                           capture_output=True, text=True, timeout=120)
    except Exception as e:
        return None, [("pprof failed: %s" % e, "", "")]
    total = None; rows = []
    for ln in o.stdout.splitlines():
        m = re.match(r"Duration: .*Total samples = ([0-9.]+)(\w+)", ln) or re.search(r"Total samples = ([0-9.]+)(\w*)", ln)
        if m: total = m.group(1) + m.group(2)
        # flat flat% sum% cum cum% name
        mm = re.match(r"\s*([0-9.]+\w?s?)\s+([0-9.]+)%\s+[0-9.]+%\s+([0-9.]+\w?s?)\s+([0-9.]+)%\s+(.+)", ln)
        if mm:
            rows.append((mm.group(2), mm.group(4), mm.group(5).strip()))  # flat%, cum%, func
    return total, rows

for label, prof in (("idle", "cpu_A.pprof"), ("under load (%s tabs)" % TABS, "cpu_B.pprof")):
    total, rows = pprof_top(prof)
    rule(f"CPU per component — {label}" + (f"  (total samples {total})" if total else ""))
    if not rows:
        print("  (no profile)"); continue
    print(f"  {'flat%':>6} {'cum%':>6}  function")
    for flat, cum, fn in rows:
        print(f"  {flat:>6} {cum:>6}  {fn[:78]}")

# ---- latency per endpoint ----
def load_csv(p):
    path = os.path.join(OUT, p)
    if not os.path.exists(path): return {}
    return {r["endpoint"]: r for r in csv.DictReader(open(path))}

idle, load = load_csv("idle.csv"), load_csv("load.csv")
rule("Latency per endpoint (ms): p50 / p95 / p99 / max")
eps = list(load.keys()) or list(idle.keys())
hdr = f"  {'endpoint':<20}{'idle p50':>9}{'p95':>7}{'p99':>7}{'max':>8}   {'load p50':>9}{'p95':>7}{'p99':>7}{'max':>8}{'err':>5}"
print(hdr)
for ep in eps:
    i = idle.get(ep, {}); l = load.get(ep, {})
    print(f"  {ep:<20}{i.get('p50_ms','-'):>9}{i.get('p95_ms','-'):>7}{i.get('p99_ms','-'):>7}{i.get('max_ms','-'):>8}   "
          f"{l.get('p50_ms','-'):>9}{l.get('p95_ms','-'):>7}{l.get('p99_ms','-'):>7}{l.get('max_ms','-'):>8}{l.get('errors','-'):>5}")

# ---- fork/exec census ----
rule("Fork/exec rate (execve, server started under strace)")
counts = {}
for ln in read("execve_counts.txt").splitlines():
    try:
        k, v = ln.split(); counts[k] = int(v)
    except ValueError:
        pass
for phase in ("idle", "load"):
    if phase in counts:
        print(f"  {phase:<6} {counts[phase]:>7} execve in {STRACE_S:.0f}s = {counts[phase]/STRACE_S:.2f}/s")
# per-binary breakdown (whole strace file)
cat = collections.Counter()
try:
    for ln in open(os.path.join(OUT, "strace_C.txt"), errors="replace"):
        m = re.search(r'execve\("([^"]+)"', ln)
        if m: cat[os.path.basename(m.group(1))] += 1
except Exception:
    pass
if cat:
    print("  by program (whole census):")
    for prog, c in cat.most_common(10):
        print(f"    {prog:<16} {c:>7}")
print()
