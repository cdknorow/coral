# The shared Inbox component

Coral owns it; Coral Hub mounts the same files at its Coral pin (design: the #5750 note, built in #5754). This page is the contract between the two.

## Files

| File | What it is |
|---|---|
| `static/inbox_panel.js` | The component: `mountInbox(host, adapter)`, `INBOX_CONTRACT`, `clearInboxDrafts()`, `safePath()`. Imports only `./safe_markdown.js`. |
| `static/safe_markdown.js` | The ES wrapper of the renderer: `renderSafe(text, opts)` and `makeLinkPolicy(lookups)`. No imports. |
| `static/vendor/safe-markdown.js` | The renderer itself: Coral Hub's `markdown.js`, **byte for byte** (a classic script that defines `window.AccountMarkdown`). Its sha256 is pinned in `tests/frontend/vendor_manifest.mjs`; the hub pins the same one. A change goes through Security on both sides. |
| `static/css/components.css` | The rules (`.ibx*`, `.md*`) |
| `static/inbox_coral.js` | Coral's adapter. Not shared: the hub writes its own. |
| `tests/frontend/fixtures/markdown-corpus.mjs` | The shared hostile-input corpus (also pinned). Both repos run it against the renderer in a real browser (`hostile_markdown_browser.test.js` is Coral's). |

## What the hub's page does

```html
<script src="<static base>/vendor/safe-markdown.js" integrity="sha384-..." crossorigin="anonymous"></script>
<script type="module">
  import { mountInbox } from '<static base>/inbox_panel.js';
  const inbox = mountInbox(document.getElementById('inbox-tasks'), adapter);   // { refresh, savedExpand, destroy }
</script>
```

(The hub's own page puts the second script in a file, not inline: the dashboard's CSP is `script-src 'self'`.)

**Integrity.** Put `integrity="sha384-<base64>"` on both `<script>` tags, computed from the file at the pin: `openssl dgst -sha384 -binary <file> | openssl base64 -A`. A changed file then does not run silently. The attribute covers the file it is on: `inbox_panel.js` imports `safe_markdown.js`, which an `integrity` on the entry does not cover, so pin that file's hash in the hub's own test of its pin, as the renderer's is. Coral's own page needs none: the files are the same build.

## The adapter (contract 1)

A plain object of functions, never evaluated text. `mountInbox` refuses one that lacks a required function, that is not an object, or whose `contract` is set to a different version.

| Function | Answers |
|---|---|
| `listTasks()` | `[{ id, project, board, title, priority, status, created_by, created_at, updated_at }]`: the open tasks assigned to the Operator |
| `getTask(project, id)` | `{ body, created_by, status, ... }`: the full task |
| `replyToTask({ project, taskId, content })` | `POST /api/board/{project}/tasks/{id}/reply` with `{content}` and nothing else; `{ ok, notified: [names], warnings: [codes] }`; a refusal is a thrown `Error` whose message is the server's reason |
| `canReply()` | `{ ok: true }` or `{ ok: false, reason }` (a Coral Hub viewer at the View tier) |
| `readSavedMessage(item)` | `GET /api/saved/{id}/message`: `{ text, partial, reason }` with `reason` one of `''`, `gone`, `not_reachable`, `too_long` (optional: without it the saved control is not drawn) |
| `contract` (optional) | the `INBOX_CONTRACT` the adapter was written for |
| `linkPolicy(url)` (optional) | the host's link callback (see below) |
| `openTask(task)`, `taskHref(task)` (optional) | "Open the task": a button that calls `openTask`, or a link when `taskHref` returns a same-origin path (`safePath`: "/x", never "//host", a backslash, a blank or a control character; anything else makes no link) |
| `now()` (optional) | the clock, for ages |

`INBOX_CONTRACT` is 1. A breaking change to the adapter or to `mountInbox` bumps it and is a line in the release notes; the hub adapts when it takes the pin.

`mountInbox(host, adapter, { load: false })` does not read the tasks at once (the host calls `refresh()` when the list is shown). `savedExpand(item)` returns `{ root, button, region }`: the "Read the whole message" control for one saved item; the host puts `root` in its item. `destroy()` removes the panel and drops the drafts.

## Rules the component keeps (Security's conditions on #5750)

- Nothing builds HTML from machine text. Titles, names, counts, errors and reasons are set with `textContent`; the task and message text go through `renderSafe` (the DOM-built renderer: a closed list of tags, no image, links `http`, `https` and `mailto` with the host shown, hidden characters removed, bounded work). `inbox_no_html_census.test.js` is an AST scan of `inbox_panel.js` and `safe_markdown.js` for `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `DOMParser`, `createContextualFragment`, `on*`, `style` and `srcdoc` attributes, `href`/`src` set from anything but `safePath(...)`, `eval`, `Function`, dynamic `import()` and any import from outside `./name.js`.
- **Link policy.** Coral's `makeLinkPolicy` owns only fixed same-origin shapes after a lookup that the id exists on this server (an artifact: `coral://artifacts/<64 hex>` as `/api/artifacts/<hex>`, only when the host's `artifactExists` says `true`); it builds no path from text. Everything else is `undefined`, which leaves the url to the renderer's default policy or its refusal. A host that writes its own policy must do the same; a policy that throws or answers anything else is a refusal. Tested per refusal in `safe_markdown_policy.test.mjs` and in the corpus.
- **Drafts.** A reply draft is kept in memory only (never `localStorage` or `sessionStorage`) and dropped by `clearInboxDrafts()`, which empties the reply boxes that are open too. Coral calls it from `onAccountForgotten` (sign-out and a change of account); a host calls it at its own sign-out.
- **Limits.** The reply box takes 64 K characters. The server's own cap for a post is 256 KiB and its refusal is shown as it words it. The write budget (60 burst, 240 a minute) is for agent tokens only: the Operator and Coral Hub callers are not limited by it.
- **Hosts.** The module uses no `eval`, no `new Function`, and no `import()` of a URL from data. An adapter's `open` targets are used only if they are same-origin paths.

## Routes (all in the tunnel table)

`GET /api/board/tasks` (the list), `GET /api/board/{project}/tasks/{id}` (the task), `POST /api/board/{project}/tasks/{id}/reply` (Interact, #5463), and the saved message: `GET /api/saved/{id}/message` (Backend, #5753; owner-only through the hub together with `GET /api/saved`).
