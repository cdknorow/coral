r"""Regenerates the *.expected.txt files from the *.raw captures with pyte, an independent terminal emulator.

The expected text is what pyte shows for the same bytes, NOT what Coral's own renderer (internal/vtscreen) shows: the test
TestPTYRenderMatchesAnIndependentEmulatorOnRealAgentScreens compares the two, row by row. pyte is pinned in requirements.txt.

    python -m venv .venv && .venv/bin/pip install -r requirements.txt     (.venv\Scripts on Windows)
    .venv/bin/python gen_expected.py

A capture <agent>_<state>_<cols>x<rows>.raw is the raw output of the agent's terminal (Coral's replay ring), user name redacted.
"""
import glob, os, re
import pyte

here = os.path.dirname(os.path.abspath(__file__))
for path in sorted(glob.glob(os.path.join(here, "*.raw"))):
    cols, rows = map(int, re.search(r"_(\d+)x(\d+)\.raw$", path).groups())
    screen = pyte.Screen(cols, rows)
    pyte.Stream(screen).feed(open(path, "rb").read().decode("utf-8", errors="replace"))
    lines = [row.rstrip() for row in screen.display]
    with open(path[:-4] + ".expected.txt", "w", encoding="utf-8", newline="\n") as f:
        f.write("\n".join(lines) + "\n")
