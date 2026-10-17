# What Coral stores on this computer

Coral keeps its data in one folder, `<data dir>` (`~/.coral` unless `CORAL_DATA_DIR` says otherwise). The files in the table are readable only by you (file mode 0600, in a 0700 folder). This page says what is kept, how long, whether it is cleaned of secrets, and where to change it. Nothing here is sent to anyone: the last section says what reaches Coral Hub.

<!-- TODO(#4381): the launch temp file wording below follows Lead's #4381 (temp files under <data dir>/tmp, swept at startup); check it against the commit as landed. -->

| What | Kept | Cleaned of secrets? | To change it |
|---|---|---|---|
| **Terminal logs** (`<data dir>/logs/<agent>_coral_<id>.log`: what an agent's terminal showed; search reads them) | As long as the agent exists. **Killing an agent deletes its log on the tmux terminal backend** (Sleep and Archive keep it; the pty backend leaves it). | Raw on disk. Redacted when Coral serves them to the dashboard or Coral Hub. | Delete a log's file. Kill an agent to remove its log (tmux). |
| **Board messages and tasks** (`messageboard.db`) | Until you delete the message or the board. Coral never ages them out. | Redacted where Coral's secret rules apply (task text, chat views); not everywhere. | Delete a message (its ×) or the board. |
| **Sessions, search index, usage** (`sessions.db`, which also holds the search index) | Kept. Events, call metrics and token rollups have their own retention. | Where Coral's secret rules apply; not everywhere. | No single setting; see [settings-system.md](settings-system.md). |
| **Uploads** (`<data dir>/uploads/`: images you attach to a chat) | 30 days or 1 GB, whichever comes first. | No. | `uploads_retention_days` (1 to 365, default 30) and `uploads_max_mb` (50 to 102400, default 1024), see [settings-system.md](settings-system.md#upload-an-image). Delete an upload by its API. |
| **Artifacts** (`<data dir>/artifacts/objects/`: files and reports agents publish) | No age limit by default. A store cap you can set. | Images lose their metadata; other bytes are stored exactly as uploaded and served exactly (artifact bytes are not redacted). | Settings: **Artifact store limit (GiB)** and **Automatic cleanup (days)** (0 is off), and the Operator's **Delete**. See [artifacts.md](artifacts.md#freeing-space-the-operator). |
| **Launch files** (the prompt, instructions and settings Coral writes to start an agent) | In `<data dir>/tmp`, removed when the agent ends. Ones left by a crash are swept at the next start. | No: a prompt file holds the whole launch prompt. | Nothing to set. |
| **Input debug log** (`input-debug.log`: what Coral typed into agents' terminals) | Off by default. When on: at most 5 MiB plus one older copy. | **No.** It holds typed text, which may include secrets. | Settings: **Log terminal input (debug)**, then **Delete the log** once it is off. See [troubleshooting.md](troubleshooting.md#seeing-exactly-what-coral-types-into-an-agents-terminal). |
| **Coral's own logs** (`coral.log`, `tray.log`, `app.log`) | Rotated at 20 MB, five older copies each. | `coral.log`: yes (secret rules are logged at startup). | Delete the files. |

## Things to know

- **A Kill is not a Sleep.** Killing an agent ends it and, on the tmux terminal backend, deletes its terminal log, which is what search reads. Sleep and Archive keep the log. Every Kill confirmation says so.
- **Terminal logs are the biggest and the most revealing.** They record what the terminal showed, secrets included, and are redacted only when served. Treat `<data dir>/logs` like the agents' own terminals.
- **Board history is a product choice.** Coral keeps your board messages and tasks so a team has a memory; nothing removes them unless you do.
- **Artifacts are served byte for byte.** Coral cannot hide a token in an artifact: redact a log or a screenshot before you upload it (see [artifacts.md](artifacts.md#limits-images-and-deleting)).
- **The agent CLIs (Claude, Codex and the others) keep their own transcripts** in their own folders. Coral does not manage those.
- **Other files:** per-session board state (`board_state_*.json`, no message text), the knowledge notes an agent distills (`agent_knowledge/`), and a few small caches. They are kept until you delete them.

## What reaches Coral Hub

Only what Coral's redacting connector sends: View-tier replies and the redacted terminal and agent-list streams. A raw file on this page is never sent to Coral Hub. Two things to know: **artifact downloads are served byte for byte** (they are exempt from redaction on purpose, so an artifact you share can be opened and is shown exactly), and **uploads are not served back through Coral Hub**. Whatever you see through Coral Hub that is not an artifact has been redacted on this computer first. See [hub.md](hub.md).
