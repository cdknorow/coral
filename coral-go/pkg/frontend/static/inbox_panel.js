/* The shared Inbox component (#5750 design, #5754 phase 1): "Tasks for you" (a list; each task opens inline to its full text and a reply box) and "Read the whole message" for a Saved for later item.
 * Coral owns it; Coral Hub mounts the same file at its Coral pin (see INBOX.md next to this folder's static/ for the contract, the adapter and the integrity attribute the hub puts on its script tag).
 *
 * It knows nothing of Coral's pages. Everything it needs from its host comes through an ADAPTER: a plain object of functions, never evaluated text:
 *   listTasks()                              -> [{ id, project, board, title, priority, status, created_by, updated_at, created_at, revision }]   the tasks assigned to the Operator, open ones
 *   getTask(project, id)                     -> { body, created_by, status, ... }                                                        the full task
 *   replyToTask({ project, taskId, content })-> { ok, notified: [names], warnings: [codes] }                                             POST /api/board/{project}/tasks/{id}/reply, {content} only
 *   canReply()                               -> { ok: true } | { ok: false, reason }                                                     (e.g. a Coral Hub viewer at the View tier)
 *   readSavedMessage(item)                   -> { text, partial, reason }   reason: '' | 'gone' | 'not_reachable' | 'too_long'          GET /api/saved/{id}/message
 *   optional: contract (the INBOX_CONTRACT it was written for: a different one is refused), linkPolicy(url), prepareLinks(text) (async: the lookup for that text, returns the linkPolicy to use for it), openTask(task), taskHref(task) (a same-origin path), now()
 * Rules it keeps (Security's conditions on #5750): nothing here builds HTML from machine text: every title, name, count, error and reason is set with textContent, and the task and message text goes
 * through safe_markdown.js (the shared DOM-built renderer); it imports only same-origin modules and uses no eval or dynamic import; a path an adapter returns is used only if it is a same-origin path;
 * a reply draft is kept in memory only (never localStorage or sessionStorage) and dropped by clearInboxDrafts() (sign-out). inbox_no_html_census.test.js holds the first two to account. */
import { renderSafe } from './safe_markdown.js';

/** The contract version of the adapter and of mountInbox: a breaking change bumps it, and is a line in the release notes (the hub adapts when it takes the new pin). */
export const INBOX_CONTRACT = 1;

const REPLY_MAX = 65536; // characters the box takes; the server's own cap is 256 KiB for a post, and its refusal is shown as it words it

// ── drafts: in memory only ─────────────────────────────────────────────────────────────────────────────
const _drafts = new Map();
const _boxes = new Set(); // the reply boxes on screen, so a purge empties what is typed in them too
/** Drop every unsent reply (sign-out, a change of account): the kept drafts and what is typed in an open box. */
export function clearInboxDrafts() {
    _drafts.clear();
    for (const box of _boxes) { if (!box.isConnected) { _boxes.delete(box); continue; } box.value = ''; box.dispatchEvent(new Event('input')); }
}
const draftKey = (project, id) => `${project}#${id}`;

// ── small helpers (DOM only: text through textContent) ─────────────────────────────────────────────────
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = String(text); return e; };
let _seq = 0;
const uid = prefix => `${prefix}-${++_seq}`;

/** A path an adapter hands back is a link target only when it is a same-origin absolute path: "/x", never "//host", a backslash, a blank or a control character. */
export function safePath(p) {
    return typeof p === 'string' && p.length > 0 && p.length <= 2048 && p.charAt(0) === '/' && p.charAt(1) !== '/' && !/[\\\s\u0000-\u001F\u007F]/.test(p) ? p : '';
}

function ago(iso, now) {
    const t = Date.parse(iso);
    if (!t) return '';
    const s = Math.max(0, ((now ? now() : Date.now()) - t) / 1000);
    if (s < 60) return 'just now';
    const unit = (n, w) => `${n} ${w}${n === 1 ? '' : 's'} ago`;
    if (s < 3600) return unit(Math.floor(s / 60), 'minute');
    if (s < 86400) return unit(Math.floor(s / 3600), 'hour');
    return unit(Math.floor(s / 86400), 'day');
}

function checkAdapter(a) {
    if (!a || typeof a !== 'object') throw new TypeError('mountInbox: the adapter is a plain object of functions');
    for (const f of ['listTasks', 'getTask', 'replyToTask', 'canReply']) if (typeof a[f] !== 'function') throw new TypeError(`mountInbox: the adapter has no ${f}()`);
    if (a.contract !== undefined && a.contract !== INBOX_CONTRACT) throw new TypeError(`mountInbox: the adapter was written for contract ${a.contract}, this is contract ${INBOX_CONTRACT}`);
    return a;
}

