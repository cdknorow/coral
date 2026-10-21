#!/usr/bin/env python3
"""Input-fidelity cases for tests/stress/run_input_fidelity.sh.

Drives a mock-agent in raw-input mode (MOCK_AGENT_RAW_DIR: every byte its terminal delivers is
appended to <raw-dir>/<session-id>.raw) through Coral's input paths and compares what arrived
with what was sent. The same cases run on every backend (tmux, pty), and `compare` lists the
cases whose result differs between two runs.

  input_fidelity.py run --base URL --data-dir DIR --raw-dir DIR --work DIR --env-dump DIR \
      --cookie COOKIE --backend NAME --out results.json
  input_fidelity.py compare a.json b.json

Reading the bytes: ESC[200~ ... ESC[201~ is a bracketed paste (a CR or LF inside it is text);
a CR outside a paste submits the line. "submissions" are the submitted lines, paste markers
removed and a CR inside a paste read as a line break, so tmux's LF->CR paste translation and a
PTY's raw LF compare equal.
"""
import argparse
import concurrent.futures
import json
import os
import platform
import sys
import time
import urllib.error
import urllib.request

PASTE_ON, PASTE_OFF = b"\x1b[200~", b"\x1b[201~"


# ── HTTP ─────────────────────────────────────────────────────────────────────

class Coral:
    def __init__(self, base, cookie, api_key):
        self.base, self.cookie, self.api_key = base, cookie, api_key

    def call(self, method, path, body=None, human=True, timeout=180):
        """One request: as the dashboard (human, the session cookie) or as automation (the API
        key, which the automated-input gate handles). Returns (status, parsed JSON or text)."""
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if human:
            req.add_header("Origin", self.base)
            req.add_header("Sec-Fetch-Site", "same-origin")
            req.add_header("Cookie", self.cookie)
        else:
            req.add_header("Authorization", "Bearer " + self.api_key)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                status, raw = r.status, r.read()
        except urllib.error.HTTPError as e:
            status, raw = e.code, e.read()
        except Exception as e:  # a timeout or refused connection is a result too
            return 0, {"error": repr(e)}
        try:
            return status, json.loads(raw)
        except ValueError:
            return status, raw.decode("utf-8", "replace")


# ── Reading what arrived ─────────────────────────────────────────────────────

def parse(stream):
    """(submissions, pending, pastes): submitted lines, the unsubmitted tail, and how many
    bracketed pastes were seen."""
    subs, cur, i, inside, pastes = [], bytearray(), 0, False, 0
    while i < len(stream):
        if stream.startswith(PASTE_ON, i):
            inside, pastes, i = True, pastes + 1, i + len(PASTE_ON)
            continue
        if stream.startswith(PASTE_OFF, i):
            inside, i = False, i + len(PASTE_OFF)
            continue
        b = stream[i]
        if b == 0x0D and not inside:
            subs.append(bytes(cur))
            cur = bytearray()
        elif b == 0x0D and inside:
            cur.append(0x0A)  # tmux pastes LF as CR; both are a line break in the text
        else:
            cur.append(b)
        i += 1
    return subs, bytes(cur), pastes


class Agent:
    def __init__(self, coral, launch, raw_dir):
        self.coral = coral
        self.sid = launch.get("session_id", "")
        self.name = launch.get("name") or launch.get("session_name") or ("claude-" + self.sid)
        self.raw = os.path.join(raw_dir, self.sid + ".raw")

    def size(self):
        try:
            return os.path.getsize(self.raw)
        except OSError:
            return 0

    def read_from(self, off):
        try:
            with open(self.raw, "rb") as f:
                f.seek(off)
                return f.read()
        except OSError:
            return b""

    def wait(self, off, done, timeout=30, quiet=2.0):
        """Bytes after off once done(bytes) holds, or once nothing new arrived for `quiet`
        seconds after something did, or at the timeout."""
        end, last, last_change = time.time() + timeout, -1, time.time()
        while time.time() < end:
            got = self.read_from(off)
            if done(got):
                time.sleep(0.3)  # let a trailing Enter land
                return self.read_from(off)
            if len(got) != last:
                last, last_change = len(got), time.time()
            elif got and time.time() - last_change > quiet:
                return got
            time.sleep(0.2)
        return self.read_from(off)

    def target(self):
        return {"agent_type": "claude", "session_id": self.sid}


