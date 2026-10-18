# insnotes.py RELEASE_NOTES.md notes.md VER: insert the notes section right after "# Release Notes"
import sys
p, n, ver = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(p).read(); notes = open(n).read().strip() + "\n"
assert notes.startswith("## v" + ver + " "), "notes must start with ## v" + ver
assert ("## v" + ver + " ") not in s, "section already present"
head = "# Release Notes\n"
assert s.startswith(head), "unexpected first line"
rest = s[len(head):].lstrip("\n")
open(p, "w").write(head + "\n" + notes + "\n" + rest)