/** The text as DOM. When the adapter has prepareLinks(text) (a lookup that must answer before a link is owned), it is awaited for THIS text and the policy it returns is the one used; if it throws, the
 *  adapter's own linkPolicy (or none) is used, so a failed lookup leaves the links as text. */
async function renderBody(text, adapter) {
    let policy = adapter.linkPolicy;
    if (typeof adapter.prepareLinks === 'function') {
        try { const p = await adapter.prepareLinks(text); if (typeof p === 'function') policy = p; } catch { /* the links stay as the renderer's default has them */ }
    }
    const opts = typeof policy === 'function' ? { linkPolicy: policy } : undefined;
    return renderSafe(text, opts).node;
}

/** The panel the message body sits in, apart from the list and from the actions around it (#5882). */
function inset(node) { const p = el('div', 'ibx-panel'); p.append(node); return p; }

const SAVED_OPEN_LABEL = 'Read the whole message', SAVED_CLOSE_LABEL = 'Show less';
const SAVED_WORDS = {
    gone: 'This message was deleted, so only the saved snippet is left.',
    not_reachable: 'This message cannot be read from here: its agent ended on another server.',
    too_long: 'This message is too long to show here.',
};

/**
 * Mount the Inbox panel into `host`. Returns { refresh, savedExpand, destroy }.
 *   refresh()            reads the tasks again
 *   savedExpand(item, which)    the "Read the whole message" control for one saved item: { root, button, region } (the host puts `root` in the item)
 *   destroy()            removes what was mounted and forgets the drafts
 * options.load === false: do not read the tasks at once (the host calls refresh() when the list is shown).
 * @param {HTMLElement} host @param {object} adapter @param {{ load?: boolean }} [options]
 */
