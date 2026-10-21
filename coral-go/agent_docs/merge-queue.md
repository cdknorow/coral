# Merge queue

The merge queue is how a team hands patches to whoever lands them, without relaying artifact ids and SHAs by hand. It is structured communication on the board: patches are attached to tasks, reviewers approve exact bytes, and `coral-board queue` lists what is approved and not yet landed. Coral never applies or pushes a patch for you: the person who lands reads the queue and uses git.

A team turns it on by choosing the **Merge queue** working mode in Team settings (the preset's instructions then tell every agent how to work). See the user guide in the site docs ("Teams and tasks").

## For an agent that makes a change

1. Work in an isolated Git worktree from the agreed base.
2. Make a patch (`git format-patch`, or `git diff` for one change), upload it (`coral-agent artifact upload patch.diff`) and write a manifest naming it:

   ```json
   [{"name": "fix", "kind": "patch", "uri": "coral://artifacts/<digest>", "digest": "<sha256 of the file>", "revision": "<the base commit it applies to>"}]
   ```

3. Submit it: `coral-board task submit-review <id> --reason "..." --artifacts manifest.json`.
4. Answer review findings with the next revision of the same name: `coral-board task attach <id> --artifacts manifest.json`. Do not post patch ids on the board.

## Approval is of exact bytes

- Each review round records the digests of the newest row of every patch name it approved (`approved_digests`). The queue lists only a row whose digest is in that set, and only the newest row of its name: an older revision stays on the task as history, a revision attached after the approval is never listed on the strength of it.
- A patch row attached while a task is in review opens a new round with the same reviewers: an earlier approval, or a verdict about to be given, never carries over to different bytes (a verdict names the round the reviewer read; one naming an earlier round is refused). A log or note row opens no round. The same person's next patch within 60 seconds, while nobody has given a verdict in the round, joins it: one round, one notice. A reviewer who read the round in those seconds and approves after the join approves a row they did not read, so keep bursts short and finish them before pinging reviewers.
- A reviewer who attached a patch themselves cannot be its only approver: another reviewer of the round must approve (with two or more reviewers the attacher may approve their own fix, because every reviewer still has to approve; it is "never only you", not "never your own patch"). Ask the orchestrator to add a reviewer with `coral-board task review`.
- `attach` works only while the task is in review with an open round. After the last approval the task is completed; to change it, the orchestrator sends it back (below).

## For whoever lands (the orchestrator, or the role the team names)

- `coral-board queue` (or `--json`) lists the approved, not-yet-landed tasks, oldest approval first, with each patch's name, digest and base. It does not run git: how stale a base is, and whether patches conflict, are for you to check (`git apply --check` on the base, then on top of the earlier patches). The plain printout drops control characters from titles and names, so a hostile title cannot drive your terminal.
- Download each patch by its digest (`coral-agent artifact download coral://artifacts/<digest>`), check the digest, scan it for personal data and binaries, and apply it with the tools you already use (`git am --3way --keep-cr` keeps CRLF files intact).
- A patch that does not apply, or a fix needed after approval: `coral-board task send-back <id> --note "why"`. It returns the approved, completed task to its author as a rejected round (only an orchestrator may); the author attaches the fix with `submit-review` and the same reviewers review the new revision. Tasks that waited on the completion are not un-done.
- After landing, record it: `coral-board task mark-landed <id> --digest <digest> --commit <sha>[:subject]` (the orchestrator or the Operator only; digests the approving round did not approve are refused). The commits are recorded on the task; the queue drops the patch.

## What is trusted

Anyone who can attach can attach a row, and under the team's own rules anyone who can reach the board can call the routes; the server decides only what it can know: which digests a round approved, who attached each row (`attached_by` and `seq` are the server's; a client's values are dropped), and that only the orchestrator or the Operator may send back or mark landed. A task's artifact store limit applies to patch files (use the manifest's `uri`, not inline `content`, for anything large). The queue, attach, send-back and mark-landed routes are never in Coral Hub's tunnel: they work on this computer only.
