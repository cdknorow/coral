#!/usr/bin/python3
"""A stand-in agent CLI for tests/release/smoke_macos.sh (set as cli_path_claude).

It does what Claude Code does for a multi-select question, and nothing else: it fires the Coral hooks named in
its --settings file (PreToolUse, PermissionRequest for AskUserQuestion), draws the question on its terminal
(five options with boxes, an unnumbered Submit row), toggles a box on a digit, moves with Down/Up (both arrow
encodings), takes Submit with Enter, fires PostToolUse, writes the boxes that were ticked to standin-answer.txt
in its working folder, and then idles. Its terminal closing ends it, as it ends a real CLI. It never talks to the network and is not an agent.
"""
import json
import os
import re
import subprocess
import sys
import termios
import tty

if len(sys.argv) > 1 and sys.argv[1] == "auth":  # Coral's sign-in check after a launch (#5838): answered like the CLI does, never taken for a launch
    print('{"loggedIn": true}')
    sys.exit(0)

LABELS = ["Feels like a slideshow", "Doesn't show the game", "Tone/story is off", "Looks cheap/amateur", "Type something"]
QUESTION = "What's not working about the trailer? (Pick everything that applies.)"
checked = [False] * 5
focus = 1


def hooks(event, extra=None):
    """Run the hook commands Coral put in --settings for this event, the CLI's way: the event's JSON on stdin."""
    try:
        a = sys.argv
        cfg = json.load(open(a[a.index("--settings") + 1]))
        sid = a[a.index("--session-id") + 1]
        env = dict(os.environ)
        env.update(cfg.get("env", {}))
        tp = os.path.join(os.environ.get("HOME", "/tmp"), ".claude", "projects", "-standin", sid + ".jsonl")
        os.makedirs(os.path.dirname(tp), exist_ok=True)
        open(tp, "a").close()
        payload = {"cwd": os.getcwd(), "hook_event_name": event, "permission_mode": "default", "session_id": sid,
                   "transcript_path": tp, "tool_name": "AskUserQuestion",
                   "tool_input": {"questions": [{"header": "Trailer", "multiSelect": True, "question": QUESTION,
                                                 "options": [{"label": l, "description": ""} for l in LABELS[:4]]}]}}
        payload.update(extra or {})
        for grp in cfg.get("hooks", {}).get(event, []):
            m = grp.get("matcher")
            if m and "AskUserQuestion" not in m and m != "*":
                continue
            for h in grp.get("hooks", []):
                subprocess.run(h["command"], shell=True, input=json.dumps(payload).encode(), env=env, timeout=20, capture_output=True)
    except Exception as e:  # a hook that cannot run is the smoke's finding, not a crash of the stand-in
        open("standin-hook-error.txt", "a").write(repr(e) + "\n")


def draw():
    out = "\x1b[2J\x1b[H ← ☐ Trailer  ✔ Submit →\n\n" + QUESTION + "\n\n"
    for i, l in enumerate(LABELS):
        out += "%s %d. [%s] %s\n" % ("❯" if focus == i + 1 else " ", i + 1, "✔" if checked[i] else " ", l)
    out += "%s    Submit\n" % ("❯" if focus == 6 else " ")
    out += "────────────────────────────────────────\n  Chat about this\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n"
    sys.stdout.write(out.replace("\n", "\r\n"))  # raw mode: no newline translation
    sys.stdout.flush()


fd = sys.stdin.fileno()
old = termios.tcgetattr(fd)
tty.setraw(fd)
try:
    hooks("PreToolUse")
    hooks("PermissionRequest")
    draw()
    while True:
        buf = os.read(fd, 64)
        done = False
        for tok in re.findall(rb"\x1b\[[AB]|\x1bO[AB]|\r|\n|.", buf, re.S):
            if len(tok) == 1 and tok in b"12345":
                checked[tok[0] - 49] = not checked[tok[0] - 49]
            elif tok in (b"\x1b[B", b"\x1bOB") and focus < 6:
                focus += 1
            elif tok in (b"\x1b[A", b"\x1bOA") and focus > 1:
                focus -= 1
            elif tok in (b"\r", b"\n") and focus == 6:
                done = True
        if done:
            break
        draw()
finally:
    termios.tcsetattr(fd, termios.TCSADRAIN, old)
hooks("PostToolUse", {"tool_response": {"answers": {}}})
open("standin-answer.txt", "w").write(json.dumps([LABELS[i] for i in range(5) if checked[i]]))
sys.stdout.write("\x1b[2J\x1b[H● Working on it…\r\n\r\n❯ \r\n")
sys.stdout.flush()
# then wait for input like a real CLI does: when the terminal goes (EOF or an I/O error on a closed pty) it exits, instead of lingering with nothing to read
try:
    tty.setraw(fd)
    while os.read(fd, 1024):
        pass
except OSError:
    pass