export function mountInbox(host, adapter, options) {
    checkAdapter(adapter);
    const root = el('section', 'ibx');
    root.setAttribute('aria-label', 'Tasks for you');
    const live = el('div', 'ibx-live'); live.setAttribute('role', 'status'); live.setAttribute('aria-live', 'polite'); live.setAttribute('aria-atomic', 'true');
    const list = el('ul', 'ibx-tasks');
    const empty = el('p', 'ibx-empty'); empty.hidden = true;
    root.append(live, list, empty);
    host.append(root);
    let liveFlip = false;
    const say = msg => { liveFlip = !liveFlip; live.textContent = msg + (liveFlip ? '' : ' '); }; // the same words twice are still a change
    let gone = false;

    const openIds = new Set(); // tasks that are open, kept across a refresh (the list is rebuilt each time the tab is shown)
    // The drawn body of each open task with the updated_at it was read at (#5803): a refresh draws the same body again, without a read and without the host's link lookups, when the list says the task has
    // not changed (the same updated_at and revision). A task that changed, or one the list gives no updated_at for, is read and drawn again. Links that went stale in between are still refused by the server when they are followed.
    const drawn = new Map();
    // The change stamp: updated_at AND revision (an amendment moves the revision, an edit or a claim moves updated_at). A task the list gives either of no value for has no stamp, so it is read again every time.
    const stampOf = t => (t && t.updated_at != null && t.updated_at !== '' && t.revision != null && t.revision !== '' ? `${t.updated_at}#${t.revision}` : '');
    function taskRow(task) {
        const li = el('li', 'ibx-task');
        li.dataset.key = draftKey(task.project, task.id);
        const bodyId = uid('ibx-task-body');
        const head = el('button', 'ibx-task-head');
        head.type = 'button';
        head.setAttribute('aria-expanded', 'false');
        head.setAttribute('aria-controls', bodyId);
        const title = el('span', 'ibx-task-title', task.title || '(no title)');
        const meta = el('span', 'ibx-task-meta');
        const bits = [task.board, task.priority, task.status === 'review_pending' ? 'review' : '', ago(task.updated_at || task.created_at, adapter.now)].filter(Boolean);
        meta.textContent = bits.join(' · ');
        head.append(title, meta);
        const region = el('div', 'ibx-task-body');
        region.id = bodyId; // not a landmark: aria-controls and aria-expanded on the button already tie them together
        region.hidden = true;
        li.append(head, region);
        let loaded = false, loading = false;
        async function setOpen(open, announce) {
            head.setAttribute('aria-expanded', String(open));
            region.hidden = !open;
            if (open) openIds.add(li.dataset.key); else openIds.delete(li.dataset.key);
            if (!open || loaded || loading) return;
            loading = true;
            region.setAttribute('aria-busy', 'true');
            region.replaceChildren(el('p', 'ibx-loading', 'Reading the task…'));
            if (announce) say('Reading the task…');
            try {
                const full = await adapter.getTask(task.project, task.id);
                region.replaceChildren(...await taskBody(task, full || {}));
                loaded = true;
                if (stampOf(task)) drawn.set(li.dataset.key, { stamp: stampOf(task), nodes: [...region.childNodes] });
                if (announce) say(`${task.title || 'The task'} is open.`);
            } catch (e) {
                const msg = `Could not read the task (${e && e.message ? e.message : 'no answer'}).`;
                region.replaceChildren(el('p', 'ibx-error', msg));
                say(msg);
            } finally { loading = false; region.removeAttribute('aria-busy'); }
        }
        head.addEventListener('click', () => setOpen(head.getAttribute('aria-expanded') !== 'true', true));
        if (openIds.has(li.dataset.key)) {
            const hit = drawn.get(li.dataset.key);
            if (hit && hit.stamp === stampOf(task)) { region.replaceChildren(...hit.nodes); loaded = true; } // unchanged since it was drawn: the same body, the draft in it as it was
            setOpen(true, false);
        }
        return li;
    }

    async function taskBody(task, full) {
        // The meta line (who it is from, its status, and the link to the task at its right end), the message body alone in its panel, then the reply: boxes in boxes are not drawn (#5882).
        const out = [];
        const from = full.created_by || task.created_by;
        const facts = [from ? `From ${from}` : '', full.status || task.status || ''].filter(Boolean).join(' · ');
        const metaRow = el('div', 'ibx-task-metarow');
        if (facts) metaRow.append(el('p', 'ibx-task-facts', facts));
        const href = typeof adapter.taskHref === 'function' ? safePath(adapter.taskHref(task)) : '';
        if (href) { const a = el('a', 'ibx-open', 'Open the task'); a.setAttribute('href', safePath(href)); metaRow.append(a); }
        else if (typeof adapter.openTask === 'function') {
            const b = el('button', 'btn ibx-open', 'Open the task'); b.type = 'button';
            b.addEventListener('click', () => adapter.openTask(task));
            metaRow.append(b);
        }
        if (metaRow.childNodes.length) out.push(metaRow);
        const text = full.body == null ? '' : String(full.body);
        out.push(inset(text ? await renderBody(text, adapter) : el('p', 'ibx-empty', 'This task has no text.')));
        out.push(replyBox(task, from));
        return out;
    }

    function replyBox(task, creator) {
        const form = el('form', 'ibx-reply');
        form.noValidate = true;
        const key = draftKey(task.project, task.id);
        const id = uid('ibx-reply');
        const label = el('label', 'ibx-reply-label', `Your reply to ${task.title || 'this task'}`);
        label.setAttribute('for', id);
        const ta = el('textarea', 'ibx-reply-input');
        ta.id = id; ta.rows = 4; ta.maxLength = REPLY_MAX;
        ta.value = _drafts.get(key) || '';
        _boxes.add(ta);
        const send = el('button', 'btn btn-primary ibx-send', 'Send reply');
        send.type = 'submit';
        const status = el('p', 'ibx-reply-status'); // its words are spoken once, through the panel's live region (say), not by a second one here
        const note = el('p', 'ibx-note', 'A reply does not complete or change the task.');
        const row = el('div', 'ibx-reply-row'); row.append(send, status);
        form.append(label, ta, row, note);
        let busy = false;
        const sync = () => { send.disabled = busy || !ta.value.trim(); };
        ta.addEventListener('input', () => { if (ta.value) _drafts.set(key, ta.value); else _drafts.delete(key); sync(); });
        ta.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); form.requestSubmit(); } });
        sync();
        Promise.resolve().then(() => adapter.canReply()).then(can => {
            if (can && can.ok === false) {
                form.replaceChildren(el('p', 'ibx-note', can.reason || 'You can read this task here but not reply to it.'));
            }
        }).catch(() => { /* an adapter that cannot say leaves the box; the server is the gate */ });
        form.addEventListener('submit', async e => {
            e.preventDefault();
            const content = ta.value.trim();
            if (!content || busy) return;
            busy = true; sync(); status.textContent = 'Sending…';
            try {
                const res = await adapter.replyToTask({ project: task.project, taskId: task.id, content });
                const warnings = Array.isArray(res && res.warnings) ? res.warnings : [];
                const who = creator || 'its creator';
                const text = warnings.includes('creator_not_reachable')
                    ? `Reply saved on the board, but ${who} could not be notified.`
                    : (res && Array.isArray(res.notified) && res.notified.length ? `Reply sent. ${res.notified.join(', ')} notified.` : 'Reply sent.');
                _drafts.delete(key);
                ta.value = '';
                status.textContent = text;
                say(text);
                ta.focus(); // the Send button is disabled now (nothing to send): focus goes to the empty box, not to the page
            } catch (err) {
                status.textContent = err && err.message ? err.message : 'The reply was refused.';
                say(status.textContent);
            } finally { busy = false; sync(); }
        });
        return form;
    }

    async function refresh() {
        try {
            const tasks = await adapter.listTasks();
            if (gone) return;
            const rows = Array.isArray(tasks) ? tasks : [];
            const kept = document.activeElement && list.contains(document.activeElement) ? document.activeElement.closest('.ibx-task')?.dataset.key : '';
            const stillListed = new Set(rows.map(t => draftKey(t.project, t.id)));
            for (const k of [...drawn.keys()]) if (!stillListed.has(k)) drawn.delete(k);
            list.replaceChildren(...rows.map(taskRow));
            if (kept) list.querySelector(`.ibx-task[data-key="${CSS.escape(kept)}"] .ibx-task-head`)?.focus(); // a refresh does not take the focus from the task being read
            empty.hidden = rows.length > 0;
            empty.textContent = rows.length ? '' : 'No assigned tasks.';
        } catch (e) {
            if (gone) return;
            list.replaceChildren();
            empty.hidden = false;
            empty.textContent = `Could not read the tasks (${e && e.message ? e.message : 'no answer'}).`;
        }
    }

    function savedExpand(item, givenWhich) {
        const wrap = el('div', 'ibx-saved');
        const regionId = uid('ibx-saved-body');
        const button = el('button', 'btn ibx-saved-toggle', SAVED_OPEN_LABEL);
        button.type = 'button';
        button.setAttribute('aria-expanded', 'false');
        button.setAttribute('aria-controls', regionId);
        const which = typeof givenWhich === 'string' ? givenWhich : (() => { const t = String(item && item.snippet || '').replace(/\s+/g, ' ').trim(); const src = item && (item.title || item.ref) || ''; const c = [...t]; return [src, c.length > 40 ? c.slice(0, 40).join('') + '…' : t].filter(Boolean).join(', '); })(); // the host may say which message this is (it names apart two that start alike)
        const nameIt = text => { if (which) button.setAttribute('aria-label', `${text}: ${which}`); }; // the words on the button, then the message they are for (several items read as the same button otherwise, #6541)
        nameIt(SAVED_OPEN_LABEL);
        const region = el('div', 'ibx-saved-body');
        region.id = regionId;
        region.hidden = true;
        wrap.append(button, region);
        if (!adapter || typeof adapter.readSavedMessage !== 'function') { wrap.hidden = true; return { root: wrap, button, region }; }
        let loaded = false, loading = false;
        button.addEventListener('click', async () => {
            const open = button.getAttribute('aria-expanded') === 'true';
            button.setAttribute('aria-expanded', String(!open));
            button.textContent = open ? SAVED_OPEN_LABEL : SAVED_CLOSE_LABEL; nameIt(open ? SAVED_OPEN_LABEL : SAVED_CLOSE_LABEL); // a toggle: the words say what the next press does (#5882, the Operator's choice over a label that stays); aria-expanded still says which state it is in
            region.hidden = open;
            if (open || loaded || loading) return;
            loading = true;
            region.setAttribute('aria-busy', 'true');
            say('Reading the message…');
            region.replaceChildren(el('p', 'ibx-loading', 'Reading the message…'));
            try {
                const res = await adapter.readSavedMessage(item) || {};
                const parts = [];
                const reason = res.reason && SAVED_WORDS[res.reason];
                if (reason) parts.push(el('p', 'ibx-why', reason));
                if (res.text) parts.push(inset(await renderBody(res.text, adapter)));
                if (res.partial && res.reason !== 'too_long') parts.push(el('p', 'ibx-why', 'Shown in part: the message is very long.'));
                if (!parts.length) parts.push(el('p', 'ibx-why', 'There is no more text than the snippet.'));
                region.replaceChildren(...parts);
                loaded = !reason || !!res.text;
                say(reason || 'The message is open.');
            } catch (e) {
                const msg = `Could not read the message (${e && e.message ? e.message : 'no answer'}).`;
                region.replaceChildren(el('p', 'ibx-error', msg));
                say(msg);
            } finally { loading = false; region.removeAttribute('aria-busy'); }
        });
        return { root: wrap, button, region };
    }

    function destroy() { gone = true; for (const t of root.querySelectorAll('textarea')) _boxes.delete(t); root.remove(); clearInboxDrafts(); }

    if (!options || options.load !== false) refresh();
    return { refresh, savedExpand, destroy };
}