# ── Cases ────────────────────────────────────────────────────────────────────

SPECIALS = "quote' dq\" back\\slash $(whoami) `id` %PATH% ^caret & | < > !bang C:\\Program Files (x86)\\Coral\\"
UNICODE = "héllo wörld ü ß 日本語 中文 🎉 👍🏽 👨\u200d👩\u200d👧 ✓ —"


def text_cases():
    """(id, text) for the send paths."""
    ten_k = ("0123456789abcdef" * 640)[:10240]
    hundred_k = ("fidelity-" * 11378)[:102400]
    lines = "\n".join("line %03d of a multi-line prompt" % i for i in range(100))
    return [
        ("plain", "hello fidelity"),
        ("keyname-text-Enter", "Enter"),
        ("keyname-text-Escape", "Escape"),
        ("keyname-text-C-c", "C-c"),
        ("leading-dash", "- fix the tests --help"),
        ("multiline-3", "alpha\nbeta\ngamma"),
        ("multiline-crlf", "one\r\ntwo\r\nthree"),
        ("unicode", UNICODE),
        ("specials", SPECIALS),
        ("tab-inside", "col1\tcol2\tcol3"),
        ("10KB-single", ten_k),
        ("10KB-multiline", lines),
        ("100KB-single", hundred_k),
    ]


AUTOMATED_SUBSET = {"plain", "keyname-text-Enter", "multiline-3", "unicode", "specials", "10KB-multiline"}

KEY_CASES = [
    # (id, keys, the bytes an agent expects for them: a tmux key name means that key)
    ("key-Enter", ["Enter"], [b"\r"]),
    ("key-Escape", ["Escape"], [b"\x1b"]),
    ("key-Tab", ["Tab"], [b"\t"]),
    ("key-BTab", ["BTab"], [b"\x1b[Z"]),
    ("key-C-c", ["C-c"], [b"\x03"]),
    ("key-C-u", ["C-u"], [b"\x15"]),
    ("key-Up", ["Up"], [b"\x1b[A", b"\x1bOA"]),
    ("key-Down-Enter", ["Down", "Enter"], [b"\x1b[B\r", b"\x1bOB\r"]),
    ("key-BSpace", ["BSpace"], [b"\x7f", b"\x08"]),
    ("key-digit-1", ["1"], [b"1"]),
]


def expect_text(text):
    """The submission a sent text should become: line breaks as LF (CRLF and CR read as one)."""
    return text.replace("\r\n", "\n").replace("\r", "\n").encode()


def judge_send(got, text):
    subs, pending, pastes = parse(got)
    want = expect_text(text)
    if subs == [want] and not pending:
        return "PASS", ""
    if len(subs) > 1 and b"\n".join(subs) == want:
        return "FAIL", "submitted early: arrived as %d separate submissions (one per line)" % len(subs)
    if not subs and pending:
        return "FAIL", "typed but never submitted (no Enter outside a paste)"
    if subs and subs[-1] != want and want in b"".join(subs) + pending:
        return "FAIL", "the text arrived split or with extra bytes"
    return "FAIL", "the bytes differ from what was sent"


def short_hex(b, n=120):
    h = b[:n].hex()
    return h + ("...(+%d bytes)" % (len(b) - n) if len(b) > n else "")


def first_diff(got, want):
    for i, (x, y) in enumerate(zip(got, want)):
        if x != y:
            return i
    return min(len(got), len(want))


