# Saved for later

The Operator can bookmark a message of an agent's chat or of a board with **Save for later**, and click the bookmark in the **Saved** list to go back to that point of the conversation (#4510). The bookmarks are the Operator's own notes: an agent's token is refused on every route below (`403 operator_only`), and nothing an agent does adds one.

## In the dashboard

- A bookmark button (an inline SVG, filled and in the accent colour when saved) one slot to the left of **Copy** on each message of an agent's Chat view, in History too, and in each board message's header. It shows on hover or keyboard focus, always while saved, and always on touch, where it and Copy are 44 px targets. Its name is "Save for later" or "Remove from saved" (`aria-pressed` says which). Saving shows a toast "Saved." with **View saved**; removing shows "Removed from saved." with **Undo**.
- **Saved** is a tab of the right sidebar, beside Files, Tasks and the others (a bookmark with the number open), so the list stays in view while the main view shows something else. The **Saved** button in the top bar (a bookmark with the same number; on a phone at large text, where the bar has no room, it is an entry of the settings menu with its number) opens the sidebar on that tab, or focuses it if it is open, and moves the focus into the list; from History, a board or the home screen it first selects a running agent, because the sidebar belongs to the agent's page. On a phone the tab is in the side panel overlay, which gives way to the conversation when an item is opened. The tab has an **Open** and a **Done** list, newest saved first. An item shows the agent or board and a chip (**Agent**, **Board**, **Ended agent** or **Gone**), a two-line snippet, when it was said and when it was saved, and **Open chat** / **Open board** / **Open in Chats**, **Done** (**Reopen** in the Done list) and **Remove**. Results are announced politely ("Marked done", "Removed from saved").
- Clicking **Open** goes to the message and leaves the Saved tab where it is: an agent that is running opens in its Chat view and pages up until the message is found; an agent that has ended opens in Chats (History) at the message; a board opens and loads older messages as the board's search does (History and boards are pages of their own, without the sidebar, so the tab cannot stay beside them: the bar's **Back to Saved** and the top bar's button bring it back). The message is outlined, tagged **Saved** and takes the focus, and a slim bar above the conversation says so with **Back to Saved**.
- When the tab is shown each item is checked: a message the transcript no longer has, a conversation whose transcript is gone, or a board message that was deleted is marked **Gone**, says why, keeps its snippet and can only be removed.
- Not in this version: agents and boards on a registered remote server (hub mode) have no button, and the Saved list is this server's own.

## The message key

A chat message is found again by a key the server adds to each message it returns, in `GET /api/sessions/live/{name}/chat` and `GET /api/sessions/history/{id}`: `key`, the first 20 hex characters of an HMAC-SHA256 of the message's type, timestamp, text (`content` or `text`) and, for a tool call or result, the tool's name and id, keyed with a secret that belongs to this installation (`<data dir>/.message_key_secret`, 32 random bytes in hex, mode 0600, made at first use and kept: a key must survive a restart). The secret is what keeps the key from being a way to test guesses: through Coral Hub at View the text of a reply is redacted but its key is not, and `find_key` answers whether a message exists, so a plain hash of the text would let a viewer check a guess for a redacted password offline. If the file cannot be read or made, a secret lasting until the next restart is used and a warning is logged. The same message has the same key after a reload, a poll, another page size, and in History, so a bookmark survives them. Two messages that say the same thing at the same second share a key (the jump finds the first). A board message is its own id.

`GET .../chat?session_id=SID&find_key=KEY` and `GET /api/sessions/history/{id}?find_key=KEY` answer `{"found": bool, "index": n, "total": n}`: whether the transcript still has the message, and its index (the History view opens at `message_index`).

## API

All four routes answer only a person: the dashboard on this computer, the signed command line, or Coral Hub (the tunnel allows the list at **View** and the changes at **Interact**: the bookmarks change no work). An agent's token, a token-less script and a forged hub stamp get `403 operator_only`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/saved?state=open\|done\|all&kind=agent\|board&conv=<session id or board>&limit=N` | `{"items": [...], "open": n, "done": n}`, newest saved first. `kind` and `conv` narrow it to one conversation; `limit` keeps the newest N (`limit=0`: only the counts, which the top bar's number asks for). |
| POST | `/api/saved` | Save `{kind, ref, key, agent_type, title, snippet, said_at}`. `ref` is the session id or the board; `key` the message key (20 hex characters for an agent, the message id for a board). Saving a saved message again answers the bookmark that exists. At most 2,000 are kept (`409`). |
| PATCH | `/api/saved/{id}` | `{"done": true\|false}`. |
| DELETE | `/api/saved/{id}` | Remove. |

An item is `{id, kind, ref, key, agent_type, title, snippet, said_at, saved_at, done_at}`.

**The snippet is redacted when it is saved**, not only when it leaves the computer: Coral's secret rules replace a token in it with `[redacted token]`, it is cut to one line of at most 200 bytes where no token can be split, and text that cannot be checked is not kept. The bookmarks are in `sessions.db` (table `saved_messages`) and are kept until the Operator removes them.