def run(args):
    api_key = open(os.path.join(args.data_dir, "api_key")).read().strip()
    coral = Coral(args.base, args.cookie, api_key)
    results = []

    def record(case_id, path, verdict, note, sent=b"", got=b"", extra=None):
        r = {"id": case_id, "path": path, "verdict": verdict, "note": note,
             "sent_len": len(sent), "got_len": len(got), "got_hex": short_hex(got, 400)}
        if got != sent and sent:
            r["first_diff_at"] = first_diff(got, sent)
        if extra:
            r.update(extra)
        results.append(r)
        print("[fidelity] %-5s %-28s %-32s %s" % (verdict, path, case_id, note), flush=True)

    def launch(display, prompt=None, board=None):
        d = os.path.join(args.work, display)
        os.makedirs(d, exist_ok=True)
        body = {"working_dir": d, "agent_type": "claude", "display_name": display}
        if prompt is not None:
            body["prompt"] = prompt
        if board:
            body.update({"board_name": board, "new_board": True})
        st, out = coral.call("POST", "/api/sessions/launch", body)
        if st != 200 or not isinstance(out, dict) or not out.get("session_id"):
            raise SystemExit("launch %s failed: %s %s" % (display, st, out))
        a = Agent(coral, out, args.raw_dir)
        # Ready when the mock has opened its raw log (raw mode on, bracketed paste announced).
        for _ in range(150):
            if os.path.exists(a.raw):
                break
            time.sleep(0.2)
        time.sleep(1.0)
        return a, out

    # ── 1. Launch prompt: handed to the agent CLI as an argument ──
    lp = "launch prompt: " + SPECIALS + " " + UNICODE
    agent, out = launch("fidelity-main", prompt=lp)
    handed = os.path.join(args.env_dump, agent.sid + ".handed")
    for _ in range(50):
        if os.path.exists(handed):
            break
        time.sleep(0.2)
    try:
        argv = open(handed, encoding="utf-8", errors="replace").read()
    except OSError:
        argv = ""
    if not argv:
        record("launch-prompt-arg", "launch prompt (argv)", "SKIP", "no .handed dump (MOCK_AGENT_ENV_DUMP)")
    elif lp in argv:
        record("launch-prompt-arg", "launch prompt (argv)", "PASS", "the prompt arrived in the agent's arguments intact")
    else:
        near = [l for l in argv.splitlines() if "launch prompt" in l]
        why = "the agent's arguments do not contain the prompt as sent"
        try:
            if lp.encode("utf-8").decode("cp1252", "replace")[:60] in argv or "Ã©" in (near[0] if near else ""):
                why = "non-ASCII arrived as mojibake: the UTF-8 prompt was read as Windows-1252"
        except Exception:
            pass
        record("launch-prompt-arg", "launch prompt (argv)", "FAIL", why,
               extra={"argv_line": (near[0] if near else argv[:600])})

    # ── 2. /send as a person (dashboard cookie: direct, no gate) and 3. as automation (gate) ──
    for human, path in ((True, "send (dashboard)"), (False, "send (automated, gate)")):
        for cid, text in text_cases():
            # The gate can hold each automated send while the agent is "in a turn" (a mock sends no
            # hook events, so up to its 2-minute staleness): a representative subset there.
            if not human and cid not in AUTOMATED_SUBSET:
                continue
            off = agent.size()
            want = expect_text(text)
            st, resp = coral.call("POST", "/api/sessions/live/%s/send" % agent.name,
                                  dict(agent.target(), command=text), human=human)
            got = agent.wait(off, lambda g: parse(g)[0][-1:] == [want], timeout=60 if len(text) > 50000 else 30)
            verdict, note = judge_send(got, text)
            if st == 202:
                note = ("(held by the gate, HTTP 202) " + note).strip()
            elif st != 200:
                # Refused or not confirmed: still say what reached the agent (a retype shows here).
                code = resp.get("code", "") if isinstance(resp, dict) else ""
                n = len(parse(got)[0])
                verdict, note = "FAIL", "HTTP %s %s; the agent got %d submission(s): %s" % (st, code, n, note)
            record(cid, path, verdict, note, sent=want + b"\r", got=got)

    # ── 4. /keys: tmux key names ──
    for human, path in ((True, "keys (dashboard)"), (False, "keys (automated, gate)")):
        for cid, keys, wants in KEY_CASES:
            if not human and cid not in ("key-Enter", "key-Escape", "key-Up"):
                continue  # held by the gate like automated sends: a subset
            off = agent.size()
            st, resp = coral.call("POST", "/api/sessions/live/%s/keys" % agent.name,
                                  dict(agent.target(), keys=keys), human=human)
            if st not in (200, 202):
                record(cid, path, "FAIL", "HTTP %s: %s" % (st, str(resp)[:200]))
                continue
            got = agent.wait(off, lambda g: any(g == w for w in wants), timeout=15, quiet=1.5)
            if any(got == w for w in wants):
                record(cid, path, "PASS", "", sent=wants[0], got=got)
            elif got == "".join(keys).encode():
                record(cid, path, "FAIL", "the key NAME was typed as text: %r" % got.decode(errors="replace"),
                       sent=wants[0], got=got)
            else:
                record(cid, path, "FAIL", "expected %r, got %r" % (wants[0], got[:80]), sent=wants[0], got=got)
            # Clear whatever a failed key left in the agent's input line before the next case.
            agent.coral.call("POST", "/api/sessions/live/%s/keys" % agent.name,
                             dict(agent.target(), keys=["\r"]), human=True)
            agent.wait(agent.size(), lambda g: False, timeout=1.0, quiet=0.5)

    # ── 5. Rapid fire: several senders at once, each text must arrive whole and on its own ──
    for human, path in ((True, "send x16 concurrent (dashboard)"),):
        texts = ["rapid-%02d-%s" % (i, "xyz" * (5 + i)) for i in range(16)]
        off = agent.size()
        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
            codes = list(pool.map(lambda t: coral.call("POST", "/api/sessions/live/%s/send" % agent.name,
                                                       dict(agent.target(), command=t), human=human)[0], texts))
        # A person's sends to one agent go one at a time: one that finds another in progress is
        # refused (409), never typed. So: every accepted send arrives whole as a submission of its
        # own, no refused one arrives, and nothing is interleaved.
        accepted = sorted(t.encode() for t, c in zip(texts, codes) if c == 200)
        refused = [t.encode() for t, c in zip(texts, codes) if c != 200]
        got = agent.wait(off, lambda g: sorted(parse(g)[0]) == accepted, timeout=60, quiet=4)
        subs, pending, _ = parse(got)
        mangled = [s for s in subs if s not in accepted]
        if sorted(subs) == accepted and not pending and not any(r in subs for r in refused):
            record("rapid-16", path, "PASS",
                   "%d accepted, each arrived whole as its own submission; %d refused (HTTP %s), none typed" % (
                       len(accepted), len(refused), sorted({c for c in codes if c != 200})), got=got)
        else:
            record("rapid-16", path, "FAIL",
                   "HTTP %s; %d accepted, %d arrived intact; mangled or unexpected submissions: %r" % (
                       sorted(set(codes)), len(accepted), len([s for s in subs if s in accepted]),
                       [m[:60] for m in mangled[:4]]),
                   got=got)

    # ── 6. Task notification (typed by the automated-input gate) ──
    title = "Fidelity task " + UNICODE + " " + SPECIALS
    off = agent.size()
    st, resp = coral.call("POST", "/api/sessions/live/%s/tasks" % agent.name,
                          {"title": title, "body": "", "session_id": agent.sid, "notify": True})
    notified = resp.get("notified", "") if isinstance(resp, dict) else ""
    if st != 200 or not notified:
        record("task-notify", "task notification", "FAIL", "HTTP %s: %s" % (st, str(resp)[:200]))
    else:
        got = agent.wait(off, lambda g: notified.encode() in parse(g)[0], timeout=180, quiet=170)
        verdict, note = judge_send(got, notified)
        record("task-notify", "task notification", verdict, note, sent=notified.encode() + b"\r", got=got)

    # ── 7. Board nudge: a message for the agent on its board ──
    bagent, bout = launch("fidelity-board", board="fidelity-board-%d" % int(time.time()))
    board = bout.get("board_id") or bout.get("board_name") or ""
    off = bagent.size()
    msg = "@fidelity-board please check " + UNICODE
    st, resp = coral.call("POST", "/api/board/%s/messages" % board,
                          {"subscriber_id": "Operator", "content": msg})
    got = bagent.wait(off, lambda g: len(parse(g)[0]) >= 1, timeout=180, quiet=170)
    subs, pending, pastes = parse(got)
    if st not in (200, 201):
        record("board-nudge", "board notifier nudge", "FAIL", "post HTTP %s: %s" % (st, str(resp)[:200]))
    elif len(subs) == 1 and not pending:
        record("board-nudge", "board notifier nudge", "PASS",
               "one submission: %r" % subs[0][:100].decode(errors="replace"), got=got)
    elif not subs and not pending:
        record("board-nudge", "board notifier nudge", "SKIP", "no nudge reached the agent within 180s", got=got)
    else:
        record("board-nudge", "board notifier nudge", "FAIL",
               "%d submissions, pending %r" % (len(subs), pending[:60]), got=got)

    # ── 8. A person's draft in the input box vs an automated notice (#3478) ──
    # The gate must not paste Coral's text onto a draft someone left in the box: the two would be
    # submitted together. The draft is typed through /keys as a person (single characters are the
    # same on both backends), then a task notification (an automated paste) is created.
    off = agent.size()
    clear_key = "C-u" if args.backend == "tmux" else "\x15"
    coral.call("POST", "/api/sessions/live/%s/keys" % agent.name, dict(agent.target(), keys=["x", "y", "z"]))
    agent.wait(off, lambda g: g.endswith(b"xyz"), timeout=10, quiet=1)
    title2 = "Draft check %d" % int(time.time())
    st, resp = coral.call("POST", "/api/sessions/live/%s/tasks" % agent.name,
                          {"title": title2, "body": "", "session_id": agent.sid, "notify": True})
    notice = resp.get("notified", "") if isinstance(resp, dict) else ""
    got = agent.wait(off, lambda g: len(parse(g)[0]) >= 1, timeout=45, quiet=40)
    subs = parse(got)[0]
    merged = [s for s in subs if s.startswith(b"xyz") and notice.encode()[:20] in s]
    if merged:
        record("draft-vs-notice", "gate draft check (#3478)", "FAIL",
               "the notice was pasted onto the draft and both were submitted as one: %r" % merged[0][:80], got=got)
    elif subs:
        record("draft-vs-notice", "gate draft check (#3478)", "FAIL",
               "something was submitted while the draft sat in the box: %r" % [s[:60] for s in subs], got=got)
    else:
        # Held: clear the draft as its owner would, and the notice must then arrive on its own.
        off2 = agent.size()
        coral.call("POST", "/api/sessions/live/%s/keys" % agent.name, dict(agent.target(), keys=[clear_key]))
        got2 = agent.wait(off2, lambda g: notice.encode() in parse(g)[0], timeout=180, quiet=170)
        if notice.encode() in parse(got2)[0]:
            record("draft-vs-notice", "gate draft check (#3478)", "PASS",
                   "held while the draft was in the box, delivered alone once it was cleared", got=got + got2)
        else:
            record("draft-vs-notice", "gate draft check (#3478)", "PASS",
                   "held while the draft was in the box (not delivered within 180 s of clearing it)", got=got + got2)

    for a in (agent, bagent):
        coral.call("POST", "/api/sessions/live/%s/kill" % a.name, a.target())

    doc = {"backend": args.backend, "os": platform.system(), "base": args.base, "results": results}
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(doc, f, indent=1, ensure_ascii=False)
    fails = sum(r["verdict"] == "FAIL" for r in results)
    print("[fidelity] backend=%s os=%s: %d PASS, %d FAIL, %d SKIP; results in %s" % (
        args.backend, doc["os"], sum(r["verdict"] == "PASS" for r in results), fails,
        sum(r["verdict"] == "SKIP" for r in results), args.out), flush=True)
    return 1 if fails else 0


def compare(a_path, b_path):
    a, b = (json.load(open(p, encoding="utf-8")) for p in (a_path, b_path))
    key = lambda r: (r["path"], r["id"])
    ra, rb = {key(r): r for r in a["results"]}, {key(r): r for r in b["results"]}
    la, lb = "%s/%s" % (a["os"], a["backend"]), "%s/%s" % (b["os"], b["backend"])
    diffs = 0
    for k in sorted(set(ra) | set(rb)):
        x, y = ra.get(k, {}), rb.get(k, {})
        if x.get("verdict") != y.get("verdict") or x.get("got_hex") != y.get("got_hex"):
            diffs += 1
            print("DIFF %-32s %-28s %s=%s %s | %s=%s %s" % (k[0], k[1], la, x.get("verdict", "-"), x.get("note", ""),
                                                             lb, y.get("verdict", "-"), y.get("note", "")))
    print("%d case(s) differ between %s and %s" % (diffs, la, lb))
    return 1 if diffs else 0


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run")
    for flag in ("--base", "--data-dir", "--raw-dir", "--work", "--env-dump", "--cookie", "--backend", "--out"):
        r.add_argument(flag, required=True)
    c = sub.add_parser("compare")
    c.add_argument("a")
    c.add_argument("b")
    args = p.parse_args()
    if args.cmd == "run":
        sys.exit(run(args))
    sys.exit(compare(args.a, args.b))


if __name__ == "__main__":
    main()
